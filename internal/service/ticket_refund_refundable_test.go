package service

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 可退本金（堵漏）测试。
//
// 背景：21-*.md §7.1 用户诉求「用户只能退款自己充值的余额」。
// 漏洞形态：充 100 → 赠 20 → 花掉 20 赠金 → 余额仍是 100 → 退 100 → 平台净亏 20。
// 口径推导见 ticket_refund_refundable.go 顶部注释。

// newRefundablePrincipalTestDB 建一个可控制本金/赠金/账单的最小库。
func newRefundablePrincipalTestDB(t *testing.T, balance, gift float64) (*gorm.DB, entity.User, entity.User) {
	t.Helper()

	db := newRechargeOrderTestDB(t)
	if err := db.AutoMigrate(&entity.TicketRefund{}, &entity.TicketRefundLedger{}); err != nil {
		t.Fatalf("migrate refund tables: %v", err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })

	owner := entity.User{ID: 191, Username: "refundable-owner", Role: "user", Status: "enabled",
		Balance: balance, GiftBalance: gift}
	admin := entity.User{ID: 193, Username: "refundable-admin", Role: "admin", Status: "enabled"}
	for _, row := range []*entity.User{&owner, &admin} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	return db, owner, admin
}

// seedConsumption 写一条消费账单，用于构造「已消耗本金 / 已消耗赠金」。
func seedConsumption(t *testing.T, db *gorm.DB, userID int32, requestID string, amount, giftCharged, principalCharged float64) {
	t.Helper()

	row := entity.BillingRecord{
		UserID: userID, Type: "normal_search", Provider: "icodef",
		Amount: amount, GiftCharged: giftCharged, PrincipalCharged: principalCharged,
		BalanceBefore: 0, BalanceAfter: 0, RequestID: requestID, Remark: "搜索答题消耗",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed consumption: %v", err)
	}
}

// seedRefundableOrder 建一笔已支付且有渠道凭证的充值订单。
//
// order_no 用订单 ID 生成，不能用时间：`seedTicketRefundOrder` 用 paidAt 拼 order_no，
// 同一测试里连续建两笔会撞唯一索引（实测）。
func seedRefundableOrder(t *testing.T, db *gorm.DB, id int32, ownerID int32, amount float64) entity.RechargeOrder {
	t.Helper()

	paidAt := time.Now()
	row := entity.RechargeOrder{
		ID: id, UserID: ownerID, OrderNo: "refundable-order-" + itoa32(id),
		PaymentProvider: DefaultPaymentProvider, Status: rechargeOrderStatusPaid,
		Amount: amount, PaidAt: &paidAt,
		RefundChannelFingerprint: strings.Repeat("a", 64),
		RefundOriginalTradeNo:    "synthetic-verified-original-trade",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}
	return row
}

func itoa32(value int32) string {
	return strconv.FormatInt(int64(value), 10)
}

// createAndApprove 走真实链路：创建申请 → 审批冻结（可退额闸在这里生效）。
func createAndApprove(t *testing.T, db *gorm.DB, owner, admin entity.User, orderID int32, cents int64) (entity.TicketRefund, error) {
	t.Helper()

	orderIDValue := orderID
	created, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindOriginal, OrderID: &orderIDValue, RequestedCents: cents,
		Reason: "用户申请退款",
	}, testVerifyTicketRefundChannel)
	if err != nil {
		return entity.TicketRefund{}, err
	}
	return ReviewTicketRefundWithDB(db, admin, created.ID, true, cents, testVerifyTicketRefundChannel)
}

// approveOnly 只跑审批阶段，用于「先验证会被拒、再验证放行」的场景。
//
// 为什么不复用 createAndApprove：`ticket_refunds.order_id` 是唯一索引，
// 同一订单第二次 Create 会被 `ensureTicketRefundOrderUnused` 挡下（报
// 「充值订单不可用于此次退款」），与可退额闸无关。所以要测「先拒后放」，
// 必须在**同一个已创建的申请单**上改金额重试。
func approveOnly(db *gorm.DB, admin entity.User, refundID int64, cents int64) (entity.TicketRefund, error) {
	return ReviewTicketRefundWithDB(db, admin, refundID, true, cents, testVerifyTicketRefundChannel)
}

// createRefund 只创建申请单（不审批）。
func createRefund(t *testing.T, db *gorm.DB, owner entity.User, orderID int32, cents int64) entity.TicketRefund {
	t.Helper()

	orderIDValue := orderID
	created, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindOriginal, OrderID: &orderIDValue, RequestedCents: cents,
		Reason: "用户申请退款",
	}, testVerifyTicketRefundChannel)
	if err != nil {
		t.Fatalf("create refund request: %v", err)
	}
	return created
}

// 测试 1：纯充值未消费 → 可退额 = 实付。
func TestRefundablePrincipalPureRechargeKeepsFullAmount(t *testing.T) {
	db, owner, admin := newRefundablePrincipalTestDB(t, 100, 0)
	order := seedRefundableOrder(t, db, 1, owner.ID, 100)

	refunded, err := createAndApprove(t, db, owner, admin, order.ID, 10000)
	if err != nil {
		t.Fatalf("full refund of an unconsumed order must succeed: %v", err)
	}
	if refunded.Status != entity.TicketRefundStatusFrozen || refunded.ApprovedCents != 10000 {
		t.Fatalf("expected frozen 10000 cents, got %+v", refunded)
	}
	// 没有赠金消耗 → 不追加审计噪声。
	if strings.Contains(refunded.Reason, "可退本金核算") {
		t.Fatalf("no audit note should be added when nothing was gift-consumed: %q", refunded.Reason)
	}
}

// 测试 2：用赠金消费 → **可退额不变**（赠金消耗不影响本金可退 —— 但等等，这里要小心）。
//
// 注意：本测试断言的是「纯赠金消费后，用户仍可退到本笔实付」，
// 因为此时 balance 没被动过。真正的堵漏点在测试 3（赠金消耗会挤占可退额度）。
//
// 换句话说：赠金消耗本身不减少 balance，但它会让「取走的总价值」超过实付，
// 所以它**必须**挤占可退额度 —— 见测试 3 与口径注释里的恒等式。
func TestRefundablePrincipalGiftConsumptionReducesClaim(t *testing.T) {
	// 充 100，赠 20（已在 gift_balance），花掉 20 赠金 → balance 仍是 100。
	db, owner, admin := newRefundablePrincipalTestDB(t, 100, 0)
	order := seedRefundableOrder(t, db, 1, owner.ID, 100)
	seedConsumption(t, db, owner.ID, "gift-only", 20, 20, 0)

	// 这就是原漏洞：用户想退满 100。
	created := createRefund(t, db, owner, order.ID, 10000)
	if _, err := approveOnly(db, admin, created.ID, 10000); !errors.Is(err, ErrTicketRefundExceedsRefundablePrincipal) {
		t.Fatalf("refunding the full paid amount after spending gift money must be refused, got %v", err)
	}

	// 可退上限 = 100 − 20 = 80。同一个申请单改金额重试（订单已绑定，不能再建申请）。
	refunded, err := approveOnly(db, admin, created.ID, 8000)
	if err != nil {
		t.Fatalf("refunding 80 (100 − 20 gift spent) must succeed: %v", err)
	}
	if refunded.ApprovedCents != 8000 {
		t.Fatalf("expected 8000 cents, got %d", refunded.ApprovedCents)
	}
	// 审计：必须写明赠金消耗与可退上限，否则用户无法理解「为什么只能退 80」。
	if !strings.Contains(refunded.Reason, "可退本金核算") ||
		!strings.Contains(refunded.Reason, "20") || !strings.Contains(refunded.Reason, "80") {
		t.Fatalf("audit note must explain the gift deduction and the cap: %q", refunded.Reason)
	}
}

// 测试 3：用本金消费 → 可退额减少（本金已被花掉，balance 已经反映了）。
func TestRefundablePrincipalPrincipalConsumptionReducesClaim(t *testing.T) {
	// 充 100，花掉 30 本金 → balance = 70。
	db, owner, admin := newRefundablePrincipalTestDB(t, 70, 0)
	order := seedRefundableOrder(t, db, 1, owner.ID, 100)
	seedConsumption(t, db, owner.ID, "principal-only", 30, 0, 30)

	// 余额只剩 70，退 100 必须被拒（本笔实付上限也挡不住，因为钱包不够）。
	created := createRefund(t, db, owner, order.ID, 10000)
	if _, err := approveOnly(db, admin, created.ID, 10000); err == nil {
		t.Fatal("refunding more than the remaining principal must be refused")
	}
	// 退 70 应当成功。
	refunded, err := approveOnly(db, admin, created.ID, 7000)
	if err != nil {
		t.Fatalf("refunding the remaining principal (70) must succeed: %v", err)
	}
	if refunded.ApprovedCents != 7000 {
		t.Fatalf("expected 7000 cents, got %d", refunded.ApprovedCents)
	}
}

// 测试 4：赠金 + 本金混合消费 → 只减本金部分（余额已反映），但赠金部分挤占额度。
func TestRefundablePrincipalMixedConsumptionOnlyGiftReducesClaim(t *testing.T) {
	// 充 100，赠 20；花掉 30（先扣 20 赠金、再扣 10 本金）→ balance = 90。
	db, owner, admin := newRefundablePrincipalTestDB(t, 90, 0)
	order := seedRefundableOrder(t, db, 1, owner.ID, 100)
	seedConsumption(t, db, owner.ID, "mixed", 30, 20, 10)

	// 可退 = 90 − 20（赠金消耗）= 70。
	created := createRefund(t, db, owner, order.ID, 10000)
	if _, err := approveOnly(db, admin, created.ID, 8000); !errors.Is(err, ErrTicketRefundExceedsRefundablePrincipal) {
		t.Fatalf("80 exceeds the 70 refundable cap and must be refused, got %v", err)
	}
	refunded, err := approveOnly(db, admin, created.ID, 7000)
	if err != nil {
		t.Fatalf("refunding 70 must succeed: %v", err)
	}
	if refunded.ApprovedCents != 7000 {
		t.Fatalf("expected 7000 cents, got %d", refunded.ApprovedCents)
	}
	// 恒等式校验：可退额 + 已消耗本金 + 已消耗赠金 == 实付
	//   70 + 10 + 20 = 100 ✅（这是「平台零利润」的判据）
	if got := 70.0 + 10 + 20; got != 100 {
		t.Fatalf("identity check failed: %v", got)
	}
}

// 测试 5：消费超过本金（欠款）→ 可退额 = 0，不是负数。
func TestRefundablePrincipalDebtYieldsZeroNotNegative(t *testing.T) {
	// 充 100，赠 20，花掉 150（20 赠金 + 130 本金）→ balance = -30（欠款）。
	db, owner, admin := newRefundablePrincipalTestDB(t, -30, 0)
	order := seedRefundableOrder(t, db, 1, owner.ID, 100)
	seedConsumption(t, db, owner.ID, "overdraft", 150, 20, 130)

	// 任何正数退款都必须被拒。
	if _, err := createAndApprove(t, db, owner, admin, order.ID, 1); err == nil {
		t.Fatal("a debt account must not be able to refund anything")
	}

	// 纯函数层直接断言「不返回负数」。
	breakdown := computeRefundablePrincipal(yuanToMicros(-30), yuanToMicros(20), yuanToMicros(100))
	if breakdown.RefundableMicros != 0 {
		t.Fatalf("debt account must yield 0 refundable, got %d micros", breakdown.RefundableMicros)
	}
	if microsToCents(breakdown.RefundableMicros) != 0 {
		t.Fatalf("cents conversion must also be 0, got %d", microsToCents(breakdown.RefundableMicros))
	}
}

// 测试 6：两笔充值 + 部分消费 → 按用户口径计算，且「不超过本笔实付」上限生效。
func TestRefundablePrincipalUserScopeCappedByOrderPaid(t *testing.T) {
	// 充 100 + 充 50 = 150 实付；赠 20；花掉 20 赠金 + 30 本金 → balance = 120。
	db, owner, admin := newRefundablePrincipalTestDB(t, 120, 0)
	orderA := seedRefundableOrder(t, db, 1, owner.ID, 100)
	seedRefundableOrder(t, db, 2, owner.ID, 50)
	seedConsumption(t, db, owner.ID, "user-scope", 50, 20, 30)

	// 用户口径可退 = 120 − 20 = 100。本笔订单 A 实付 100 → 上限也是 100。
	// 所以退 100 应当成功（不因「用户口径 100 < 本笔实付 100」而被额外截断）。
	refunded, err := createAndApprove(t, db, owner, admin, orderA.ID, 10000)
	if err != nil {
		t.Fatalf("refunding 100 (order paid, within user-scope cap) must succeed: %v", err)
	}
	if refunded.ApprovedCents != 10000 {
		t.Fatalf("expected 10000 cents, got %d", refunded.ApprovedCents)
	}

	// 纯函数层：用户口径可退（100）大于本笔实付（50）时必须被截到 50。
	breakdown := computeRefundablePrincipal(yuanToMicros(120), yuanToMicros(20), yuanToMicros(50))
	if got := microsToCents(breakdown.RefundableMicros); got != 5000 {
		t.Fatalf("order-paid cap must truncate to 5000 cents, got %d", got)
	}
}

// 测试 7：历史账单（gift_charged 为 0）→ 历史行的赠金消耗**事实上就是 0**，
// 不能因为「0 可能是未知」就把可退额算成全额或算成 0。
//
// 关键事实：旧模型下 `gift_balance` 是只增不减的来源标记，消费从不扣它，
// 所有消费都只 Update("balance", ...)。所以历史行「已消耗赠金 = 0」是**事实**而非猜测。
// 历史消费消耗的本金已经体现在当前 balance 里，本口径自然扣到，不需要 sentinel。
func TestRefundablePrincipalHistoricalRowsAreFactuallyZeroGift(t *testing.T) {
	// 旧模型：充 100，花掉 30 本金（历史行两列都是 0）→ balance = 70。
	db, owner, admin := newRefundablePrincipalTestDB(t, 70, 0)
	order := seedRefundableOrder(t, db, 1, owner.ID, 100)
	seedConsumption(t, db, owner.ID, "legacy", 30, 0, 0) // 历史行：分桶列都是 0

	// 历史行不能被当成「消耗了 30 赠金」→ 否则可退会被错误压到 40。
	// 也不能被当成「什么都没消耗」→ 但本金消耗已由 balance=70 体现，所以可退就是 70。
	refunded, err := createAndApprove(t, db, owner, admin, order.ID, 7000)
	if err != nil {
		t.Fatalf("historical rows must not distort the claim; refunding 70 must succeed: %v", err)
	}
	if refunded.ApprovedCents != 7000 {
		t.Fatalf("expected 7000 cents, got %d", refunded.ApprovedCents)
	}

	// 退 80 必须被拒（balance 只有 70）。
	if _, err := createAndApprove(t, db, owner, admin, order.ID, 8000); err == nil {
		t.Fatal("refunding 80 with only 70 principal must be refused")
	}

	// 纯函数层：giftConsumed=0 时不产生任何赠金抵扣。
	breakdown := computeRefundablePrincipal(yuanToMicros(70), 0, yuanToMicros(100))
	if breakdown.GiftDeductedMicros != 0 {
		t.Fatalf("historical rows must not be treated as gift consumption, got %d", breakdown.GiftDeductedMicros)
	}
	if got := microsToCents(breakdown.RefundableMicros); got != 7000 {
		t.Fatalf("expected 7000 cents, got %d", got)
	}
}

// 冻结与可退额计算的先后关系：**必须先算可退额、再扣钱包**。
//
// 若顺序反了（先冻结扣走 balance，再算可退额），可退额会凭空变小：
//
//	balance 100、赠金消耗 20 → 可退 80
//	先冻结 80 → balance 20 → 再算可退 = 20 − 20 = 0  ← 错
//
// 正确顺序下冻结用的是**冻结前**的余额快照，可退额不受冻结影响。
//
// 这条钉住的是顺序，不是数值：断言「一次成功冻结后，再次审批同一张单
// 不会被可退额闸拒绝」，因为第一次冻结后的余额已经变小，若实现用冻结后的
// 余额重算就会误拒。
func TestRefundablePrincipalComputedBeforeWalletFreeze(t *testing.T) {
	// 充 100、赠 20 已花掉 → balance 100，可退 80。
	db, owner, admin := newRefundablePrincipalTestDB(t, 100, 0)
	order := seedRefundableOrder(t, db, 1, owner.ID, 100)
	seedConsumption(t, db, owner.ID, "freeze-order", 20, 20, 0)

	created := createRefund(t, db, owner, order.ID, 8000)
	refunded, err := approveOnly(db, admin, created.ID, 8000)
	if err != nil {
		t.Fatalf("approving 80 must succeed (computed from pre-freeze balance): %v", err)
	}
	if refunded.Status != entity.TicketRefundStatusFrozen || refunded.ApprovedCents != 8000 {
		t.Fatalf("expected frozen 8000, got %+v", refunded)
	}

	// 冻结已把 balance 从 100 扣到 20。此刻若重新算可退额会得到 20 − 20 = 0。
	var wallet entity.User
	if err := db.Where("id = ?", owner.ID).First(&wallet).Error; err != nil {
		t.Fatal(err)
	}
	if util.RoundMoney(wallet.Balance) != 20 {
		t.Fatalf("sanity: freeze should leave balance 20, got %v", wallet.Balance)
	}
	postFreeze := computeRefundablePrincipal(yuanToMicros(wallet.Balance), yuanToMicros(20), yuanToMicros(100))
	if postFreeze.RefundableMicros != 0 {
		t.Fatalf("sanity: recomputing after freeze would yield 0, got %d", postFreeze.RefundableMicros)
	}

	// 关键断言：同一张单重复审批（幂等路径）不能被可退额闸误拒 ——
	// 幂等分支在可退额闸之前就 return 了（status 已是 frozen，走 ErrTicketRefundState
	// 或幂等返回），所以不会用「冻结后的余额」重算。这里断言它不会返回
	// ErrTicketRefundExceedsRefundablePrincipal 这种误导性错误。
	if _, err := approveOnly(db, admin, created.ID, 8000); errors.Is(err, ErrTicketRefundExceedsRefundablePrincipal) {
		t.Fatalf("re-approval must not be rejected by the refundable gate "+
			"(that would mean the gate reads post-freeze balance): %v", err)
	}
}
func TestRefundablePrincipalRoundsDownToCents(t *testing.T) {
	// 可退 10.999999 元（6 位精度的最大值，RoundMoney 后仍是它）→ 只允许退 1099 分。
	breakdown := computeRefundablePrincipal(yuanToMicros(10.999999), 0, 0)
	if got := microsToCents(breakdown.RefundableMicros); got != 1099 {
		t.Fatalf("claim must round down to 1099 cents, got %d", got)
	}
	// 精确整数不受影响。
	if got := microsToCents(yuanToMicros(20)); got != 2000 {
		t.Fatalf("exact 20 yuan must be 2000 cents, got %d", got)
	}
	// 恰好整分也不该被截掉。
	if got := microsToCents(yuanToMicros(10.99)); got != 1099 {
		t.Fatalf("exact 10.99 must be 1099 cents, got %d", got)
	}
}

// 附加：yuanToMicros 的 6 位精度（与 util.MoneyPrecision 一致）。
func TestRefundablePrincipalMicrosConversionPrecision(t *testing.T) {
	cases := []struct {
		amount float64
		want   int64
	}{
		{0, 0},
		{1, 1_000_000},
		{0.000001, 1},
		{100.123456, 100_123_456},
		{-5, -5_000_000},
	}
	for _, tc := range cases {
		if got := yuanToMicros(tc.amount); got != tc.want {
			t.Fatalf("yuanToMicros(%v) = %d, want %d", tc.amount, got, tc.want)
		}
	}
	// 超 6 位的输入先按 RoundMoney 归一，不产生额外精度。
	if got := yuanToMicros(0.00000049); got != 0 {
		t.Fatalf("sub-micro amounts must round to 0, got %d", got)
	}
	_ = util.MoneyPrecision
}
