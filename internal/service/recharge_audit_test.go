package service

import (
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func seedRechargeAuditNotify(t *testing.T, db *gorm.DB) (entity.User, entity.RechargeOrder, url.Values) {
	t.Helper()
	user := seedRechargeOrderUser(t, db, 124, "user")
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)
	order := entity.RechargeOrder{
		UserID: user.ID, OrderNo: "rcg_audit_notify", Amount: 9.25,
		PaymentProvider: DefaultPaymentProvider, PaymentMethodNameSnapshot: "支付宝", Status: rechargeOrderStatusPending,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	values := url.Values{
		"pid": {"10001"}, "type": {"alipay"}, "out_trade_no": {order.OrderNo},
		"trade_no": {"trade_audit"}, "trade_status": {rechargeOrderTradeStatusSuccess},
		"money": {"9.25"}, "credentialURL": {"https://pay.example.com/secret-payment-link"},
	}
	signature, err := signRechargeValues(values, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	values.Set("sign", signature)
	values.Set("sign_type", rechargeOrderSignType)
	return user, order, values
}

func TestRechargeAuditConcurrentCallbacksSettleAndLogOnce(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	user, order, values := seedRechargeAuditNotify(t, db)
	const count = 8
	start := make(chan struct{})
	errorsOut := make(chan error, count)
	var group sync.WaitGroup
	for i := 0; i < count; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values)
			if err == nil && !acknowledged {
				err = errors.New("verified callback was not acknowledged")
			}
			errorsOut <- err
		}()
	}
	close(start)
	group.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatalf("concurrent callback failed: %v", err)
		}
	}
	var updated entity.User
	if err := db.First(&updated, user.ID).Error; err != nil || updated.Balance != order.Amount {
		t.Fatalf("balance must increase once: user=%+v err=%v", updated, err)
	}
	var billingCount int64
	if err := db.Model(&entity.BillingRecord{}).Where("related_order_id = ?", order.ID).Count(&billingCount).Error; err != nil || billingCount != 1 {
		t.Fatalf("billing must be written once: count=%d err=%v", billingCount, err)
	}
	var logs []entity.Log
	if err := db.Where("module = ? AND action = ?", "recharge", 41).Find(&logs).Error; err != nil || len(logs) != 1 {
		t.Fatalf("audit must be written once: logs=%+v err=%v", logs, err)
	}
	for _, value := range []string{values.Get("sign"), values.Get("credentialURL"), "sign_type", "credentialURL"} {
		if strings.Contains(logs[0].OldAnswer+logs[0].NewAnswer, value) {
			t.Fatalf("payment credential leaked into audit: %q", value)
		}
	}
}

func TestRechargeAuditFailureRollsBackSettlementAndCanRetry(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user, order, values := seedRechargeAuditNotify(t, db)
	const callbackName = "test:recharge_audit_failure"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if entry, ok := tx.Statement.Dest.(*entity.Log); ok && entry.Module == "recharge" && entry.Action == 41 {
			tx.AddError(errors.New("audit write failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })
	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err == nil || acknowledged {
		t.Fatalf("audit failure must fail the transaction: acknowledged=%v err=%v", acknowledged, err)
	}
	var updated entity.User
	var persisted entity.RechargeOrder
	var billingCount int64
	var auditCount int64
	if err := db.First(&updated, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	db.Model(&entity.BillingRecord{}).Where("related_order_id = ?", order.ID).Count(&billingCount)
	db.Model(&entity.Log{}).Where("module = ? AND action = ?", "recharge", 41).Count(&auditCount)
	if updated.Balance != 0 || persisted.Status != rechargeOrderStatusPending || billingCount != 0 || auditCount != 0 {
		t.Fatalf("settlement escaped rollback: balance=%v status=%s bills=%d logs=%d", updated.Balance, persisted.Status, billingCount, auditCount)
	}
	if err := db.Callback().Create().Remove(callbackName); err != nil {
		t.Fatal(err)
	}
	if acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values); err != nil || !acknowledged {
		t.Fatalf("callback retry should succeed after audit recovery: acknowledged=%v err=%v", acknowledged, err)
	}
}

func TestRechargeAuditUnknownGatewayResultDoesNotLeakOrRepeat(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 125, "user")
	method := seedRechargePaymentMethod(t, db, entity.PaymentMethod{Name: "微信支付", EPayType: "wxpay", Enabled: true})
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)
	original := createRechargePaymentRequest
	t.Cleanup(func() { createRechargePaymentRequest = original })
	calls := 0
	createRechargePaymentRequest = func(_ RechargePaymentConfig, _ RechargePayRequest) (RechargePaymentResult, error) {
		calls++
		return RechargePaymentResult{}, errors.New("response lost at https://pay.example.com?sign=sensitive-signature")
	}
	input := CreateRechargeOrderInput{Amount: 8, PaymentMethodID: method.ID, ClientIP: "203.0.113.1"}
	for i := 0; i < 2; i++ {
		if _, err := CreateRechargeOrderIdempotentWithDB(db, user, input, "audit-unknown-key"); !errors.Is(err, ErrIdempotencyPending) {
			t.Fatalf("expected unknown gateway result: %v", err)
		}
	}
	var logs []entity.Log
	if err := db.Where("module = ? AND action = ?", "recharge", 40).Find(&logs).Error; err != nil || len(logs) != 1 || calls != 1 {
		t.Fatalf("replay duplicated gateway or audit: calls=%d logs=%+v err=%v", calls, logs, err)
	}
	if !strings.Contains(logs[0].NewAnswer, `"status":"pending"`) || !strings.Contains(logs[0].NewAnswer, `"code":"PAYMENT_RESULT_UNKNOWN"`) || logs[0].ClientIP != input.ClientIP {
		t.Fatalf("unexpected safe pending audit: %+v", logs[0])
	}
	if strings.Contains(logs[0].OldAnswer+logs[0].NewAnswer, "sensitive-signature") || strings.Contains(logs[0].NewAnswer, "https://") {
		t.Fatalf("gateway error details leaked into audit: %+v", logs[0])
	}
}

func TestRechargeAuditFailureDoesNotDiscardCreatedLegacyOrder(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 126, "user")
	method := seedRechargePaymentMethod(t, db, entity.PaymentMethod{Name: "微信支付", EPayType: "wxpay", Enabled: true})
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)
	original := createRechargePaymentRequest
	t.Cleanup(func() { createRechargePaymentRequest = original })
	calls := 0
	createRechargePaymentRequest = func(_ RechargePaymentConfig, _ RechargePayRequest) (RechargePaymentResult, error) {
		calls++
		return RechargePaymentResult{PayType: "qrcode", PayInfo: "qr:legacy", ThirdPartyOrderNo: "trade-legacy"}, nil
	}
	if err := db.Callback().Create().Before("gorm:create").Register("fail_legacy_create_audit", func(tx *gorm.DB) {
		if entry, ok := tx.Statement.Dest.(*entity.Log); ok && entry.Module == "recharge" && entry.Action == 40 {
			tx.AddError(errors.New("audit unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	created, err := CreateRechargeOrderWithDB(db, user, CreateRechargeOrderInput{Amount: 8, PaymentMethodID: method.ID})
	if err != nil || calls != 1 || created.PayInfo != "qr:legacy" || created.Order.ID <= 0 {
		t.Fatalf("audit failure must not undo a successful legacy gateway order: created=%+v calls=%d err=%v", created, calls, err)
	}
	var stored entity.RechargeOrder
	if err := db.First(&stored, created.Order.ID).Error; err != nil || stored.ThirdPartyOrderNo != "trade-legacy" || stored.OrderNo != created.Order.OrderNo {
		t.Fatalf("successful gateway order must remain available for callback reconciliation: stored=%+v err=%v", stored, err)
	}
}
