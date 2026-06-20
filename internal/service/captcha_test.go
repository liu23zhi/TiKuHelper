package service

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	if err := db.AutoMigrate(&entity.CaptchaChallenge{}, &entity.CaptchaToken{}, &entity.Log{}); err != nil {
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

func TestVerifyRemoteCaptchaWithDBCreatesActiveToken(t *testing.T) {
	db := newCaptchaTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/check2" || r.URL.Query().Get("id") != "captcha-1" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"success":true}`))
	}))
	defer server.Close()

	token, err := VerifyRemoteCaptchaWithDB(db, RemoteCaptchaVerifyInput{
		Scene:      CaptchaSceneLoginPassword,
		CaptchaID:  "captcha-1",
		RequestIP:  "127.0.0.1",
		APIBaseURL: server.URL,
		Client:     server.Client(),
	})
	if err != nil {
		t.Fatalf("verify remote captcha: %v", err)
	}
	if strings.TrimSpace(token.Token) == "" || token.Scene != CaptchaSceneLoginPassword {
		t.Fatalf("unexpected token: %+v", token)
	}
}

func TestFetchRemoteCaptchaWithDBProxiesGenerateWithCaptchaKey(t *testing.T) {
	db := newCaptchaTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/gen" || r.URL.Query().Get("type") != "SLIDER" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("X-Captcha-Key"); got != "secret-key" {
			t.Fatalf("expected captcha key header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":{"id":"captcha-1","type":"SLIDER"}}`))
	}))
	defer server.Close()

	payload, contentType, err := FetchRemoteCaptchaWithDB(RemoteCaptchaProxyInput{
		DB:          db,
		APIBaseURL:  server.URL,
		APIKey:      "secret-key",
		CaptchaType: "SLIDER",
		RequestIP:   "10.1.2.3",
		Client:      server.Client(),
	})
	if err != nil {
		t.Fatalf("fetch remote captcha: %v", err)
	}
	if contentType != "application/json" {
		t.Fatalf("expected json content type, got %s", contentType)
	}
	if !strings.Contains(string(payload), `"captcha-1"`) {
		t.Fatalf("unexpected payload %s", string(payload))
	}

	var entry entity.Log
	if err := db.Where("module = ? AND action = ?", "captcha", captchaLogActionRemoteGenerate).First(&entry).Error; err != nil {
		t.Fatalf("load remote generate log: %v", err)
	}
	if entry.ClientIP != "10.1.2.3" {
		t.Fatalf("expected client ip snapshot, got %+v", entry)
	}
	for _, expected := range []string{"step=remote_gen", "result=success", "status=200", "type=SLIDER", "captchaId=captcha-1", "hasKey=true"} {
		if !strings.Contains(entry.Description, expected) {
			t.Fatalf("expected description to contain %q, got %q", expected, entry.Description)
		}
	}
	if strings.Contains(entry.Description+entry.OldAnswer+entry.NewAnswer, "secret-key") {
		t.Fatalf("captcha api key leaked into log: %+v", entry)
	}
}

func TestCheckRemoteCaptchaWithDBProxiesTrackWithCaptchaKey(t *testing.T) {
	db := newCaptchaTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/check" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("X-Captcha-Key"); got != "secret-key" {
			t.Fatalf("expected captcha key header, got %q", got)
		}
		if got := r.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Fatalf("expected json content type, got %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"captcha-1"`) {
			t.Fatalf("unexpected body %s", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"data":{"id":"captcha-1"}}`))
	}))
	defer server.Close()

	payload, contentType, err := CheckRemoteCaptchaWithDB(RemoteCaptchaProxyInput{
		DB:         db,
		APIBaseURL: server.URL,
		APIKey:     "secret-key",
		Body:       []byte(`{"id":"captcha-1","data":{"trackList":[]}}`),
		RequestIP:  "10.1.2.4",
		Client:     server.Client(),
	})
	if err != nil {
		t.Fatalf("check remote captcha: %v", err)
	}
	if contentType != "application/json" {
		t.Fatalf("expected json content type, got %s", contentType)
	}
	if !strings.Contains(string(payload), `"captcha-1"`) {
		t.Fatalf("unexpected payload %s", string(payload))
	}

	var entry entity.Log
	if err := db.Where("module = ? AND action = ?", "captcha", captchaLogActionRemoteCheck).First(&entry).Error; err != nil {
		t.Fatalf("load remote check log: %v", err)
	}
	for _, expected := range []string{"step=remote_check", "result=success", "status=200", "captchaId=captcha-1", "hasKey=true"} {
		if !strings.Contains(entry.Description, expected) {
			t.Fatalf("expected description to contain %q, got %q", expected, entry.Description)
		}
	}
	if strings.Contains(entry.Description+entry.OldAnswer+entry.NewAnswer, "secret-key") {
		t.Fatalf("captcha api key leaked into log: %+v", entry)
	}
}

func TestCheckRemoteCaptchaWithDBLogsFailedRemoteStatus(t *testing.T) {
	db := newCaptchaTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":403,"message":"bad track"}`))
	}))
	defer server.Close()

	_, _, err := CheckRemoteCaptchaWithDB(RemoteCaptchaProxyInput{
		DB:         db,
		APIBaseURL: server.URL,
		APIKey:     "secret-key",
		Body:       []byte(`{"id":"captcha-failed"}`),
		RequestIP:  "10.1.2.5",
		Client:     server.Client(),
	})
	if err == nil {
		t.Fatal("expected failed remote captcha check")
	}

	var entry entity.Log
	if err := db.Where("module = ? AND action = ?", "captcha", captchaLogActionRemoteCheck).First(&entry).Error; err != nil {
		t.Fatalf("load failed remote check log: %v", err)
	}
	for _, expected := range []string{"step=remote_check", "result=failed", "status=403", "remoteCode=403", "captchaId=captcha-failed"} {
		if !strings.Contains(entry.Description, expected) {
			t.Fatalf("expected description to contain %q, got %q", expected, entry.Description)
		}
	}
	if !strings.Contains(entry.NewAnswer, "bad track") {
		t.Fatalf("expected remote failure response snapshot, got %q", entry.NewAnswer)
	}
	if strings.Contains(entry.Description+entry.OldAnswer+entry.NewAnswer, "secret-key") {
		t.Fatalf("captcha api key leaked into log: %+v", entry)
	}
}

func TestVerifyRemoteCaptchaWithDBAcceptsPlainBooleanResponse(t *testing.T) {
	db := newCaptchaTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/check2" || r.URL.Query().Get("id") != "captcha-boolean" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`true`))
	}))
	defer server.Close()

	token, err := VerifyRemoteCaptchaWithDB(db, RemoteCaptchaVerifyInput{
		Scene:      CaptchaSceneLoginPassword,
		CaptchaID:  "captcha-boolean",
		RequestIP:  "127.0.0.1",
		APIBaseURL: server.URL,
		Client:     server.Client(),
	})
	if err != nil {
		t.Fatalf("verify remote captcha plain boolean: %v", err)
	}
	if strings.TrimSpace(token.Token) == "" || token.Scene != CaptchaSceneLoginPassword {
		t.Fatalf("unexpected token: %+v", token)
	}
}

func TestVerifyRemoteCaptchaWithDBSendsCaptchaKeyHeader(t *testing.T) {
	db := newCaptchaTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Captcha-Key"); got != "secret-key" {
			t.Fatalf("expected captcha key header, got %q", got)
		}
		_, _ = w.Write([]byte(`true`))
	}))
	defer server.Close()

	if _, err := VerifyRemoteCaptchaWithDB(db, RemoteCaptchaVerifyInput{
		Scene:      CaptchaSceneLoginPassword,
		CaptchaID:  "captcha-key",
		RequestIP:  "127.0.0.1",
		APIBaseURL: server.URL,
		APIKey:     "secret-key",
		Client:     server.Client(),
	}); err != nil {
		t.Fatalf("verify remote captcha with key: %v", err)
	}
}

func TestVerifyRemoteCaptchaWithDBRejectsFailedRemoteCheck(t *testing.T) {
	db := newCaptchaTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":400,"success":false}`))
	}))
	defer server.Close()

	if _, err := VerifyRemoteCaptchaWithDB(db, RemoteCaptchaVerifyInput{
		Scene:      CaptchaSceneLoginPassword,
		CaptchaID:  "captcha-1",
		RequestIP:  "127.0.0.1",
		APIBaseURL: server.URL,
		Client:     server.Client(),
	}); err == nil {
		t.Fatal("expected failed remote check to fail")
	}
}

func TestVerifyRemoteCaptchaWithDBLogsSecondVerificationFailure(t *testing.T) {
	db := newCaptchaTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/check2" || r.URL.Query().Get("id") != "captcha-second-failed" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`false`))
	}))
	defer server.Close()

	if _, err := VerifyRemoteCaptchaWithDB(db, RemoteCaptchaVerifyInput{
		Scene:      CaptchaSceneLoginSMS,
		CaptchaID:  "captcha-second-failed",
		RequestIP:  "10.1.2.6",
		APIBaseURL: server.URL,
		APIKey:     "secret-key",
		Client:     server.Client(),
	}); err == nil {
		t.Fatal("expected failed second verification")
	}

	var entry entity.Log
	if err := db.Where("module = ? AND action = ?", "captcha", captchaLogActionRemoteSecondCheck).First(&entry).Error; err != nil {
		t.Fatalf("load second verification log: %v", err)
	}
	for _, expected := range []string{"step=remote_check2", "result=failed", "status=200", "scene=login_sms", "captchaId=captcha-second-failed", "remoteValid=false", "hasKey=true"} {
		if !strings.Contains(entry.Description, expected) {
			t.Fatalf("expected description to contain %q, got %q", expected, entry.Description)
		}
	}
	if strings.Contains(entry.Description+entry.OldAnswer+entry.NewAnswer, "secret-key") {
		t.Fatalf("captcha api key leaked into log: %+v", entry)
	}
}
