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
		return service.SystemSettingDetail{CaptchaAPIBaseURL: "https://captcha.example.com"}, nil
	}
	verifyRemoteCaptchaWithDB = func(input service.RemoteCaptchaVerifyInput) (service.CaptchaTokenResult, error) {
		if input.Scene != service.CaptchaSceneLoginPassword || input.CaptchaID != "captcha-1" {
			t.Fatalf("unexpected input: %+v", input)
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
