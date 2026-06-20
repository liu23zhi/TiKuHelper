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
