package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newCaptchaTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:captcha-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open captcha test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.CaptchaChallenge{}, &entity.CaptchaToken{}); err != nil {
		t.Fatalf("migrate captcha test db: %v", err)
	}
	return db
}

func TestCreateCaptchaChallengeWithDBCreatesPendingChallenge(t *testing.T) {
	db := newCaptchaTestDB(t)
	captchaCodeGenerator = func() string { return "AB12CD" }
	defer func() { captchaCodeGenerator = defaultCaptchaCodeGenerator }()

	challenge, err := CreateCaptchaChallengeWithDB(db, CaptchaSceneLoginPassword, "127.0.0.1")
	if err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	if strings.TrimSpace(challenge.ChallengeID) == "" {
		t.Fatal("expected challenge id")
	}
	if !strings.HasPrefix(challenge.ImageData, "data:image/png;base64,") {
		t.Fatalf("expected base64 png image, got %s", challenge.ImageData)
	}
	if len(challenge.ImageData) <= len("data:image/png;base64,") {
		t.Fatal("expected non-empty image payload")
	}
	if challenge.ExpiresAt.IsZero() {
		t.Fatal("expected expires at")
	}

	secondChallenge, err := CreateCaptchaChallengeWithDB(db, CaptchaSceneLoginPassword, "127.0.0.1")
	if err != nil {
		t.Fatalf("create second challenge: %v", err)
	}
	if challenge.ImageData == secondChallenge.ImageData {
		t.Fatal("expected captcha image layout to vary between challenges with the same code")
	}

	var stored entity.CaptchaChallenge
	if err := db.Where("challenge_id = ?", challenge.ChallengeID).First(&stored).Error; err != nil {
		t.Fatalf("load stored challenge: %v", err)
	}
	if stored.Status != CaptchaChallengeStatusPending {
		t.Fatalf("expected pending challenge, got %s", stored.Status)
	}
}

func TestVerifyCaptchaChallengeWithDBRejectsWrongAnswer(t *testing.T) {
	db := newCaptchaTestDB(t)
	captchaCodeGenerator = func() string { return "AB12CD" }
	defer func() { captchaCodeGenerator = defaultCaptchaCodeGenerator }()

	challenge, err := CreateCaptchaChallengeWithDB(db, CaptchaSceneRegisterSubmit, "127.0.0.1")
	if err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	if _, err := VerifyCaptchaChallengeWithDB(db, challenge.ChallengeID, CaptchaSceneRegisterSubmit, "WRONG", "127.0.0.1"); err == nil {
		t.Fatal("expected wrong captcha answer to fail")
	}
}

func TestVerifyCaptchaChallengeWithDBCreatesActiveToken(t *testing.T) {
	db := newCaptchaTestDB(t)
	captchaNow = func() time.Time { return time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC) }
	defer func() { captchaNow = time.Now }()
	captchaCodeGenerator = func() string { return "AB12CD" }
	defer func() { captchaCodeGenerator = defaultCaptchaCodeGenerator }()

	challenge, err := CreateCaptchaChallengeWithDB(db, CaptchaSceneSMSSendLogin, "127.0.0.1")
	if err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	token, err := VerifyCaptchaChallengeWithDB(db, challenge.ChallengeID, CaptchaSceneSMSSendLogin, "AB12CD", "127.0.0.1")
	if err != nil {
		t.Fatalf("verify challenge: %v", err)
	}
	if strings.TrimSpace(token.Token) == "" {
		t.Fatal("expected captcha token")
	}
	if token.Scene != CaptchaSceneSMSSendLogin {
		t.Fatalf("expected token scene %s, got %s", CaptchaSceneSMSSendLogin, token.Scene)
	}
}

func TestConsumeCaptchaTokenWithDBConsumesOnceAndEnforcesScene(t *testing.T) {
	db := newCaptchaTestDB(t)
	captchaCodeGenerator = func() string { return "ZX90KL" }
	defer func() { captchaCodeGenerator = defaultCaptchaCodeGenerator }()

	challenge, err := CreateCaptchaChallengeWithDB(db, CaptchaSceneLoginSMS, "127.0.0.1")
	if err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	token, err := VerifyCaptchaChallengeWithDB(db, challenge.ChallengeID, CaptchaSceneLoginSMS, "ZX90KL", "127.0.0.1")
	if err != nil {
		t.Fatalf("verify challenge: %v", err)
	}

	if err := ConsumeCaptchaTokenWithDB(db, token.Token, CaptchaSceneLoginSMS, "127.0.0.1"); err != nil {
		t.Fatalf("consume token first time: %v", err)
	}
	if err := ConsumeCaptchaTokenWithDB(db, token.Token, CaptchaSceneLoginSMS, "127.0.0.1"); err == nil {
		t.Fatal("expected consumed token reuse to fail")
	}
	if err := ConsumeCaptchaTokenWithDB(db, token.Token, CaptchaSceneRegisterSubmit, "127.0.0.1"); err == nil {
		t.Fatal("expected token scene mismatch to fail")
	}
}
