package service

import (
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// 本文件钉住第 20.1.5 节状态机定稿要求的「支付已入账」唯一事件
// （internal/entity/payment_event.go / internal/service/payment_event.go）。
//
// 被修掉的真实漏洞：加款幂等原本只看 recharge_orders.status 是否 paid。
// 管理员把订单改成 refunded 之后，支付网关**重投**的同一条成功回调会因
// 「现在不等于 paid」而再次走「首次成功」分支，于是同一个网关订单被加款两次。

const paymentEventTestOrderNo = "rcg_payment_event_261109"

// seedPaymentEventOrder 建一张 pending 充值单 + 一条已签名的成功回调。
func seedPaymentEventOrder(t *testing.T, db *gorm.DB, userID int32, orderNo string, amount float64) (entity.RechargeOrder, url.Values) {
	t.Helper()

	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)

	order := entity.RechargeOrder{
		UserID: userID, OrderNo: orderNo, PaymentProvider: DefaultPaymentProvider,
		PaymentMethodNameSnapshot: "支付宝", Amount: amount, Status: rechargeOrderStatusPending,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create recharge order: %v", err)
	}

	values := url.Values{
		"pid": {"10001"}, "type": {"alipay"}, "out_trade_no": {orderNo},
		"trade_no": {"trade_" + orderNo}, "trade_status": {rechargeOrderTradeStatusSuccess},
		"money": {formatRechargeAmount(amount)},
	}
	signature, err := signRechargeValues(values, privateKey)
	if err != nil {
		t.Fatalf("sign notify values: %v", err)
	}
	values.Set("sign", signature)
	values.Set("sign_type", rechargeOrderSignType)
	return order, values
}

// requirePaymentEventSettledOnce 断言「钱只加了一次、账单只写一条、事件只有一条」。
func requirePaymentEventSettledOnce(t *testing.T, db *gorm.DB, userID, orderID int32, wantBalance float64) {
	t.Helper()

	var user entity.User
	if err := db.First(&user, userID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.Balance != wantBalance {
		t.Fatalf("wallet must be credited exactly once: got %v want %v", user.Balance, wantBalance)
	}
	var billingCount int64
	if err := db.Model(&entity.BillingRecord{}).
		Where("related_order_id = ? AND type = ?", orderID, rechargeBillingRecordType).
		Count(&billingCount).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if billingCount != 1 {
		t.Fatalf("recharge ledger must be written exactly once, got %d", billingCount)
	}
	var eventCount int64
	if err := db.Model(&entity.PaymentEvent{}).Where("recharge_order_id = ?", orderID).Count(&eventCount).Error; err != nil {
		t.Fatalf("count payment events: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("exactly one payment event must exist per gateway order, got %d", eventCount)
	}
	var auditCount int64
	if err := db.Model(&entity.Log{}).Where("module = ? AND action = ?", "recharge", 41).Count(&auditCount).Error; err != nil {
		t.Fatalf("count settlement audits: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("settlement audit must be written exactly once, got %d", auditCount)
	}
}

// TestPaymentEventFirstSuccessCallbackCreditsOnce 断言 1：
// 首次成功回调 → 加款一次、写一条账单、记一条事件、写一条到账审计。
func TestPaymentEventFirstSuccessCallbackCreditsOnce(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 301, "user")
	order, values := seedPaymentEventOrder(t, db, user.ID, paymentEventTestOrderNo, 12.35)

	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("first success callback: acknowledged=%v err=%v", acknowledged, err)
	}

	requirePaymentEventSettledOnce(t, db, user.ID, order.ID, 12.35)

	var settled entity.RechargeOrder
	if err := db.First(&settled, order.ID).Error; err != nil {
		t.Fatalf("load settled order: %v", err)
	}
	if settled.Status != rechargeOrderStatusPaid || settled.PaidAt == nil {
		t.Fatalf("first success callback must settle the order: %+v", settled)
	}
	var event entity.PaymentEvent
	if err := db.Where("order_no = ?", order.OrderNo).Take(&event).Error; err != nil {
		t.Fatalf("load payment event: %v", err)
	}
	if event.RechargeOrderID != order.ID || event.UserID != user.ID || event.Amount != 12.35 ||
		event.TradeNo != values.Get("trade_no") || event.TradeStatus != rechargeOrderTradeStatusSuccess ||
		event.Source != paymentEventSourceNotify || event.CreatedAt.IsZero() {
		t.Fatalf("payment event must snapshot the credited gateway order: %+v", event)
	}
}

// TestPaymentEventDuplicateSuccessCallbackDoesNotCreditAgain 是本 PR 的核心断言：
// 同一条成功回调重投 → 订单已 paid，直接 ACK，不再加款、不再写账单、不再写审计。
func TestPaymentEventDuplicateSuccessCallbackDoesNotCreditAgain(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 302, "user")
	order, values := seedPaymentEventOrder(t, db, user.ID, paymentEventTestOrderNo, 12.35)

	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("first success callback: acknowledged=%v err=%v", acknowledged, err)
	}
	var before entity.RechargeOrder
	if err := db.First(&before, order.ID).Error; err != nil {
		t.Fatalf("load settled order: %v", err)
	}

	for i := 0; i < 3; i++ {
		if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
			t.Fatalf("duplicate callback #%d: acknowledged=%v err=%v", i+1, acknowledged, err)
		}
	}

	requirePaymentEventSettledOnce(t, db, user.ID, order.ID, 12.35)

	var after entity.RechargeOrder
	if err := db.First(&after, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if after.PaidAt == nil || !after.PaidAt.Equal(*before.PaidAt) || after.Status != rechargeOrderStatusPaid ||
		after.CallbackPayload != before.CallbackPayload {
		t.Fatalf("duplicate callback must not mutate the settled order: before=%+v after=%+v", before, after)
	}
}

// TestPaymentEventCallbackAfterOrderMarkedRefundedDoesNotCreditAgain 是漏洞本体：
// 订单先被运营改成 refunded（status != paid，旧判定失效），同一条成功回调再来
// → 事件已存在，直接 ACK，**一分钱都不加**，且订单的 refunded 状态不被改回 paid。
func TestPaymentEventCallbackAfterOrderMarkedRefundedDoesNotCreditAgain(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 303, "user")
	order, values := seedPaymentEventOrder(t, db, user.ID, paymentEventTestOrderNo, 12.35)

	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("first success callback: acknowledged=%v err=%v", acknowledged, err)
	}

	// 运营把订单改成 refunded（这是真实后台操作，status 不再是 paid）。
	if err := db.Model(&entity.RechargeOrder{}).Where("id = ?", order.ID).
		Update("status", rechargeOrderStatusRefunded).Error; err != nil {
		t.Fatalf("mark order refunded: %v", err)
	}

	// 网关重投同一条成功回调。
	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("callback after refunded: acknowledged=%v err=%v", acknowledged, err)
	}

	requirePaymentEventSettledOnce(t, db, user.ID, order.ID, 12.35)

	var after entity.RechargeOrder
	if err := db.First(&after, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if after.Status != rechargeOrderStatusRefunded {
		t.Fatalf("replayed callback must not revive a refunded order, status=%q", after.Status)
	}
}

// TestPaymentEventConcurrentDuplicateCallbacksCreditOnce 断言 4：
// 并发两条同回调同时到达 → 只加款一次（唯一索引 + OnConflict{DoNothing} 分岔）。
func TestPaymentEventConcurrentDuplicateCallbacksCreditOnce(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	// 与 recharge_audit_test 同构：把内存 SQLite 的连接池收敛到 1 条，
	// 让并发回调真正在事务锁上排队，而不是被驱动层的写锁打断。
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("load sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	user := seedRechargeOrderUser(t, db, 304, "user")
	order, values := seedPaymentEventOrder(t, db, user.ID, paymentEventTestOrderNo, 12.35)

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values)
			if err == nil && !acknowledged {
				err = errors.New("verified callback was not acknowledged")
			}
			errs <- err
		}()
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent callback failed: %v", err)
		}
	}

	requirePaymentEventSettledOnce(t, db, user.ID, order.ID, 12.35)
}

// TestPaymentEventRefundDeductsAndLaterSuccessCallbackDoesNotCredit 断言 5：
// 退款照常扣款（用真实的退款钱包原语 lockTicketRefundWallet / updateTicketRefundWallet），
// 扣完后再来成功回调仍不加款 —— 事件不随钱包和订单状态变化而丢失。
func TestPaymentEventRefundDeductsAndLaterSuccessCallbackDoesNotCredit(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 305, "user")
	order, values := seedPaymentEventOrder(t, db, user.ID, paymentEventTestOrderNo, 12.35)

	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("first success callback: acknowledged=%v err=%v", acknowledged, err)
	}
	requirePaymentEventSettledOnce(t, db, user.ID, order.ID, 12.35)

	// 退款：走票券退款同一条钱包扣减原语（不是直接 UPDATE users.balance）。
	err := db.Transaction(func(tx *gorm.DB) error {
		wallet, err := lockTicketRefundWallet(tx, user.ID)
		if err != nil {
			return err
		}
		after := wallet.micros - 12_350_000 // 12.35 元 = 1235 分 = 12_350_000 微元
		if err := updateTicketRefundWallet(tx, user.ID, wallet, after); err != nil {
			return err
		}
		return tx.Model(&entity.RechargeOrder{}).Where("id = ?", order.ID).
			Update("status", rechargeOrderStatusRefunded).Error
	})
	if err != nil {
		t.Fatalf("refund must deduct the wallet: %v", err)
	}
	var refunded entity.User
	if err := db.First(&refunded, user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if refunded.Balance != 0 {
		t.Fatalf("refund must deduct the credited amount, balance=%v", refunded.Balance)
	}

	// 退款后网关重投同一条成功回调 → 不加款、不复活订单。
	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("callback after refund: acknowledged=%v err=%v", acknowledged, err)
	}
	var after entity.User
	if err := db.First(&after, user.ID).Error; err != nil {
		t.Fatalf("reload user after replay: %v", err)
	}
	if after.Balance != 0 {
		t.Fatalf("success callback after refund must not re-credit, balance=%v", after.Balance)
	}
	var eventCount, billingCount int64
	if err := db.Model(&entity.PaymentEvent{}).Where("recharge_order_id = ?", order.ID).Count(&eventCount).Error; err != nil {
		t.Fatalf("count payment events: %v", err)
	}
	if err := db.Model(&entity.BillingRecord{}).Where("related_order_id = ?", order.ID).Count(&billingCount).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if eventCount != 1 || billingCount != 1 {
		t.Fatalf("refund replay must keep one event and one recharge row: events=%d bills=%d", eventCount, billingCount)
	}
	var final entity.RechargeOrder
	if err := db.First(&final, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if final.Status != rechargeOrderStatusRefunded {
		t.Fatalf("refunded order must stay refunded, status=%q", final.Status)
	}
}

// TestPaymentEventNonSuccessCallbackDoesNotConsumeEvent 钉住「非成功状态不占事件」：
// WAIT_BUYER_PAY 只留痕不入账，之后真正的 TRADE_SUCCESS 仍必须能入账一次。
func TestPaymentEventNonSuccessCallbackDoesNotConsumeEvent(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 306, "user")
	order, values := seedPaymentEventOrder(t, db, user.ID, paymentEventTestOrderNo, 12.35)

	// 先用同一条已签名回调降级成「等待付款」再签一次（签名覆盖 trade_status）。
	waiting := clonePaymentEventValues(values)
	waiting.Set("trade_status", "WAIT_BUYER_PAY")
	resignPaymentEventValues(t, db, waiting)
	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, waiting); err != nil || !acknowledged {
		t.Fatalf("non-success callback: acknowledged=%v err=%v", acknowledged, err)
	}
	var eventCount int64
	if err := db.Model(&entity.PaymentEvent{}).Count(&eventCount).Error; err != nil {
		t.Fatalf("count payment events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("non-success callback must not record a credit event, got %d", eventCount)
	}
	var pending entity.User
	if err := db.First(&pending, user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if pending.Balance != 0 {
		t.Fatalf("non-success callback must not credit, balance=%v", pending.Balance)
	}

	// 真正的成功回调仍然入账一次。
	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("success callback after waiting: acknowledged=%v err=%v", acknowledged, err)
	}
	requirePaymentEventSettledOnce(t, db, user.ID, order.ID, 12.35)
}

// TestPaymentEventMissingTableFailsClosedWithoutCredit 钉住 fail-closed：
// 事件表还没迁移就**绝不加款**（宁可让网关重投，也不能冒重复加款的风险）。
// 这与充值赠送/返佣的「表缺失就降级」是**相反**的取舍，不能混为一谈。
func TestPaymentEventMissingTableFailsClosedWithoutCredit(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	if err := db.Migrator().DropTable(&entity.PaymentEvent{}); err != nil {
		t.Fatalf("drop payment_events: %v", err)
	}
	user := seedRechargeOrderUser(t, db, 307, "user")
	order, values := seedPaymentEventOrder(t, db, user.ID, paymentEventTestOrderNo, 12.35)

	acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values)
	if !errors.Is(err, ErrRechargePaymentEventUnavailable) {
		t.Fatalf("missing payment_events must fail closed, got ack=%v err=%v", acknowledged, err)
	}
	if acknowledged {
		t.Fatal("missing payment_events must not be acknowledged")
	}
	var user0 entity.User
	if err := db.First(&user0, user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	var order0 entity.RechargeOrder
	if err := db.First(&order0, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	var billingCount int64
	if err := db.Model(&entity.BillingRecord{}).Count(&billingCount).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if user0.Balance != 0 || order0.Status != rechargeOrderStatusPending || order0.PaidAt != nil || billingCount != 0 {
		t.Fatalf("fail-closed must leave no financial trace: balance=%v status=%q paidAt=%v bills=%d",
			user0.Balance, order0.Status, order0.PaidAt, billingCount)
	}
}

// TestPaymentEventIsNotMutableAcrossOrderStateChange 钉住「事件只增不改」：
// 订单被改成任意状态、甚至被删掉，事件行与它的 created_at 都不变。
func TestPaymentEventIsNotMutableAcrossOrderStateChange(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 308, "user")
	order, values := seedPaymentEventOrder(t, db, user.ID, paymentEventTestOrderNo, 12.35)

	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("first success callback: acknowledged=%v err=%v", acknowledged, err)
	}
	var before entity.PaymentEvent
	if err := db.Where("order_no = ?", order.OrderNo).Take(&before).Error; err != nil {
		t.Fatalf("load payment event: %v", err)
	}

	for _, status := range []string{rechargeOrderStatusRefunded, rechargeOrderStatusPending, "closed"} {
		if err := db.Model(&entity.RechargeOrder{}).Where("id = ?", order.ID).Update("status", status).Error; err != nil {
			t.Fatalf("set status %q: %v", status, err)
		}
		if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
			t.Fatalf("callback while status=%q: acknowledged=%v err=%v", status, acknowledged, err)
		}
		var current entity.PaymentEvent
		if err := db.Where("order_no = ?", order.OrderNo).Take(&current).Error; err != nil {
			t.Fatalf("reload payment event: %v", err)
		}
		if current.ID != before.ID || !current.CreatedAt.Equal(before.CreatedAt) || current.Amount != before.Amount ||
			current.TradeNo != before.TradeNo || current.Source != before.Source {
			t.Fatalf("payment event mutated while status=%q: before=%+v after=%+v", status, before, current)
		}
	}
	var after entity.User
	if err := db.First(&after, user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if after.Balance != 12.35 {
		t.Fatalf("order state changes must never move money again, balance=%v", after.Balance)
	}
	var eventCount int64
	if err := db.Model(&entity.PaymentEvent{}).Count(&eventCount).Error; err != nil || eventCount != 1 {
		t.Fatalf("exactly one immutable event expected: count=%d err=%v", eventCount, err)
	}
}

// TestPaymentEventBackfilledForAlreadyPaidOrderWithoutCredit 钉住「补锚点不加款」：
// 一张已 paid 但没有事件行的订单（历史回填尚未成功 / 回填后被漏掉），
// 再来成功回调时必须补上事件锚点，但余额、账单、审计都不许再动。
func TestPaymentEventBackfilledForAlreadyPaidOrderWithoutCredit(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 309, "user")
	order, values := seedPaymentEventOrder(t, db, user.ID, paymentEventTestOrderNo, 12.35)

	// 直接把订单摆成「已入账但没有事件」的历史形态：钱已到账、账单已写、事件缺失。
	paidAt := time.Now().Add(-time.Hour)
	if err := db.Model(&entity.RechargeOrder{}).Where("id = ?", order.ID).
		Updates(map[string]any{"status": rechargeOrderStatusPaid, "paid_at": &paidAt}).Error; err != nil {
		t.Fatalf("mark order paid: %v", err)
	}
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 12.35).Error; err != nil {
		t.Fatalf("seed balance: %v", err)
	}
	if err := db.Create(&entity.BillingRecord{
		UserID: user.ID, Type: rechargeBillingRecordType, Provider: DefaultPaymentProvider,
		Amount: 12.35, BalanceBefore: 0, BalanceAfter: 12.35,
		RequestID: order.OrderNo, RelatedOrderID: order.ID, Remark: "账户充值",
	}).Error; err != nil {
		t.Fatalf("seed billing record: %v", err)
	}

	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("callback on paid order without event: acknowledged=%v err=%v", acknowledged, err)
	}

	var after entity.User
	if err := db.First(&after, user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if after.Balance != 12.35 {
		t.Fatalf("healing the event anchor must never credit again, balance=%v", after.Balance)
	}
	var billingCount, auditCount, eventCount int64
	if err := db.Model(&entity.BillingRecord{}).Where("related_order_id = ?", order.ID).Count(&billingCount).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if err := db.Model(&entity.Log{}).Where("module = ? AND action = ?", "recharge", 41).Count(&auditCount).Error; err != nil {
		t.Fatalf("count settlement audits: %v", err)
	}
	if err := db.Model(&entity.PaymentEvent{}).Where("recharge_order_id = ?", order.ID).Count(&eventCount).Error; err != nil {
		t.Fatalf("count payment events: %v", err)
	}
	if billingCount != 1 || auditCount != 0 || eventCount != 1 {
		t.Fatalf("heal must add the anchor only: bills=%d audits=%d events=%d", billingCount, auditCount, eventCount)
	}
	var paid entity.RechargeOrder
	if err := db.First(&paid, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if paid.PaidAt == nil || !paid.PaidAt.Equal(paidAt) {
		t.Fatalf("heal must not rewrite the original paid_at: %+v", paid.PaidAt)
	}
}

// clonePaymentEventValues 深拷贝回调参数，避免并发/多次使用时互相污染。
func clonePaymentEventValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, list := range values {
		clone[key] = append([]string(nil), list...)
	}
	return clone
}

// resignPaymentEventValues 用测试库里的商户私钥重新签名（改过字段后必须重签）。
func resignPaymentEventValues(t *testing.T, db *gorm.DB, values url.Values) {
	t.Helper()

	settings, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		t.Fatalf("load system settings: %v", err)
	}
	values.Del("sign")
	values.Del("sign_type")
	signature, err := signRechargeValues(values, settings.EPayMerchantPrivateKey)
	if err != nil {
		t.Fatalf("resign notify values: %v", err)
	}
	values.Set("sign", signature)
	values.Set("sign_type", rechargeOrderSignType)
}
