package service

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newRechargeOrderTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:recharge-order-"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.PaymentMethod{}, &entity.RechargeOrder{}, &entity.BillingRecord{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	return db
}

func seedRechargeOrderUser(t *testing.T, db *gorm.DB, id int32, role string) entity.User {
	t.Helper()

	user := entity.User{
		ID:             id,
		Username:       "recharge-user-" + role,
		Password:       "123456",
		Role:           role,
		Status:         "enabled",
		Balance:        0,
		RegisterSource: "admin",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func seedRechargePaymentMethod(t *testing.T, db *gorm.DB, method entity.PaymentMethod) entity.PaymentMethod {
	t.Helper()

	if err := db.Create(&method).Error; err != nil {
		t.Fatalf("create payment method: %v", err)
	}
	return method
}

func seedRechargePaymentSettings(t *testing.T, db *gorm.DB, publicKey string, privateKey string) {
	t.Helper()

	_, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{
		RegisterEnabled:        true,
		EPayVersion:            "v2",
		EPayGatewayURL:         "https://pay.example.com",
		EPayPartnerID:          "10001",
		EPayPlatformPublicKey:  publicKey,
		EPayMerchantPrivateKey: privateKey,
		EPayNotifyURL:          "https://app.example.com/adapter-service/payment/notify",
		EPayReturnURL:          "https://app.example.com/adapter/recharge/result",
	})
	if err != nil {
		t.Fatalf("seed system settings: %v", err)
	}
}

func generateRechargeRSAKeys(t *testing.T) (string, string, *rsa.PublicKey) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}

	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}

	return base64.StdEncoding.EncodeToString(publicDER), base64.StdEncoding.EncodeToString(privateDER), &privateKey.PublicKey
}

func buildRechargeSignContentForTest(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key, list := range values {
		if key == "sign" || key == "sign_type" {
			continue
		}
		value := ""
		if len(list) > 0 {
			value = list[0]
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}
	return strings.Join(parts, "&")
}

func TestBuildRechargePayURLSignsRequiredParams(t *testing.T) {
	publicKey, privateKey, verifyKey := generateRechargeRSAKeys(t)

	payURL, err := buildRechargePayURL(RechargePaymentConfig{
		GatewayURL:          "https://pay.example.com",
		PartnerID:           "10001",
		PlatformPublicKey:   publicKey,
		MerchantPrivateKey:  privateKey,
		NotifyURL:           "https://app.example.com/notify",
		ReturnURL:           "https://app.example.com/return",
		PaymentMethod:       "alipay",
		PaymentProvider:     "epay",
	}, RechargePayRequest{
		OutTradeNo: "rcg_123",
		Name:       "账户充值",
		Money:      "12.34",
		Type:       "alipay",
	})
	if err != nil {
		t.Fatalf("build recharge pay url: %v", err)
	}

	parsed, err := url.Parse(payURL)
	if err != nil {
		t.Fatalf("parse pay url: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "pay.example.com" || parsed.Path != "/api/pay/submit" {
		t.Fatalf("unexpected pay url: %s", payURL)
	}

	query := parsed.Query()
	if query.Get("pid") != "10001" {
		t.Fatalf("expected pid 10001, got %q", query.Get("pid"))
	}
	if query.Get("type") != "alipay" {
		t.Fatalf("expected type alipay, got %q", query.Get("type"))
	}
	if query.Get("notify_url") != "https://app.example.com/notify" {
		t.Fatalf("unexpected notify url: %q", query.Get("notify_url"))
	}
	if query.Get("return_url") != "https://app.example.com/return" {
		t.Fatalf("unexpected return url: %q", query.Get("return_url"))
	}
	if query.Get("out_trade_no") != "rcg_123" {
		t.Fatalf("unexpected out trade no: %q", query.Get("out_trade_no"))
	}
	if query.Get("money") != "12.34" {
		t.Fatalf("unexpected money: %q", query.Get("money"))
	}
	if query.Get("sign_type") != "RSA" {
		t.Fatalf("expected RSA sign type, got %q", query.Get("sign_type"))
	}
	if strings.TrimSpace(query.Get("sign")) == "" {
		t.Fatalf("expected sign to be present")
	}
	if strings.TrimSpace(query.Get("timestamp")) == "" {
		t.Fatalf("expected timestamp to be present")
	}

	signature, err := base64.StdEncoding.DecodeString(query.Get("sign"))
	if err != nil {
		t.Fatalf("decode sign: %v", err)
	}
	hash := sha256.Sum256([]byte(buildRechargeSignContentForTest(query)))
	if err := rsa.VerifyPKCS1v15(verifyKey, crypto.SHA256, hash[:], signature); err != nil {
		t.Fatalf("verify sign: %v", err)
	}
}

func TestCreateRechargeOrderWithDBCreatesPendingOrderAndReturnsPayURL(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 2, "user")
	method := seedRechargePaymentMethod(t, db, entity.PaymentMethod{Name: "微信支付", EPayType: "wxpay", Enabled: true, Sort: 20})
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)

	original := buildRechargePayURL
	defer func() {
		buildRechargePayURL = original
	}()

	var capturedConfig RechargePaymentConfig
	var capturedRequest RechargePayRequest
	buildRechargePayURL = func(config RechargePaymentConfig, request RechargePayRequest) (string, error) {
		capturedConfig = config
		capturedRequest = request
		return "https://pay.example.com/api/pay/submit?mock=1", nil
	}

	created, err := CreateRechargeOrderWithDB(db, user, CreateRechargeOrderInput{Amount: 12.345, PaymentMethodID: method.ID})
	if err != nil {
		t.Fatalf("create recharge order: %v", err)
	}
	if created.PayURL != "https://pay.example.com/api/pay/submit?mock=1" {
		t.Fatalf("unexpected pay url: %q", created.PayURL)
	}
	if created.Order.UserID != user.ID {
		t.Fatalf("expected order user %d, got %d", user.ID, created.Order.UserID)
	}
	if created.Order.Status != rechargeOrderStatusPending {
		t.Fatalf("expected pending status, got %q", created.Order.Status)
	}
	if created.Order.PaymentProvider != DefaultPaymentProvider {
		t.Fatalf("expected payment provider %q, got %q", DefaultPaymentProvider, created.Order.PaymentProvider)
	}
	if created.Order.PaymentMethodNameSnapshot != "微信支付" {
		t.Fatalf("expected payment method snapshot 微信支付, got %q", created.Order.PaymentMethodNameSnapshot)
	}
	if created.Order.Amount != 12.35 {
		t.Fatalf("expected rounded amount 12.35, got %v", created.Order.Amount)
	}
	if !strings.HasPrefix(created.Order.OrderNo, "rcg_") {
		t.Fatalf("expected generated order no, got %q", created.Order.OrderNo)
	}

	if capturedConfig.PartnerID != "10001" {
		t.Fatalf("unexpected payment config: %+v", capturedConfig)
	}
	if capturedRequest.Type != "wxpay" {
		t.Fatalf("expected wxpay type, got %q", capturedRequest.Type)
	}
	if capturedRequest.Money != "12.35" {
		t.Fatalf("expected money 12.35, got %q", capturedRequest.Money)
	}
	if capturedRequest.OutTradeNo != created.Order.OrderNo {
		t.Fatalf("expected request order no %q, got %q", created.Order.OrderNo, capturedRequest.OutTradeNo)
	}
	if capturedRequest.Name != "账户充值" {
		t.Fatalf("expected recharge name, got %q", capturedRequest.Name)
	}

	var persisted entity.RechargeOrder
	if err := db.Where("id = ?", created.Order.ID).First(&persisted).Error; err != nil {
		t.Fatalf("load persisted order: %v", err)
	}
	if persisted.OrderNo != created.Order.OrderNo || persisted.Amount != 12.35 || persisted.Status != rechargeOrderStatusPending {
		t.Fatalf("unexpected persisted order: %+v", persisted)
	}
	if persisted.PaymentMethodNameSnapshot != "微信支付" {
		t.Fatalf("expected persisted payment method snapshot 微信支付, got %q", persisted.PaymentMethodNameSnapshot)
	}
}

func TestCreateRechargeOrderWithDBRejectsInvalidAmount(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 3, "user")

	_, err := CreateRechargeOrderWithDB(db, user, CreateRechargeOrderInput{Amount: 0, PaymentMethodID: 1})
	if err != ErrRechargeAmountInvalid {
		t.Fatalf("expected ErrRechargeAmountInvalid, got %v", err)
	}

	var count int64
	if err := db.Model(&entity.RechargeOrder{}).Count(&count).Error; err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no recharge order, got %d", count)
	}
}

func TestCreateRechargeOrderWithDBRejectsDisabledPaymentMethod(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 5, "user")
	method := seedRechargePaymentMethod(t, db, entity.PaymentMethod{Name: "银行卡", EPayType: "bank", Enabled: false, Sort: 30})
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)

	_, err := CreateRechargeOrderWithDB(db, user, CreateRechargeOrderInput{Amount: 20, PaymentMethodID: method.ID})
	if err != ErrRechargePaymentMethodDisabled {
		t.Fatalf("expected ErrRechargePaymentMethodDisabled, got %v", err)
	}

	var count int64
	if err := db.Model(&entity.RechargeOrder{}).Count(&count).Error; err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no recharge order, got %d", count)
	}
}

func TestCreateRechargeOrderWithDBRejectsUnknownPaymentMethod(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 6, "user")
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)

	_, err := CreateRechargeOrderWithDB(db, user, CreateRechargeOrderInput{Amount: 20, PaymentMethodID: 999})
	if err != ErrRechargePaymentMethodNotFound {
		t.Fatalf("expected ErrRechargePaymentMethodNotFound, got %v", err)
	}
}

func TestHandleRechargePaymentNotifyWithDBSettlesPendingOrderOnce(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 7, "user")
	if err := db.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", 5.25).Error; err != nil {
		t.Fatalf("seed user balance: %v", err)
	}
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)

	order := entity.RechargeOrder{
		UserID:                    user.ID,
		OrderNo:                   "rcg_notify_success",
		PaymentProvider:           DefaultPaymentProvider,
		PaymentMethodNameSnapshot: "支付宝",
		Amount:                    12.35,
		Status:                    rechargeOrderStatusPending,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create recharge order: %v", err)
	}

	values := url.Values{}
	values.Set("pid", "10001")
	values.Set("type", "alipay")
	values.Set("out_trade_no", order.OrderNo)
	values.Set("trade_no", "trade_20260506001")
	values.Set("trade_status", "TRADE_SUCCESS")
	values.Set("money", "12.35")
	signature, err := signRechargeValues(values, privateKey)
	if err != nil {
		t.Fatalf("sign notify values: %v", err)
	}
	values.Set("sign", signature)
	values.Set("sign_type", rechargeOrderSignType)

	acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values)
	if err != nil {
		t.Fatalf("handle recharge notify: %v", err)
	}
	if !acknowledged {
		t.Fatal("expected notify to be acknowledged")
	}

	var settled entity.RechargeOrder
	if err := db.Where("id = ?", order.ID).First(&settled).Error; err != nil {
		t.Fatalf("load settled order: %v", err)
	}
	if settled.Status != rechargeOrderStatusPaid {
		t.Fatalf("expected paid status, got %q", settled.Status)
	}
	if settled.ThirdPartyOrderNo != "trade_20260506001" {
		t.Fatalf("expected third party order no to persist, got %q", settled.ThirdPartyOrderNo)
	}
	if settled.PaidAt == nil {
		t.Fatal("expected paid at to be set")
	}
	if !strings.Contains(settled.CallbackPayload, "trade_status=TRADE_SUCCESS") {
		t.Fatalf("expected callback payload to include trade status, got %q", settled.CallbackPayload)
	}

	var updatedUser entity.User
	if err := db.Where("id = ?", user.ID).First(&updatedUser).Error; err != nil {
		t.Fatalf("load updated user: %v", err)
	}
	if updatedUser.Balance != 17.6 {
		t.Fatalf("expected updated balance 17.6, got %v", updatedUser.Balance)
	}

	var records []entity.BillingRecord
	if err := db.Where("related_order_id = ?", order.ID).Find(&records).Error; err != nil {
		t.Fatalf("load billing records: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 billing record, got %d", len(records))
	}
	if records[0].Amount != 12.35 {
		t.Fatalf("expected billing amount 12.35, got %v", records[0].Amount)
	}
	if records[0].BalanceBefore != 5.25 || records[0].BalanceAfter != 17.6 {
		t.Fatalf("unexpected balance snapshot: %+v", records[0])
	}
	if records[0].Provider != DefaultPaymentProvider {
		t.Fatalf("expected billing provider %q, got %q", DefaultPaymentProvider, records[0].Provider)
	}
	if records[0].Type != rechargeBillingRecordType {
		t.Fatalf("expected billing type %q, got %q", rechargeBillingRecordType, records[0].Type)
	}

	acknowledged, err = HandleRechargePaymentNotifyWithDB(db, values)
	if err != nil {
		t.Fatalf("handle duplicate recharge notify: %v", err)
	}
	if !acknowledged {
		t.Fatal("expected duplicate notify to be acknowledged")
	}

	var count int64
	if err := db.Model(&entity.BillingRecord{}).Where("related_order_id = ?", order.ID).Count(&count).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected duplicate notify to keep 1 billing record, got %d", count)
	}
	if err := db.Where("id = ?", user.ID).First(&updatedUser).Error; err != nil {
		t.Fatalf("reload updated user: %v", err)
	}
	if updatedUser.Balance != 17.6 {
		t.Fatalf("expected balance to stay 17.6 after duplicate notify, got %v", updatedUser.Balance)
	}
}

func TestHandleRechargePaymentNotifyWithDBRejectsInvalidSignature(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 8, "user")
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)

	order := entity.RechargeOrder{
		UserID:                    user.ID,
		OrderNo:                   "rcg_notify_invalid_sign",
		PaymentProvider:           DefaultPaymentProvider,
		PaymentMethodNameSnapshot: "微信支付",
		Amount:                    8.88,
		Status:                    rechargeOrderStatusPending,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create recharge order: %v", err)
	}

	otherPublicKey, otherPrivateKey, _ := generateRechargeRSAKeys(t)
	_ = otherPublicKey
	values := url.Values{}
	values.Set("pid", "10001")
	values.Set("type", "wxpay")
	values.Set("out_trade_no", order.OrderNo)
	values.Set("trade_no", "trade_invalid_sign")
	values.Set("trade_status", "TRADE_SUCCESS")
	values.Set("money", "8.88")
	signature, err := signRechargeValues(values, otherPrivateKey)
	if err != nil {
		t.Fatalf("sign notify values with wrong key: %v", err)
	}
	values.Set("sign", signature)
	values.Set("sign_type", rechargeOrderSignType)

	acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values)
	if err == nil {
		t.Fatal("expected invalid signature error")
	}
	if acknowledged {
		t.Fatal("expected invalid signature notify not to be acknowledged")
	}

	var persisted entity.RechargeOrder
	if err := db.Where("id = ?", order.ID).First(&persisted).Error; err != nil {
		t.Fatalf("load persisted order: %v", err)
	}
	if persisted.Status != rechargeOrderStatusPending {
		t.Fatalf("expected order to stay pending, got %q", persisted.Status)
	}
	if persisted.PaidAt != nil || strings.TrimSpace(persisted.ThirdPartyOrderNo) != "" {
		t.Fatalf("expected order not to be settled, got %+v", persisted)
	}

	var count int64
	if err := db.Model(&entity.BillingRecord{}).Count(&count).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no billing record, got %d", count)
	}
}

func TestHandleRechargePaymentNotifyWithDBAcknowledgesNonSuccessStatusWithoutSettlement(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 9, "user")
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)

	order := entity.RechargeOrder{
		UserID:                    user.ID,
		OrderNo:                   "rcg_notify_wait_buy_pay",
		PaymentProvider:           DefaultPaymentProvider,
		PaymentMethodNameSnapshot: "支付宝",
		Amount:                    18.6,
		Status:                    rechargeOrderStatusPending,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create recharge order: %v", err)
	}

	values := url.Values{}
	values.Set("pid", "10001")
	values.Set("type", "alipay")
	values.Set("out_trade_no", order.OrderNo)
	values.Set("trade_no", "trade_wait_buy_pay")
	values.Set("trade_status", "WAIT_BUYER_PAY")
	values.Set("money", "18.60")
	signature, err := signRechargeValues(values, privateKey)
	if err != nil {
		t.Fatalf("sign notify values: %v", err)
	}
	values.Set("sign", signature)
	values.Set("sign_type", rechargeOrderSignType)

	acknowledged, err := HandleRechargePaymentNotifyWithDB(db, values)
	if err != nil {
		t.Fatalf("handle recharge notify: %v", err)
	}
	if !acknowledged {
		t.Fatal("expected verified notify to be acknowledged")
	}

	var persisted entity.RechargeOrder
	if err := db.Where("id = ?", order.ID).First(&persisted).Error; err != nil {
		t.Fatalf("load persisted order: %v", err)
	}
	if persisted.Status != rechargeOrderStatusPending {
		t.Fatalf("expected pending status to remain, got %q", persisted.Status)
	}

	var count int64
	if err := db.Model(&entity.BillingRecord{}).Count(&count).Error; err != nil {
		t.Fatalf("count billing records: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no billing record for non-success notify, got %d", count)
	}
}
