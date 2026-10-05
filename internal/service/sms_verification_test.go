package service

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestSMSBaoProviderSelectsInternationalEndpointAndHashesPasswordCredential(t *testing.T) {
	var domesticCalled bool
	var capturedPhone string
	var capturedPassword string
	internationalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPhone = r.URL.Query().Get("m")
		capturedPassword = r.URL.Query().Get("p")
		_, _ = w.Write([]byte("0"))
	}))
	defer internationalServer.Close()
	domesticServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		domesticCalled = true
		_, _ = w.Write([]byte("0"))
	}))
	defer domesticServer.Close()

	provider := SMSBaoProvider{
		DomesticEndpoint:      domesticServer.URL,
		InternationalEndpoint: internationalServer.URL,
		Username:              "demo-user",
		Secret:                "plain-password",
		CredentialType:        "password",
		RegionMode:            "auto",
		Client:                internationalServer.Client(),
	}
	if err := provider.SendSMS("+14155552671", "验证码 123456"); err != nil {
		t.Fatalf("send international sms: %v", err)
	}

	sum := md5.Sum([]byte("plain-password"))
	if domesticCalled {
		t.Fatal("expected international endpoint only")
	}
	if capturedPhone != "+14155552671" {
		t.Fatalf("expected international phone with plus preserved, got %s", capturedPhone)
	}
	if capturedPassword != hex.EncodeToString(sum[:]) {
		t.Fatalf("expected md5 password credential, got %s", capturedPassword)
	}
}

func TestSMSBaoProviderUsesApiKeyVerbatimAndRejectsRegionMismatch(t *testing.T) {
	var capturedPassword string
	domesticServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPassword = r.URL.Query().Get("p")
		_, _ = w.Write([]byte("0"))
	}))
	defer domesticServer.Close()

	provider := SMSBaoProvider{
		DomesticEndpoint: domesticServer.URL,
		Username:         "demo-user",
		Secret:           "api-key-secret",
		CredentialType:   "apikey",
		RegionMode:       "domestic",
		Client:           domesticServer.Client(),
	}
	if err := provider.SendSMS("13800000000", "验证码 123456"); err != nil {
		t.Fatalf("send domestic sms: %v", err)
	}
	if capturedPassword != "api-key-secret" {
		t.Fatalf("expected api key credential verbatim, got %s", capturedPassword)
	}
	if err := provider.SendSMS("+14155552671", "验证码 123456"); err == nil {
		t.Fatal("expected domestic mode to reject international phone")
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

func TestSendSMSCodeWithDBAllowsChangePhoneForCurrentUser(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	seedSMSSettings(t, db)
	currentPhone := "13800000000"
	if err := db.Create(&entity.User{
		ID:             77,
		Username:       "phone-owner",
		Password:       "123456",
		Role:           "user",
		Status:         "enabled",
		RegisterSource: "public",
		Phone:          &currentPhone,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	smsSender = func(settings SystemSettingDetail) SMSProvider {
		return stubSMSProvider(func(phone string, content string) error { return nil })
	}
	defer func() { smsSender = newSMSProviderFromSettings }()

	if err := SendSMSCodeWithDB(db, SendSMSCodeInput{
		Phone:        "13900000000",
		Scene:        SMSSceneChangePhone,
		RequestIP:    "127.0.0.1",
		TargetUserID: 77,
	}); err != nil {
		t.Fatalf("send change-phone code: %v", err)
	}

	var count int64
	if err := db.Model(&entity.SMSVerificationCode{}).Where("phone = ? AND scene = ?", "13900000000", SMSSceneChangePhone).Count(&count).Error; err != nil {
		t.Fatalf("count sms codes: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one change-phone code, got %d", count)
	}
}

func TestSendSMSCodeWithDBRejectsChangePhoneOwnedByAnotherUser(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	seedSMSSettings(t, db)
	occupiedPhone := "13900000000"
	if err := db.Create(&entity.User{
		ID:             78,
		Username:       "phone-owner-other",
		Password:       "123456",
		Role:           "user",
		Status:         "enabled",
		RegisterSource: "public",
		Phone:          &occupiedPhone,
	}).Error; err != nil {
		t.Fatalf("create occupied user: %v", err)
	}

	smsSender = func(settings SystemSettingDetail) SMSProvider {
		return stubSMSProvider(func(phone string, content string) error { return nil })
	}
	defer func() { smsSender = newSMSProviderFromSettings }()

	if err := SendSMSCodeWithDB(db, SendSMSCodeInput{
		Phone:        occupiedPhone,
		Scene:        SMSSceneChangePhone,
		RequestIP:    "127.0.0.1",
		TargetUserID: 77,
	}); err == nil {
		t.Fatal("expected occupied change-phone number to fail")
	}
}

func TestSendSMSCodeWithDBRejectsChangePhoneWhenPhoneFeatureDisabled(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	_, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{
		SMSProvider:     "smsbao",
		SMSBaoUsername:  "demo-user",
		SMSBaoSecret:    "demo-secret",
		SMSLoginEnabled: false,
	})
	if err != nil {
		t.Fatalf("seed disabled phone settings: %v", err)
	}

	err = SendSMSCodeWithDB(db, SendSMSCodeInput{
		Phone:        "13900000000",
		Scene:        SMSSceneChangePhone,
		RequestIP:    "127.0.0.1",
		TargetUserID: 77,
	})
	if err == nil || !strings.Contains(err.Error(), "手机号功能未启用") {
		t.Fatalf("expected disabled phone feature error, got %v", err)
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
