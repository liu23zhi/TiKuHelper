package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestCreateCaptchaChallengeReturnsImageData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := createCaptchaChallengeWithDB
	defer func() { createCaptchaChallengeWithDB = original }()
	createCaptchaChallengeWithDB = func(scene string, requestIP string) (service.CaptchaChallengeResult, error) {
		return service.CaptchaChallengeResult{
			ChallengeID: "challenge-1",
			ImageData:   "data:image/png;base64,QUJD",
		}, nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/public/captcha/challenge", bytes.NewBufferString(`{"scene":"login_password"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	CreateCaptchaChallenge(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
}

func TestVerifyCaptchaChallengeReturnsCaptchaToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := verifyCaptchaChallengeWithDB
	defer func() { verifyCaptchaChallengeWithDB = original }()
	verifyCaptchaChallengeWithDB = func(challengeID string, scene string, captchaCode string, requestIP string) (service.CaptchaTokenResult, error) {
		return service.CaptchaTokenResult{Token: "token-1", Scene: scene}, nil
	}

	payload, _ := json.Marshal(map[string]string{
		"challengeId": "challenge-1",
		"scene":       "login_password",
		"captchaCode": "ABC123",
	})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/public/captcha/verify", bytes.NewBuffer(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")

	VerifyCaptchaChallenge(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
}

func TestVerifyRemoteCaptchaReturnsCaptchaToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	original := verifyRemoteCaptchaWithDB
	originalSettings := loadCaptchaSettings
	defer func() {
		verifyRemoteCaptchaWithDB = original
		loadCaptchaSettings = originalSettings
	}()
	loadCaptchaSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{CaptchaAPIBaseURL: "https://captcha.example.com", CaptchaAPIKey: "secret-key"}, nil
	}
	verifyRemoteCaptchaWithDB = func(input service.RemoteCaptchaVerifyInput) (service.CaptchaTokenResult, error) {
		if input.Scene != service.CaptchaSceneLoginPassword || input.CaptchaID != "captcha-1" {
			t.Fatalf("unexpected input: %+v", input)
		}
		if input.APIKey != "secret-key" {
			t.Fatalf("expected captcha api key, got %q", input.APIKey)
		}
		return service.CaptchaTokenResult{Token: "token-1", Scene: input.Scene}, nil
	}

	payload, _ := json.Marshal(map[string]string{
		"scene":     "login_password",
		"captchaId": "captcha-1",
	})
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/public/captcha/remote-token", bytes.NewBuffer(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")

	VerifyRemoteCaptcha(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
}

func TestProxyRemoteCaptchaGenerateReturnsRemotePayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newLogControllerTestDB(t)
	originalFetch := fetchRemoteCaptchaWithDB
	originalSettings := loadCaptchaSettings
	defer func() {
		fetchRemoteCaptchaWithDB = originalFetch
		loadCaptchaSettings = originalSettings
	}()
	loadCaptchaSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{CaptchaAPIBaseURL: "https://captcha.example.com", CaptchaAPIKey: "secret-key"}, nil
	}
	fetchRemoteCaptchaWithDB = func(input service.RemoteCaptchaProxyInput) ([]byte, string, error) {
		if input.CaptchaType != "SLIDER" || input.APIKey != "secret-key" || input.DB == nil {
			t.Fatalf("unexpected input: %+v", input)
		}
		if input.RequestIP == "" {
			t.Fatalf("unexpected input: %+v", input)
		}
		return []byte(`{"code":200,"data":{"id":"captcha-1"}}`), "application/json", nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/adapter-service/public/captcha/remote-gen?type=SLIDER", nil)

	ProxyRemoteCaptchaGenerate(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != `{"code":200,"data":{"id":"captcha-1"}}` {
		t.Fatalf("unexpected response %s", recorder.Body.String())
	}
}

func TestProxyRemoteCaptchaCheckReturnsRemotePayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newLogControllerTestDB(t)
	originalCheck := checkRemoteCaptchaWithDB
	originalSettings := loadCaptchaSettings
	defer func() {
		checkRemoteCaptchaWithDB = originalCheck
		loadCaptchaSettings = originalSettings
	}()
	loadCaptchaSettings = func() (service.SystemSettingDetail, error) {
		return service.SystemSettingDetail{CaptchaAPIBaseURL: "https://captcha.example.com", CaptchaAPIKey: "secret-key"}, nil
	}
	checkRemoteCaptchaWithDB = func(input service.RemoteCaptchaProxyInput) ([]byte, string, error) {
		if input.APIKey != "secret-key" || string(input.Body) != `{"id":"captcha-1"}` || input.DB == nil {
			t.Fatalf("unexpected input: %+v body=%s", input, string(input.Body))
		}
		if input.RequestIP == "" {
			t.Fatalf("unexpected input: %+v body=%s", input, string(input.Body))
		}
		return []byte(`{"code":200,"data":{"id":"captcha-1"}}`), "application/json", nil
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/adapter-service/public/captcha/remote-check", bytes.NewBufferString(`{"id":"captcha-1"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	ProxyRemoteCaptchaCheck(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d with body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != `{"code":200,"data":{"id":"captcha-1"}}` {
		t.Fatalf("unexpected response %s", recorder.Body.String())
	}
}
