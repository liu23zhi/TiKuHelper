package service

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

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

func TestListRechargeOrdersWithDBScopesUsersAndIncludesUsername(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	member := seedRechargeOrderUser(t, db, 21, "user")
	other := seedRechargeOrderUser(t, db, 22, "other")
	admin := seedRechargeOrderUser(t, db, 23, "admin")
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	paidAt := now.Add(time.Minute)
	orders := []entity.RechargeOrder{
		{
			UserID:                    member.ID,
			OrderNo:                   "rcg_user_pending",
			PaymentProvider:           DefaultPaymentProvider,
			PaymentMethodNameSnapshot: "微信支付",
			Amount:                    12.5,
			Status:                    rechargeOrderStatusPending,
			CreatedAt:                 now,
		},
		{
			UserID:                    other.ID,
			OrderNo:                   "rcg_other_paid",
			PaymentProvider:           DefaultPaymentProvider,
			PaymentMethodNameSnapshot: "支付宝",
			Amount:                    20,
			Status:                    rechargeOrderStatusPaid,
			ThirdPartyOrderNo:         "trade-other",
			PaidAt:                    &paidAt,
			CreatedAt:                 now.Add(2 * time.Minute),
		},
	}
	for _, order := range orders {
		if err := db.Create(&order).Error; err != nil {
			t.Fatalf("create recharge order: %v", err)
		}
	}

	memberResult, err := ListRechargeOrdersWithDB(db, member, RechargeOrderListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list member recharge orders: %v", err)
	}
	if memberResult.Total != 1 || len(memberResult.Data) != 1 {
		t.Fatalf("expected member to see 1 order, got %+v", memberResult)
	}
	if memberResult.Data[0].OrderNo != "rcg_user_pending" || memberResult.Data[0].Username != member.Username {
		t.Fatalf("unexpected member order row: %+v", memberResult.Data[0])
	}

	adminResult, err := ListRechargeOrdersWithDB(db, admin, RechargeOrderListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list admin recharge orders: %v", err)
	}
	if adminResult.Total != 2 || len(adminResult.Data) != 2 {
		t.Fatalf("expected admin to see 2 orders, got %+v", adminResult)
	}
	if adminResult.Data[0].OrderNo != "rcg_other_paid" || adminResult.Data[0].Username != other.Username {
		t.Fatalf("expected newest other order first with username, got %+v", adminResult.Data[0])
	}
	if adminResult.Data[0].PaidAt == nil || adminResult.Data[0].ThirdPartyOrderNo != "trade-other" {
		t.Fatalf("expected paid metadata in admin row, got %+v", adminResult.Data[0])
	}
}

func TestListRechargeOrdersWithDBSearchesByKeyword(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	member := seedRechargeOrderUser(t, db, 7, "user")
	other := seedRechargeOrderUser(t, db, 8, "user")
	now := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	paidAt := now.Add(time.Minute)

	orders := []entity.RechargeOrder{
		{
			UserID: member.ID, OrderNo: "rcg_alpha_111", PaymentProvider: DefaultPaymentProvider,
			PaymentMethodNameSnapshot: "支付宝", Amount: 10, Status: rechargeOrderStatusPending, CreatedAt: now,
		},
		{
			UserID: member.ID, OrderNo: "rcg_beta_222", PaymentProvider: DefaultPaymentProvider,
			PaymentMethodNameSnapshot: "微信支付", Amount: 20, Status: rechargeOrderStatusPaid,
			ThirdPartyOrderNo: "trade-zzz-999", PaidAt: &paidAt, CreatedAt: now.Add(time.Minute),
		},
		{
			UserID: other.ID, OrderNo: "rcg_gamma_333", PaymentProvider: DefaultPaymentProvider,
			PaymentMethodNameSnapshot: "银行卡", Amount: 30, Status: rechargeOrderStatusPaid,
			ThirdPartyOrderNo: "trade-yyy", PaidAt: &paidAt, CreatedAt: now.Add(2 * time.Minute),
		},
	}
	for _, order := range orders {
		if err := db.Create(&order).Error; err != nil {
			t.Fatalf("create recharge order: %v", err)
		}
	}

	// 注意：seedRechargeOrderUser 按角色生成用户名（"recharge-user-user"），
	// member 与 other 同名，因此按用户名搜索会命中两人的订单。
	cases := []struct {
		name      string
		keyword   string
		wantTotal int64
	}{
		{name: "order no fragment", keyword: "alpha", wantTotal: 1},
		{name: "third party order no", keyword: "trade-zzz", wantTotal: 1},
		{name: "username matches both owners", keyword: "recharge-user-user", wantTotal: 2},
		{name: "payment method", keyword: "微信", wantTotal: 1},
		{name: "provider", keyword: DefaultPaymentProvider, wantTotal: 2},
		{name: "empty keyword keeps everything", keyword: "   ", wantTotal: 2},
		{name: "no match", keyword: "不存在的订单", wantTotal: 0},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := ListRechargeOrdersWithDB(db, member, RechargeOrderListQuery{Keyword: testCase.keyword}, 1, 10)
			if err != nil {
				t.Fatalf("list recharge orders: %v", err)
			}
			if result.Total != testCase.wantTotal {
				t.Fatalf("expected %d rows for %q, got total=%d rows=%d", testCase.wantTotal, testCase.keyword, result.Total, len(result.Data))
			}
			if int64(len(result.Data)) != result.Total {
				t.Fatalf("expected returned rows to match total, got total=%d rows=%d", result.Total, len(result.Data))
			}
		})
	}
}

func TestListRechargeOrdersWithDBFiltersByStatusAndProvider(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	member := seedRechargeOrderUser(t, db, 9, "user")
	now := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	paidAt := now.Add(time.Minute)

	orders := []entity.RechargeOrder{
		{UserID: member.ID, OrderNo: "rcg_p1", PaymentProvider: DefaultPaymentProvider, Amount: 10, Status: rechargeOrderStatusPending, CreatedAt: now},
		{UserID: member.ID, OrderNo: "rcg_p2", PaymentProvider: DefaultPaymentProvider, Amount: 20, Status: rechargeOrderStatusPaid, PaidAt: &paidAt, CreatedAt: now.Add(time.Minute)},
		{UserID: member.ID, OrderNo: "rcg_p3", PaymentProvider: "legacy-gateway", Amount: 30, Status: rechargeOrderStatusPaid, PaidAt: &paidAt, CreatedAt: now.Add(2 * time.Minute)},
	}
	for _, order := range orders {
		if err := db.Create(&order).Error; err != nil {
			t.Fatalf("create recharge order: %v", err)
		}
	}

	paid, err := ListRechargeOrdersWithDB(db, member, RechargeOrderListQuery{Status: rechargeOrderStatusPaid}, 1, 10)
	if err != nil {
		t.Fatalf("filter by status: %v", err)
	}
	if paid.Total != 2 {
		t.Fatalf("expected 2 paid orders, got %d", paid.Total)
	}

	provider, err := ListRechargeOrdersWithDB(db, member, RechargeOrderListQuery{PaymentProvider: "legacy-gateway"}, 1, 10)
	if err != nil {
		t.Fatalf("filter by provider: %v", err)
	}
	if provider.Total != 1 || provider.Data[0].OrderNo != "rcg_p3" {
		t.Fatalf("expected only the legacy-gateway order, got %+v", provider.Data)
	}

	// 状态与关键词可以叠加。
	combined, err := ListRechargeOrdersWithDB(db, member, RechargeOrderListQuery{
		Status:  rechargeOrderStatusPaid,
		Keyword: "rcg_p2",
	}, 1, 10)
	if err != nil {
		t.Fatalf("combined filter: %v", err)
	}
	if combined.Total != 1 || combined.Data[0].OrderNo != "rcg_p2" {
		t.Fatalf("expected combined filter to keep only rcg_p2, got %+v", combined.Data)
	}

	// 不带筛选应看到全部 3 条。
	all, err := ListRechargeOrdersWithDB(db, member, RechargeOrderListQuery{}, 1, 10)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if all.Total != 3 {
		t.Fatalf("expected 3 orders without filters, got %d", all.Total)
	}
}

// 关键词里的 % 与 _ 必须按「字面量」处理，而不是 LIKE 通配符。
//
// 注意样本选择：两个订单号都含下划线（前缀就是 rcg_），
// 因此 "_" 应命中两条；若 _ 被当成通配符，则会命中所有记录 ——
// 用「含百分号的记录」来区分这两种行为。
func TestListRechargeOrdersWithDBKeywordTreatsWildcardsLiterally(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	member := seedRechargeOrderUser(t, db, 11, "user")
	now := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)

	orders := []entity.RechargeOrder{
		{UserID: member.ID, OrderNo: "rcg_plain", PaymentProvider: DefaultPaymentProvider, Amount: 10, Status: rechargeOrderStatusPending, CreatedAt: now},
		{UserID: member.ID, OrderNo: "rcg_under_score", PaymentProvider: DefaultPaymentProvider, Amount: 20, Status: rechargeOrderStatusPending, CreatedAt: now.Add(time.Minute)},
		{UserID: member.ID, OrderNo: "rcg_pct%value", PaymentProvider: DefaultPaymentProvider, Amount: 30, Status: rechargeOrderStatusPending, CreatedAt: now.Add(2 * time.Minute)},
	}
	for _, order := range orders {
		if err := db.Create(&order).Error; err != nil {
			t.Fatalf("create recharge order: %v", err)
		}
	}

	// "%" 只应命中真的含百分号的那条，而不是全部 3 条。
	percent, err := ListRechargeOrdersWithDB(db, member, RechargeOrderListQuery{Keyword: "%"}, 1, 10)
	if err != nil {
		t.Fatalf("search with percent: %v", err)
	}
	if percent.Total != 1 || percent.Data[0].OrderNo != "rcg_pct%value" {
		t.Fatalf("expected only the percent order, got total=%d %+v", percent.Total, percent.Data)
	}

	// "_" 是字面量下划线，3 条订单号都含下划线。
	underscore, err := ListRechargeOrdersWithDB(db, member, RechargeOrderListQuery{Keyword: "_"}, 1, 10)
	if err != nil {
		t.Fatalf("search with underscore: %v", err)
	}
	if underscore.Total != 3 {
		t.Fatalf("expected all 3 order numbers to contain a literal underscore, got %d", underscore.Total)
	}

	// 更精确的子串仍能定位到具体订单。
	precise, err := ListRechargeOrdersWithDB(db, member, RechargeOrderListQuery{Keyword: "under_score"}, 1, 10)
	if err != nil {
		t.Fatalf("search with precise substring: %v", err)
	}
	if precise.Total != 1 || precise.Data[0].OrderNo != "rcg_under_score" {
		t.Fatalf("expected only rcg_under_score, got %+v", precise.Data)
	}
}

func TestSignRechargeValuesSignsUnifiedOrderParams(t *testing.T) {
	_, privateKey, verifyKey := generateRechargeRSAKeys(t)

	values := url.Values{}
	values.Set("pid", "10001")
	values.Set("method", "web")
	values.Set("device", "pc")
	values.Set("type", "alipay")
	values.Set("notify_url", "https://app.example.com/notify")
	values.Set("return_url", "https://app.example.com/return")
	values.Set("out_trade_no", "rcg_123")
	values.Set("name", "账户充值")
	values.Set("money", "12.34")
	values.Set("client_ip", "127.0.0.1")
	values.Set("timestamp", "1781970000")

	signatureText, err := signRechargeValues(values, privateKey)
	if err != nil {
		t.Fatalf("sign recharge values: %v", err)
	}

	signature, err := base64.StdEncoding.DecodeString(signatureText)
	if err != nil {
		t.Fatalf("decode sign: %v", err)
	}
	hash := sha256.Sum256([]byte(buildRechargeSignContentForTest(values)))
	if err := rsa.VerifyPKCS1v15(verifyKey, crypto.SHA256, hash[:], signature); err != nil {
		t.Fatalf("verify sign: %v", err)
	}
}

func TestRechargeSignatureSupportsShujiePEMKeys(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}))
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	publicPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))

	values := url.Values{}
	values.Set("pid", "10001")
	values.Set("method", "web")
	values.Set("device", "pc")
	values.Set("type", "alipay")
	values.Set("out_trade_no", "rcg_pem")
	values.Set("name", "账户充值")
	values.Set("money", "1.00")
	values.Set("client_ip", "127.0.0.1")

	signature, err := signRechargeValues(values, privatePEM)
	if err != nil {
		t.Fatalf("sign values with PEM private key: %v", err)
	}
	values.Set("sign", signature)
	values.Set("sign_type", rechargeOrderSignType)
	if !verifyRechargeValuesSignature(values, publicPEM) {
		t.Fatal("expected PEM public key to verify signature")
	}
}

func TestCreateRechargePaymentRequestPostsUnifiedOrderAndReturnsQRCode(t *testing.T) {
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	var captured url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pay/create" {
			t.Fatalf("expected unified order path, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if contentType := r.Header.Get("Content-Type"); !strings.Contains(contentType, "application/x-www-form-urlencoded") {
			t.Fatalf("expected form content type, got %q", contentType)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		captured = r.PostForm
		for key, expected := range map[string]string{
			"pid":          "10001",
			"method":       "web",
			"device":       "pc",
			"type":         "wxpay",
			"notify_url":   "https://app.example.com/notify",
			"return_url":   "https://app.example.com/return",
			"out_trade_no": "rcg_123",
			"name":         "账户充值",
			"money":        "12.34",
			"clientip":     "203.0.113.9",
			"sign_type":    rechargeOrderSignType,
		} {
			if got := r.PostForm.Get(key); got != expected {
				t.Fatalf("expected %s=%q, got %q in %+v", key, expected, got, r.PostForm)
			}
		}
		if got := r.PostForm.Get("client_ip"); got != "" {
			t.Fatalf("expected legacy client_ip param to be omitted, got %q in %+v", got, r.PostForm)
		}
		if strings.TrimSpace(r.PostForm.Get("timestamp")) == "" || strings.TrimSpace(r.PostForm.Get("sign")) == "" {
			t.Fatalf("expected timestamp and sign, got %+v", r.PostForm)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":      0,
			"trade_no":  "trade_20260620001",
			"pay_type":  "qrcode",
			"pay_info":  "weixin://wxpay/bizpayurl?pr=04IPMKM",
			"timestamp": "1781970000",
		})
	}))
	defer server.Close()

	result, err := createRechargePaymentRequest(RechargePaymentConfig{
		GatewayURL:         server.URL,
		PartnerID:          "10001",
		PlatformPublicKey:  publicKey,
		MerchantPrivateKey: privateKey,
		NotifyURL:          "https://app.example.com/notify",
		ReturnURL:          "https://app.example.com/return",
		PaymentMethod:      "wxpay",
		PaymentProvider:    "epay",
	}, RechargePayRequest{
		OutTradeNo: "rcg_123",
		Name:       "账户充值",
		Money:      "12.34",
		Type:       "wxpay",
		ClientIP:   "203.0.113.9",
		Device:     "pc",
	})
	if err != nil {
		t.Fatalf("create recharge payment request: %v", err)
	}
	if result.PayType != "qrcode" || result.PayInfo != "weixin://wxpay/bizpayurl?pr=04IPMKM" || result.QRCodeContent != result.PayInfo {
		t.Fatalf("unexpected qrcode result: %+v", result)
	}
	if result.ThirdPartyOrderNo != "trade_20260620001" {
		t.Fatalf("expected third party order no, got %q", result.ThirdPartyOrderNo)
	}
	if captured.Get("sign") == "" {
		t.Fatal("expected request to be signed")
	}
}

func TestCreateRechargePaymentRequestReturnsPaymentCreateErrorWithGatewayMessage(t *testing.T) {
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 1,
			"msg":  "clientip不能为空",
		})
	}))
	defer server.Close()

	_, err := createRechargePaymentRequest(RechargePaymentConfig{
		GatewayURL:         server.URL,
		PartnerID:          "10001",
		PlatformPublicKey:  publicKey,
		MerchantPrivateKey: privateKey,
		NotifyURL:          "https://app.example.com/notify",
		ReturnURL:          "https://app.example.com/return",
		PaymentMethod:      "alipay",
		PaymentProvider:    "epay",
	}, RechargePayRequest{
		OutTradeNo: "rcg_123",
		Name:       "账户充值",
		Money:      "12.34",
		Type:       "alipay",
		ClientIP:   "203.0.113.9",
		Device:     "pc",
	})
	if !errors.Is(err, ErrRechargePaymentCreateFailed) {
		t.Fatalf("expected ErrRechargePaymentCreateFailed, got %v", err)
	}
	if !strings.Contains(err.Error(), "clientip不能为空") {
		t.Fatalf("expected gateway message in error, got %q", err.Error())
	}
}

func TestCreateRechargePaymentRequestAcceptsLegacyPayURLFields(t *testing.T) {
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":     0,
			"trade_no": "trade_20260622001",
			"payurl":   "https://pay.example.com/cashier/123",
		})
	}))
	defer server.Close()

	result, err := createRechargePaymentRequest(RechargePaymentConfig{
		GatewayURL:         server.URL,
		PartnerID:          "10001",
		PlatformPublicKey:  publicKey,
		MerchantPrivateKey: privateKey,
		NotifyURL:          "https://app.example.com/notify",
		ReturnURL:          "https://app.example.com/return",
		PaymentMethod:      "alipay",
		PaymentProvider:    "epay",
	}, RechargePayRequest{
		OutTradeNo: "rcg_123",
		Name:       "账户充值",
		Money:      "12.34",
		Type:       "alipay",
		ClientIP:   "203.0.113.9",
		Device:     "pc",
	})
	if err != nil {
		t.Fatalf("create recharge payment request: %v", err)
	}
	if result.PayType != "jump" || result.PayURL != "https://pay.example.com/cashier/123" || result.PayInfo != result.PayURL {
		t.Fatalf("unexpected legacy pay url result: %+v", result)
	}
	if result.ThirdPartyOrderNo != "trade_20260622001" {
		t.Fatalf("expected third party order no, got %q", result.ThirdPartyOrderNo)
	}
}

func TestCreateRechargeOrderWithDBCreatesPendingOrderAndReturnsQRCodePayment(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 2, "user")
	method := seedRechargePaymentMethod(t, db, entity.PaymentMethod{Name: "微信支付", EPayType: "wxpay", Enabled: true, Sort: 20})
	publicKey, privateKey, _ := generateRechargeRSAKeys(t)
	seedRechargePaymentSettings(t, db, publicKey, privateKey)

	original := createRechargePaymentRequest
	defer func() {
		createRechargePaymentRequest = original
	}()

	var capturedConfig RechargePaymentConfig
	var capturedRequest RechargePayRequest
	createRechargePaymentRequest = func(config RechargePaymentConfig, request RechargePayRequest) (RechargePaymentResult, error) {
		capturedConfig = config
		capturedRequest = request
		return RechargePaymentResult{
			PayType:           "qrcode",
			PayInfo:           "weixin://wxpay/bizpayurl?pr=04IPMKM",
			QRCodeContent:     "weixin://wxpay/bizpayurl?pr=04IPMKM",
			ThirdPartyOrderNo: "trade_20260620002",
		}, nil
	}

	created, err := CreateRechargeOrderWithDB(db, user, CreateRechargeOrderInput{Amount: 12.34, PaymentMethodID: method.ID, ClientIP: "198.51.100.8", Device: "mobile"})
	if err != nil {
		t.Fatalf("create recharge order: %v", err)
	}
	if created.PayType != "qrcode" || created.QRCodeContent != "weixin://wxpay/bizpayurl?pr=04IPMKM" {
		t.Fatalf("unexpected payment result: %+v", created)
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
	// 充值金额限制为 2 位小数，12.34 原样保留。
	if created.Order.Amount != 12.34 {
		t.Fatalf("expected amount 12.34, got %v", created.Order.Amount)
	}
	if !strings.HasPrefix(created.Order.OrderNo, "rcg_rechargeuseruser_") || !strings.Contains(created.Order.OrderNo, "_1234_") {
		t.Fatalf("expected generated order no to include username and amount, got %q", created.Order.OrderNo)
	}

	if capturedConfig.PartnerID != "10001" {
		t.Fatalf("unexpected payment config: %+v", capturedConfig)
	}
	if capturedRequest.Type != "wxpay" {
		t.Fatalf("expected wxpay type, got %q", capturedRequest.Type)
	}
	if capturedRequest.ClientIP != "198.51.100.8" || capturedRequest.Device != "mobile" {
		t.Fatalf("expected client ip/device to be forwarded, got %+v", capturedRequest)
	}
	if capturedRequest.Money != "12.34" {
		t.Fatalf("expected money 12.34, got %q", capturedRequest.Money)
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
	if persisted.OrderNo != created.Order.OrderNo || persisted.Amount != 12.34 || persisted.Status != rechargeOrderStatusPending {
		t.Fatalf("unexpected persisted order: %+v", persisted)
	}
	if persisted.PaymentMethodNameSnapshot != "微信支付" {
		t.Fatalf("expected persisted payment method snapshot 微信支付, got %q", persisted.PaymentMethodNameSnapshot)
	}
	if persisted.ThirdPartyOrderNo != "trade_20260620002" {
		t.Fatalf("expected third party order no persisted, got %q", persisted.ThirdPartyOrderNo)
	}
}

func TestBuildRechargeOrderNoIncludesSafeUsernameTimeAndAmount(t *testing.T) {
	now := time.Date(2026, 6, 21, 9, 8, 7, 0, time.UTC)
	orderNo := buildRechargeOrderNo(entity.User{ID: 42, Username: " Alice_测试-007 "}, 12.345, now, "ABC12345")

	if orderNo != "rcg_Alice007_20260621090807_1235_ABC12345" {
		t.Fatalf("unexpected order no %q", orderNo)
	}
}

func TestBuildRechargeOrderNoFallsBackToUserIDWhenUsernameHasNoSafeCharacters(t *testing.T) {
	now := time.Date(2026, 6, 21, 9, 8, 7, 0, time.UTC)
	orderNo := buildRechargeOrderNo(entity.User{ID: 42, Username: "测试用户"}, 1, now, "ABC12345")

	if orderNo != "rcg_user42_20260621090807_100_ABC12345" {
		t.Fatalf("unexpected order no %q", orderNo)
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

// 充值金额只允许 2 位小数：支付渠道只支持「分」，多余精度必须被拒绝而不是静默四舍五入。
func TestCreateRechargeOrderWithDBRejectsMoreThanTwoDecimals(t *testing.T) {
	db := newRechargeOrderTestDB(t)
	user := seedRechargeOrderUser(t, db, 4, "user")
	method := seedRechargePaymentMethod(t, db, entity.PaymentMethod{Name: "微信支付", EPayType: "wxpay", Enabled: true, Sort: 20})

	for _, amount := range []float64{12.345, 0.001, 99.999} {
		if _, err := CreateRechargeOrderWithDB(db, user, CreateRechargeOrderInput{Amount: amount, PaymentMethodID: method.ID}); err != ErrRechargeAmountPrecision {
			t.Fatalf("expected ErrRechargeAmountPrecision for %v, got %v", amount, err)
		}
	}

	var count int64
	if err := db.Model(&entity.RechargeOrder{}).Count(&count).Error; err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no recharge order for over-precision amounts, got %d", count)
	}
}

// 两位小数以内（含整数）必须正常通过，不受精度校验影响。
func TestRechargeAmountAcceptsUpToTwoDecimals(t *testing.T) {
	for _, testCase := range []struct {
		amount float64
		valid  bool
	}{
		{amount: 10, valid: true},
		{amount: 25.5, valid: true},
		{amount: 25.05, valid: true},
		{amount: 0.01, valid: true},
		{amount: 199.99, valid: true},
		{amount: 0, valid: false},
		{amount: -5, valid: false},
		{amount: 10.001, valid: false},
		{amount: 10.005, valid: false},
	} {
		err := validateRechargeAmount(testCase.amount)
		if testCase.valid && err != nil {
			t.Fatalf("expected %v to be a valid recharge amount, got %v", testCase.amount, err)
		}
		if !testCase.valid && err == nil {
			t.Fatalf("expected %v to be rejected", testCase.amount)
		}
	}
}

// 钱包口径必须保持 6 位精度，绝不能被充值口径（2 位）截断。
func TestRechargeNormalizationKeepsWalletAtSixDecimals(t *testing.T) {
	if got := normalizeRechargeAmount(12.345); got != 12.35 {
		t.Fatalf("expected recharge amount to round to 2 decimals (12.35), got %v", got)
	}
	if got := normalizeWalletAmount(49.987843); got != 49.987843 {
		t.Fatalf("expected wallet amount to keep 6 decimals, got %v", got)
	}
	if got := normalizeWalletAmount(12.345); got != 12.345 {
		t.Fatalf("expected wallet amount to keep 6 decimals, got %v", got)
	}
	if got := normalizeWalletAmount(0.000062); got != 0.000062 {
		t.Fatalf("expected tiny wallet amount to survive, got %v", got)
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
