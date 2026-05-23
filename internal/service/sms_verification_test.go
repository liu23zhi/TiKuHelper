package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newSMSVerificationTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:sms-verification-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.SMSVerificationCode{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func seedSMSSettings(t *testing.T, db *gorm.DB) {
	t.Helper()
	_, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{
		RegisterEnabled: true,
		SMSProvider:     "smsbao",
		SMSBaoUsername:  "demo-user",
		SMSBaoSecret:    "demo-secret",
		SMSCodeSignName: "题库助手",
		SMSCodeTemplate: "【{sign}】您的验证码是{code}，5分钟内有效。",
		SMSLoginEnabled: true,
	})
	if err != nil {
		t.Fatalf("seed sms settings: %v", err)
	}
}

type stubSMSProvider func(phone string, content string) error

func (fn stubSMSProvider) SendSMS(phone string, content string) error {
	return fn(phone, content)
}

func TestSMSBaoProviderMapsSuccessAndKnownErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0"))
	}))
	defer server.Close()

	provider := SMSBaoProvider{
		Endpoint: server.URL,
		Username: "demo-user",
		Secret:   "demo-secret",
		Client:   server.Client(),
	}
	if err := provider.SendSMS("13800000000", "【题库助手】验证码 123456"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("41"))
	}))
	defer errorServer.Close()

	provider.Endpoint = errorServer.URL
	provider.Client = errorServer.Client()
	if err := provider.SendSMS("13800000000", "【题库助手】验证码 123456"); err == nil {
		t.Fatal("expected balance-insufficient code to fail")
	}
}

func TestSendSMSCodeWithDBRejectsDuplicateRegisterPhone(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	seedSMSSettings(t, db)
	if err := db.Create(&entity.User{Username: "bound", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public", Phone: strPtr("13800000000")}).Error; err != nil {
		t.Fatalf("create bound user: %v", err)
	}

	smsSender = func(settings SystemSettingDetail) SMSProvider {
		return stubSMSProvider(func(phone string, content string) error { return nil })
	}
	defer func() { smsSender = newSMSProviderFromSettings }()

	if err := SendSMSCodeWithDB(db, SendSMSCodeInput{Phone: "13800000000", Scene: SMSSceneRegister, RequestIP: "127.0.0.1"}); err == nil {
		t.Fatal("expected duplicate register phone to fail")
	}
}

func TestSendSMSCodeWithDBRejectsUnboundLoginPhone(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	seedSMSSettings(t, db)

	smsSender = func(settings SystemSettingDetail) SMSProvider {
		return stubSMSProvider(func(phone string, content string) error { return nil })
	}
	defer func() { smsSender = newSMSProviderFromSettings }()

	if err := SendSMSCodeWithDB(db, SendSMSCodeInput{Phone: "13800000000", Scene: SMSSceneLogin, RequestIP: "127.0.0.1"}); err == nil {
		t.Fatal("expected unbound login phone to fail")
	}
}

func TestSendSMSCodeWithDBUsesSMSVerificationTableEvenWithUserScopedDB(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	seedSMSSettings(t, db)

	smsSender = func(settings SystemSettingDetail) SMSProvider {
		return stubSMSProvider(func(phone string, content string) error { return nil })
	}
	defer func() { smsSender = newSMSProviderFromSettings }()

	if err := SendSMSCodeWithDB(db.Model(&entity.User{}), SendSMSCodeInput{Phone: "13800000000", Scene: SMSSceneRegister, RequestIP: "127.0.0.1"}); err != nil {
		t.Fatalf("send sms code with user-scoped db: %v", err)
	}

	var count int64
	if err := db.Model(&entity.SMSVerificationCode{}).Count(&count).Error; err != nil {
		t.Fatalf("count sms verification codes: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 sms verification code, got %d", count)
	}
}

func TestVerifySMSCodeWithDBUsesSMSVerificationTableEvenWithUserScopedDB(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	seedSMSSettings(t, db)

	if err := db.Create(&entity.SMSVerificationCode{
		Phone:     "13800000000",
		Scene:     SMSSceneLogin,
		CodeHash:  hashSMSCode("222222"),
		Status:    SMSCodeStatusPending,
		ExpiresAt: time.Now().Add(5 * time.Minute),
		RequestIP: "127.0.0.1",
	}).Error; err != nil {
		t.Fatalf("seed pending code: %v", err)
	}

	if err := VerifySMSCodeWithDB(db.Model(&entity.User{}), "13800000000", SMSSceneLogin, "222222"); err != nil {
		t.Fatalf("verify sms code with user-scoped db: %v", err)
	}
}

func TestVerifySMSCodeWithDBUsesLatestPendingRecordAndConsumesOnce(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	seedSMSSettings(t, db)

	if err := db.Create(&entity.SMSVerificationCode{
		Phone:     "13800000000",
		Scene:     SMSSceneLogin,
		CodeHash:  hashSMSCode("111111"),
		Status:    SMSCodeStatusCanceled,
		ExpiresAt: time.Now().Add(5 * time.Minute),
		RequestIP: "127.0.0.1",
	}).Error; err != nil {
		t.Fatalf("seed canceled code: %v", err)
	}
	if err := db.Create(&entity.SMSVerificationCode{
		Phone:     "13800000000",
		Scene:     SMSSceneLogin,
		CodeHash:  hashSMSCode("222222"),
		Status:    SMSCodeStatusPending,
		ExpiresAt: time.Now().Add(5 * time.Minute),
		RequestIP: "127.0.0.1",
	}).Error; err != nil {
		t.Fatalf("seed pending code: %v", err)
	}

	if err := VerifySMSCodeWithDB(db, "13800000000", SMSSceneLogin, "222222"); err != nil {
		t.Fatalf("verify code: %v", err)
	}
	if err := VerifySMSCodeWithDB(db, "13800000000", SMSSceneLogin, "222222"); err == nil {
		t.Fatal("expected consumed code to fail on second use")
	}
}
