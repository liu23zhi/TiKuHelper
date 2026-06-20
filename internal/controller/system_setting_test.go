package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
	if detail.EPayVersion != "v2" {
		t.Fatalf("expected epay version v2, got %s", detail.EPayVersion)
	}
	if detail.EPayPlatformPublicKey != "platform-public-key" {
		t.Fatalf("expected platform public key, got %s", detail.EPayPlatformPublicKey)
	}
	if detail.EPayNotifyURL != "https://app.example.com/adapter-service/payment/notify" {
		t.Fatalf("expected notify url, got %s", detail.EPayNotifyURL)
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
			RegisterEnabled:          true,
			SMSLoginEnabled:          true,
			WechatLoginEnabled:       true,
			AlternativeLoginEnabled:  true,
			CaptchaProvider:          "remote",
			CaptchaAPIBaseURL:        "https://captcha.example.com",
			CaptchaType:              "RANDOM",
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
		RegisterEnabled         bool   `json:"registerEnabled"`
		SMSLoginEnabled         bool   `json:"smsLoginEnabled"`
		WechatLoginEnabled      bool   `json:"wechatLoginEnabled"`
		AlternativeLoginEnabled bool   `json:"alternativeLoginEnabled"`
		CaptchaProvider         string `json:"captchaProvider"`
		CaptchaAPIBaseURL       string `json:"captchaApiBaseUrl"`
		CaptchaType             string `json:"captchaType"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.RegisterEnabled || !response.SMSLoginEnabled || !response.WechatLoginEnabled || !response.AlternativeLoginEnabled {
		t.Fatalf("expected both public auth flags true, got %+v", response)
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

	payload := []byte(`{"registerEnabled":true,"epayVersion":"v1","epayGatewayUrl":" https://pay.example.com/submit.php ","epayPartnerId":" 10001 ","epayPlatformPublicKey":" platform-public-key ","epayMerchantPrivateKey":" merchant-private-key ","epayNotifyUrl":" https://app.example.com/adapter-service/payment/notify ","epayReturnUrl":" https://app.example.com/adapter/recharge/result "}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/adapter-service/system-settings", bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")

	UpdateSystemSetting(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if !received.RegisterEnabled {
		t.Fatal("expected register enabled input true")
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
}
