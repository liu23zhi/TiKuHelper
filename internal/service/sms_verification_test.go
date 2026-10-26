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

func TestSMSBaoProviderNormalizesLegacy86AndUsesDomesticEndpoint(t *testing.T) {
	var domesticCalled bool
	var capturedPhone string
	var capturedPassword string
	domesticServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		domesticCalled = true
		capturedPhone = r.URL.Query().Get("m")
		capturedPassword = r.URL.Query().Get("p")
		_, _ = w.Write([]byte("0"))
	}))
	defer domesticServer.Close()
	internationalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("international endpoint must not be called for mainland phone")
		_, _ = w.Write([]byte("51"))
	}))
	defer internationalServer.Close()

	provider := SMSBaoProvider{
		DomesticEndpoint:      domesticServer.URL,
		InternationalEndpoint: internationalServer.URL,
		Username:              "demo-user",
		Secret:                "plain-password",
		CredentialType:        "password",
		RegionMode:            "auto",
		Client:                domesticServer.Client(),
	}
	if err := provider.SendSMS("+86 138-0000-0000", "验证码 123456"); err != nil {
		t.Fatalf("send mainland sms: %v", err)
	}

	sum := md5.Sum([]byte("plain-password"))
	if !domesticCalled {
		t.Fatal("expected domestic endpoint")
	}
	if capturedPhone != "13800000000" {
		t.Fatalf("expected domestic phone without +86, got %s", capturedPhone)
	}
	if capturedPassword != hex.EncodeToString(sum[:]) {
		t.Fatalf("expected md5 password credential, got %s", capturedPassword)
	}
	if err := provider.SendSMS("+14155552671", "验证码 123456"); err == nil {
		t.Fatal("expected non-mainland phone to be rejected")
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
		t.Fatal("expected domestic mode to reject non-mainland phone")
	}
}

func TestSMSBaoProviderRejectsInvalidPhonesAndConfiguredInternationalModeWithoutSending(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte("0"))
	}))
	defer server.Close()
	provider := SMSBaoProvider{DomesticEndpoint: server.URL, InternationalEndpoint: server.URL, RegionMode: "auto", Client: server.Client()}
	for _, phone := range []string{"+14155552671", "+85213800000000", "138000000000", "1380000000a", "１３８００００００００"} {
		if err := provider.SendSMS(phone, "验证码 123456"); err == nil {
			t.Fatalf("expected invalid mainland phone %q to be rejected", phone)
		}
	}
	provider.RegionMode = "international"
	if err := provider.SendSMS("+8613800000000", "验证码 123456"); err == nil {
		t.Fatal("expected international-only configuration to remain unchanged and reject mainland sends")
	}
	if calls != 0 {
		t.Fatalf("invalid/mismatched phone must not reach an SMS endpoint, got %d calls", calls)
	}
}

func TestVerifySMSCodeWithDBMatchesLegacy86PhoneAlias(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	seedSMSSettings(t, db)
	if err := db.Create(&entity.SMSVerificationCode{
		Phone: "+8613800000000", Scene: SMSSceneLogin, CodeHash: hashSMSCode("222222"),
		Status: SMSCodeStatusPending, ExpiresAt: time.Now().Add(5 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed legacy sms code: %v", err)
	}
	if err := VerifySMSCodeWithDB(db, "13800000000", SMSSceneLogin, "222222"); err != nil {
		t.Fatalf("verify legacy phone alias: %v", err)
	}
	if err := VerifySMSCodeWithDB(db, "+8613800000000", SMSSceneLogin, "222222"); err == nil {
		t.Fatal("legacy code must still be consumed only once")
	}
}

func TestSendSMSCodeWithDBMatchesAliasesForScenesAndCooldown(t *testing.T) {
	cases := []struct {
		name      string
		phone     string
		scene     string
		seedPhone string
		seedCode  bool
		wantError string
	}{
		{name: "legacy login", phone: "13800000000", scene: SMSSceneLogin, seedPhone: "+8613800000000"},
		{name: "legacy register occupation", phone: "13800000000", scene: SMSSceneRegister, seedPhone: "+8613800000000", wantError: "已被绑定"},
		{name: "legacy cooldown", phone: "13800000000", scene: SMSSceneRegister, seedCode: true, wantError: "发送过于频繁"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newSMSVerificationTestDB(t)
			seedSMSSettings(t, db)
			if tc.seedPhone != "" {
				if err := db.Create(&entity.User{Username: "legacy-user", Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public", Phone: strPtr(tc.seedPhone)}).Error; err != nil {
					t.Fatalf("create legacy user: %v", err)
				}
			}
			if tc.seedCode {
				if err := db.Create(&entity.SMSVerificationCode{Phone: "+8613800000000", Scene: tc.scene, CodeHash: hashSMSCode("123456"), Status: SMSCodeStatusPending, ExpiresAt: time.Now().Add(time.Minute)}).Error; err != nil {
					t.Fatalf("seed legacy cooldown: %v", err)
				}
			}
			calls := 0
			smsSender = func(settings SystemSettingDetail) SMSProvider {
				return stubSMSProvider(func(phone string, content string) error {
					calls++
					if phone != "13800000000" {
						t.Fatalf("provider received noncanonical phone %q", phone)
					}
					return nil
				})
			}
			t.Cleanup(func() { smsSender = newSMSProviderFromSettings })
			err := SendSMSCodeWithDB(db, SendSMSCodeInput{Phone: tc.phone, Scene: tc.scene})
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || calls != 0 {
					t.Fatalf("expected %q with no send, err=%v calls=%d", tc.wantError, err, calls)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("expected canonical send, err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestSendSMSCodeWithDBCancelsLegacyAliasBeforeCreatingCanonicalCode(t *testing.T) {
	db := newSMSVerificationTestDB(t)
	seedSMSSettings(t, db)
	old := entity.SMSVerificationCode{Phone: "+8613800000000", Scene: SMSSceneRegister, CodeHash: hashSMSCode("111111"), Status: SMSCodeStatusPending, ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now().Add(-2 * time.Minute)}
	if err := db.Create(&old).Error; err != nil {
		t.Fatalf("seed old legacy code: %v", err)
	}
	smsSender = func(settings SystemSettingDetail) SMSProvider {
		return stubSMSProvider(func(phone string, content string) error { return nil })
	}
	defer func() { smsSender = newSMSProviderFromSettings }()
	if err := SendSMSCodeWithDB(db, SendSMSCodeInput{Phone: "+86 13800000000", Scene: SMSSceneRegister}); err != nil {
		t.Fatalf("send canonical replacement code: %v", err)
	}
	var records []entity.SMSVerificationCode
	if err := db.Order("id asc").Find(&records).Error; err != nil {
		t.Fatalf("load code records: %v", err)
	}
	if len(records) != 2 || records[0].Status != SMSCodeStatusCanceled || records[1].Phone != "13800000000" || records[1].Status != SMSCodeStatusPending {
		t.Fatalf("expected canceled legacy code and canonical replacement, got %+v", records)
	}
}

func TestSendSMSCodeWithDBRejectsCrossAccountAliasConflictBeforeProvider(t *testing.T) {
	for _, scene := range []string{SMSSceneLogin, SMSSceneRegister, SMSSceneChangePhone} {
		t.Run(scene, func(t *testing.T) {
			db := newSMSVerificationTestDB(t)
			seedSMSSettings(t, db)
			for id, stored := range map[int32]string{1: "13800000000", 2: "+8613800000000"} {
				if err := db.Create(&entity.User{ID: id, Username: fmt.Sprintf("owner-%d", id), Password: "123456", Role: "user", Status: "enabled", RegisterSource: "public", Phone: strPtr(stored)}).Error; err != nil {
					t.Fatalf("create conflict owner: %v", err)
				}
			}
			calls := 0
			smsSender = func(settings SystemSettingDetail) SMSProvider {
				return stubSMSProvider(func(phone string, content string) error { calls++; return nil })
			}
			t.Cleanup(func() { smsSender = newSMSProviderFromSettings })
			err := SendSMSCodeWithDB(db, SendSMSCodeInput{Phone: "13800000000", Scene: scene, TargetUserID: 3})
			if err == nil || !strings.Contains(err.Error(), "历史绑定冲突") || calls != 0 {
				t.Fatalf("expected historical conflict with no SMS send, err=%v calls=%d", err, calls)
			}
		})
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
