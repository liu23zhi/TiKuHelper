package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestSystemSettingDetailReturnsCurrentSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := loadSystemSettingDetail
	defer func() {
		loadSystemSettingDetail = original
	}()

	loadSystemSettingDetail = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{
			RegisterEnabled:        true,
			MaxLogSizeBytes:        service.DefaultMaxLogSizeBytes,
			EPayVersion:            "v2",
			EPayGatewayURL:         "https://pay.example.com/submit.php",
			EPayPartnerID:          "10001",
			EPayPlatformPublicKey:  "platform-public-key",
			EPayMerchantPrivateKey: "merchant-private-key",
			EPayNotifyURL:          "https://app.example.com/adapter-service/payment/notify",
			EPayReturnURL:          "https://app.example.com/adapter/recharge/result",
		}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/system-settings", nil)
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	SystemSettingDetail(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var detail service.SystemSettingDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !detail.RegisterEnabled {
		t.Fatal("expected register enabled true")
	}
	if detail.MaxLogSizeBytes != service.DefaultMaxLogSizeBytes {
		t.Fatalf("expected max log size %d, got %d", service.DefaultMaxLogSizeBytes, detail.MaxLogSizeBytes)
	}
	if detail.EPayVersion != "v2" {
		t.Fatalf("expected epay version v2, got %s", detail.EPayVersion)
	}
	if detail.EPayPlatformPublicKey != "platform-public-key" {
		t.Fatalf("expected platform public key, got %s", detail.EPayPlatformPublicKey)
	}
	// 系统设置只对管理员开放，响应返回明文，便于管理员核对已配置的值。
	if detail.EPayMerchantPrivateKey != "merchant-private-key" {
		t.Fatalf("expected merchant private key in plain text, got %q", detail.EPayMerchantPrivateKey)
	}
	if detail.EPayNotifyURL != "https://app.example.com/adapter-service/payment/notify" {
		t.Fatalf("expected notify url, got %s", detail.EPayNotifyURL)
	}
}

func TestUpdateSystemSettingRejectsInvalidCountAsBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := updateSystemSettingDetail
	t.Cleanup(func() { updateSystemSettingDetail = original })
	updateSystemSettingDetail = func(input service.UpdateSystemSettingInput) (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{}, service.ErrInvalidAISearchCount
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/adapter-service/system-settings", bytes.NewBufferString(`{"maxAIWebReads":-1}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
	UpdateSystemSetting(ctx)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid count, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestSystemSettingDetailReturnsServerErrorWhenLoadFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := loadSystemSettingDetail
	defer func() {
		loadSystemSettingDetail = original
	}()

	loadSystemSettingDetail = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{}, errors.New("boom")
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/system-settings", nil)
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	SystemSettingDetail(ctx)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", recorder.Code)
	}
}

func TestPublicRegisterStatusReturnsCurrentFlags(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := loadPublicAuthSettings
	defer func() {
		loadPublicAuthSettings = original
	}()

	loadPublicAuthSettings = func() (service.PublicAuthSettings, error) {
		return service.PublicAuthSettings{
			RegisterEnabled:           true,
			SMSLoginEnabled:           true,
			WechatLoginEnabled:        true,
			WechatScanLoginEnabled:    true,
			WechatCodeLoginEnabled:    true,
			WechatDefaultLoginMode:    "code",
			WechatCodeStaticQRCodeURL: "https://wechat.example.com/static-qrcode.png",
			AlternativeLoginEnabled:   true,
			CaptchaProvider:           "remote",
			CaptchaAPIBaseURL:         "https://captcha.example.com",
			CaptchaType:               "RANDOM",
		}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/public/register-status", nil)

	PublicRegisterStatus(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		RegisterEnabled           bool   `json:"registerEnabled"`
		SMSLoginEnabled           bool   `json:"smsLoginEnabled"`
		WechatLoginEnabled        bool   `json:"wechatLoginEnabled"`
		WechatScanLoginEnabled    bool   `json:"wechatScanLoginEnabled"`
		WechatCodeLoginEnabled    bool   `json:"wechatCodeLoginEnabled"`
		WechatDefaultLoginMode    string `json:"wechatDefaultLoginMode"`
		WechatCodeStaticQRCodeURL string `json:"wechatCodeStaticQrcodeUrl"`
		AlternativeLoginEnabled   bool   `json:"alternativeLoginEnabled"`
		CaptchaProvider           string `json:"captchaProvider"`
		CaptchaAPIBaseURL         string `json:"captchaApiBaseUrl"`
		CaptchaType               string `json:"captchaType"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.RegisterEnabled || !response.SMSLoginEnabled || !response.WechatLoginEnabled || !response.AlternativeLoginEnabled {
		t.Fatalf("expected both public auth flags true, got %+v", response)
	}
	if !response.WechatScanLoginEnabled || !response.WechatCodeLoginEnabled {
		t.Fatalf("expected both wechat mode flags true, got %+v", response)
	}
	if response.WechatDefaultLoginMode != "code" {
		t.Fatalf("expected default wechat mode code, got %s", response.WechatDefaultLoginMode)
	}
	if response.WechatCodeStaticQRCodeURL != "https://wechat.example.com/static-qrcode.png" {
		t.Fatalf("unexpected static qrcode url %s", response.WechatCodeStaticQRCodeURL)
	}
	if response.CaptchaProvider != "remote" {
		t.Fatalf("expected remote captcha provider, got %s", response.CaptchaProvider)
	}
	if response.CaptchaAPIBaseURL != "https://captcha.example.com" {
		t.Fatalf("expected captcha API base url, got %s", response.CaptchaAPIBaseURL)
	}
	if response.CaptchaType != "RANDOM" {
		t.Fatalf("expected captcha type RANDOM, got %s", response.CaptchaType)
	}
}

func TestPublicRegisterStatusReturnsServerErrorWhenLoadFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := loadPublicAuthSettings
	defer func() {
		loadPublicAuthSettings = original
	}()

	loadPublicAuthSettings = func() (service.PublicAuthSettings, error) {
		return service.PublicAuthSettings{}, errors.New("boom")
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/public/register-status", nil)

	PublicRegisterStatus(ctx)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", recorder.Code)
	}
}

func TestUpdateSystemSettingRejectsInvalidPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/adapter-service/system-settings", bytes.NewBufferString("not-json"))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	UpdateSystemSetting(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestUpdateSystemSettingPersistsAndReturnsDetail(t *testing.T) {
	gin.SetMode(gin.TestMode)

	original := updateSystemSettingDetail
	defer func() {
		updateSystemSettingDetail = original
	}()

	var received service.UpdateSystemSettingInput
	updateSystemSettingDetail = func(input service.UpdateSystemSettingInput) (service.SystemSettingDetail, error) {
		received = input
		return service.SystemSettingDetail{
			RegisterEnabled:        input.RegisterEnabled,
			EPayVersion:            "v2",
			EPayGatewayURL:         input.EPayGatewayURL,
			EPayPartnerID:          input.EPayPartnerID,
			EPayPlatformPublicKey:  input.EPayPlatformPublicKey,
			EPayMerchantPrivateKey: input.EPayMerchantPrivateKey,
			EPayNotifyURL:          input.EPayNotifyURL,
			EPayReturnURL:          input.EPayReturnURL,
		}, nil
	}

	payload := []byte(`{"registerEnabled":true,"maxLogSizeBytes":2147483648,"epayVersion":"v1","epayGatewayUrl":" https://pay.example.com/submit.php ","epayPartnerId":" 10001 ","epayPlatformPublicKey":" platform-public-key ","epayMerchantPrivateKey":" merchant-private-key ","epayNotifyUrl":" https://app.example.com/adapter-service/payment/notify ","epayReturnUrl":" https://app.example.com/adapter/recharge/result "}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/adapter-service/system-settings", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})

	UpdateSystemSetting(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if !received.RegisterEnabled {
		t.Fatal("expected register enabled input true")
	}
	if received.MaxLogSizeBytes != 2147483648 {
		t.Fatalf("expected max log size input, got %d", received.MaxLogSizeBytes)
	}
	if received.EPayVersion != "v1" {
		t.Fatalf("expected raw epay version v1, got %s", received.EPayVersion)
	}
	if received.EPayGatewayURL != " https://pay.example.com/submit.php " {
		t.Fatalf("expected raw gateway url preserved, got %q", received.EPayGatewayURL)
	}
	if received.EPayPartnerID != " 10001 " {
		t.Fatalf("expected raw partner id preserved, got %q", received.EPayPartnerID)
	}
	if received.EPayPlatformPublicKey != " platform-public-key " {
		t.Fatalf("expected raw platform public key preserved, got %q", received.EPayPlatformPublicKey)
	}
	if received.EPayMerchantPrivateKey != " merchant-private-key " {
		t.Fatalf("expected raw merchant private key preserved, got %q", received.EPayMerchantPrivateKey)
	}
	if received.EPayNotifyURL != " https://app.example.com/adapter-service/payment/notify " {
		t.Fatalf("expected raw notify url preserved, got %q", received.EPayNotifyURL)
	}
	if received.EPayReturnURL != " https://app.example.com/adapter/recharge/result " {
		t.Fatalf("expected raw return url preserved, got %q", received.EPayReturnURL)
	}
	var response service.SystemSettingDetail
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// 响应不再脱敏：回显明文（此处 service 是 stub，原样回显入参）。
	if strings.TrimSpace(response.EPayMerchantPrivateKey) != "merchant-private-key" {
		t.Fatalf("expected response secret in plain text, got %q", response.EPayMerchantPrivateKey)
	}
}
