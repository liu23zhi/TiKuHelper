package service

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newSystemSettingTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:system-setting-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	return db
}

func TestGetRegisterEnabledWithDBDefaultsToFalse(t *testing.T) {
	db := newSystemSettingTestDB(t)

	enabled, err := GetRegisterEnabledWithDB(db)
	if err != nil {
		t.Fatalf("get register enabled: %v", err)
	}

	if enabled {
		t.Fatal("expected register enabled default to false")
	}
}

func TestSetRegisterEnabledWithDBPersistsValue(t *testing.T) {
	db := newSystemSettingTestDB(t)

	enabled, err := SetRegisterEnabledWithDB(db, true)
	if err != nil {
		t.Fatalf("set register enabled: %v", err)
	}
	if !enabled {
		t.Fatal("expected updated register enabled to be true")
	}

	stored, err := GetRegisterEnabledWithDB(db)
	if err != nil {
		t.Fatalf("reload register enabled: %v", err)
	}
	if !stored {
		t.Fatal("expected stored register enabled to be true")
	}

	var setting entity.SystemSetting
	if err := db.Where("key = ?", RegisterEnabledSettingKey).First(&setting).Error; err != nil {
		t.Fatalf("load setting row: %v", err)
	}
	if setting.Value != "true" {
		t.Fatalf("expected stored value true, got %s", setting.Value)
	}
}

func TestGetSystemSettingDetailWithDBDefaults(t *testing.T) {
	db := newSystemSettingTestDB(t)

	detail, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		t.Fatalf("get system setting detail: %v", err)
	}

	if detail.RegisterEnabled {
		t.Fatal("expected register enabled default to false")
	}
	if detail.EPayVersion != DefaultEPayVersion {
		t.Fatalf("expected default epay version %s, got %s", DefaultEPayVersion, detail.EPayVersion)
	}
}

func TestUpdateSystemSettingDetailWithDBPersistsNormalizedValues(t *testing.T) {
	db := newSystemSettingTestDB(t)

	updated, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{
		RegisterEnabled:        true,
		EPayVersion:            "v1",
		EPayGatewayURL:         " https://pay.example.com/submit.php ",
		EPayPartnerID:          " 10001 ",
		EPayPlatformPublicKey:  " platform-public-key ",
		EPayMerchantPrivateKey: " merchant-private-key ",
		EPayNotifyURL:          " https://example.com/notify ",
		EPayReturnURL:          " https://example.com/return ",
	})
	if err != nil {
		t.Fatalf("update system setting detail: %v", err)
	}

	if !updated.RegisterEnabled {
		t.Fatal("expected register enabled true")
	}
	if updated.EPayVersion != "v2" {
		t.Fatalf("expected forced epay version v2, got %s", updated.EPayVersion)
	}
	if updated.EPayGatewayURL != "https://pay.example.com/submit.php" {
		t.Fatalf("unexpected gateway url %s", updated.EPayGatewayURL)
	}
	if updated.EPayPartnerID != "10001" {
		t.Fatalf("unexpected partner id %s", updated.EPayPartnerID)
	}
	if updated.EPayPlatformPublicKey != "platform-public-key" {
		t.Fatalf("unexpected platform public key %s", updated.EPayPlatformPublicKey)
	}
	if updated.EPayMerchantPrivateKey != "merchant-private-key" {
		t.Fatalf("unexpected merchant private key %s", updated.EPayMerchantPrivateKey)
	}
	if updated.EPayNotifyURL != "https://example.com/notify" {
		t.Fatalf("unexpected notify url %s", updated.EPayNotifyURL)
	}
	if updated.EPayReturnURL != "https://example.com/return" {
		t.Fatalf("unexpected return url %s", updated.EPayReturnURL)
	}

	reloaded, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		t.Fatalf("reload system setting detail: %v", err)
	}
	if !reloaded.RegisterEnabled || reloaded.EPayVersion != "v2" {
		t.Fatalf("unexpected reloaded detail: %+v", reloaded)
	}
	if reloaded.EPayGatewayURL != "https://pay.example.com/submit.php" || reloaded.EPayPartnerID != "10001" {
		t.Fatalf("unexpected reloaded payment config: %+v", reloaded)
	}
	if reloaded.EPayNotifyURL != "https://example.com/notify" || reloaded.EPayReturnURL != "https://example.com/return" {
		t.Fatalf("unexpected reloaded callback urls: %+v", reloaded)
	}

	checks := map[string]string{
		PaymentProviderSettingKey:        "epay",
		EPayVersionSettingKey:            "v2",
		EPayGatewayURLSettingKey:         "https://pay.example.com/submit.php",
		EPayPartnerIDSettingKey:          "10001",
		EPayPlatformPublicKeySettingKey:  "platform-public-key",
		EPayMerchantPrivateKeySettingKey: "merchant-private-key",
		EPayNotifyURLSettingKey:          "https://example.com/notify",
		EPayReturnURLSettingKey:          "https://example.com/return",
	}
	for key, want := range checks {
		var setting entity.SystemSetting
		if err := db.Where("key = ?", key).First(&setting).Error; err != nil {
			t.Fatalf("load setting %s: %v", key, err)
		}
		if setting.Value != want {
			t.Fatalf("expected %s=%s, got %s", key, want, setting.Value)
		}
	}
}

func TestGetPublicAuthSettingsWithDBDefaults(t *testing.T) {
	db := newSystemSettingTestDB(t)

	settings, err := GetPublicAuthSettingsWithDB(db)
	if err != nil {
		t.Fatalf("get public auth settings: %v", err)
	}
	if settings.RegisterEnabled {
		t.Fatal("expected register enabled default false")
	}
	if settings.SMSLoginEnabled {
		t.Fatal("expected sms login enabled default false")
	}
}

func TestUpdateSystemSettingDetailWithDBPersistsSMSFields(t *testing.T) {
	db := newSystemSettingTestDB(t)

	updated, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{
		RegisterEnabled:        true,
		EPayVersion:            "v2",
		EPayGatewayURL:         "https://pay.example.com/submit.php",
		EPayPartnerID:          "10001",
		EPayPlatformPublicKey:  "platform-public-key",
		EPayMerchantPrivateKey: "merchant-private-key",
		EPayNotifyURL:          "https://example.com/notify",
		EPayReturnURL:          "https://example.com/return",
		SMSProvider:            " smsbao ",
		SMSBaoUsername:         " demo-user ",
		SMSBaoSecret:           " demo-secret ",
		SMSCodeSignName:        " 题库助手 ",
		SMSCodeTemplate:        " 【{sign}】验证码 {code} ",
		SMSLoginEnabled:        true,
	})
	if err != nil {
		t.Fatalf("update system setting detail: %v", err)
	}

	if updated.SMSProvider != "smsbao" {
		t.Fatalf("expected sms provider smsbao, got %s", updated.SMSProvider)
	}
	if updated.SMSBaoUsername != "demo-user" {
		t.Fatalf("expected smsbao username demo-user, got %s", updated.SMSBaoUsername)
	}
	if updated.SMSBaoSecret != "demo-secret" {
		t.Fatalf("expected smsbao secret demo-secret, got %s", updated.SMSBaoSecret)
	}
	if updated.SMSCodeSignName != "题库助手" {
		t.Fatalf("expected sign name 题库助手, got %s", updated.SMSCodeSignName)
	}
	if updated.SMSCodeTemplate != "【{sign}】验证码 {code}" {
		t.Fatalf("expected sms template preserved, got %s", updated.SMSCodeTemplate)
	}
	if !updated.SMSLoginEnabled {
		t.Fatal("expected sms login enabled true")
	}

	publicSettings, err := GetPublicAuthSettingsWithDB(db)
	if err != nil {
		t.Fatalf("reload public auth settings: %v", err)
	}
	if !publicSettings.RegisterEnabled || !publicSettings.SMSLoginEnabled {
		t.Fatalf("unexpected public auth settings: %+v", publicSettings)
	}
}

func TestUpdateSystemSettingDetailWithDBPersistsWechatCaptchaAndSMSBaoFields(t *testing.T) {
	db := newSystemSettingTestDB(t)

	updated, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{
		RegisterEnabled:      true,
		SMSLoginEnabled:      false,
		WechatLoginEnabled:   true,
		WechatAPIBaseURL:     " https://wechat.example.com ",
		WechatAPIBearerToken: " token-1 ",
		CaptchaProvider:      " remote ",
		CaptchaAPIBaseURL:    " https://captcha.api.zelly.cn/ ",
		CaptchaAPIKey:        " captcha-secret ",
		CaptchaType:          " 滑块验证码 ",
		SMSBaoCredentialType: " password ",
		SMSBaoRegionMode:     " international ",
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}

	if !updated.WechatLoginEnabled {
		t.Fatal("expected wechat login enabled")
	}
	if updated.WechatAPIBaseURL != "https://wechat.example.com" {
		t.Fatalf("unexpected wechat api base url %s", updated.WechatAPIBaseURL)
	}
	if updated.WechatAPIBearerToken != "token-1" {
		t.Fatalf("unexpected wechat bearer token %s", updated.WechatAPIBearerToken)
	}
	if updated.CaptchaProvider != "remote" {
		t.Fatalf("unexpected captcha provider %s", updated.CaptchaProvider)
	}
	if updated.CaptchaAPIBaseURL != "https://captcha.api.zelly.cn" {
		t.Fatalf("unexpected captcha api base url %s", updated.CaptchaAPIBaseURL)
	}
	if updated.CaptchaAPIKey != "captcha-secret" {
		t.Fatalf("unexpected captcha api key %s", updated.CaptchaAPIKey)
	}
	if updated.CaptchaType != "SLIDER" {
		t.Fatalf("unexpected captcha type %s", updated.CaptchaType)
	}
	if updated.SMSBaoCredentialType != "password" {
		t.Fatalf("unexpected smsbao credential type %s", updated.SMSBaoCredentialType)
	}
	if updated.SMSBaoRegionMode != "international" {
		t.Fatalf("unexpected smsbao region mode %s", updated.SMSBaoRegionMode)
	}
}

func TestGetPublicAuthSettingsWithDBIncludesAlternativeLoginAndCaptcha(t *testing.T) {
	db := newSystemSettingTestDB(t)

	_, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{
		RegisterEnabled:      false,
		SMSLoginEnabled:      false,
		WechatLoginEnabled:   true,
		WechatAPIBaseURL:     "https://wechat.example.com",
		WechatAPIBearerToken: "token-1",
		CaptchaProvider:      "local",
		CaptchaAPIBaseURL:    "https://captcha.example.com",
		CaptchaType:          "rotate",
	})
	if err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	settings, err := GetPublicAuthSettingsWithDB(db)
	if err != nil {
		t.Fatalf("get public auth settings: %v", err)
	}
	if settings.RegisterEnabled {
		t.Fatal("expected register disabled")
	}
	if settings.SMSLoginEnabled {
		t.Fatal("expected sms login disabled")
	}
	if !settings.WechatLoginEnabled {
		t.Fatal("expected wechat login enabled")
	}
	if !settings.AlternativeLoginEnabled {
		t.Fatal("expected alternative login enabled when wechat login is enabled")
	}
	if settings.CaptchaProvider != "local" {
		t.Fatalf("unexpected captcha provider %s", settings.CaptchaProvider)
	}
	if settings.CaptchaAPIBaseURL != "https://captcha.example.com" {
		t.Fatalf("unexpected captcha api base url %s", settings.CaptchaAPIBaseURL)
	}
	if settings.CaptchaType != "ROTATE" {
		t.Fatalf("unexpected captcha type %s", settings.CaptchaType)
	}
}
