package service

import (
	"testing"
)

// UApiPro 密钥必须能明文存、明文读。
//
// 系统设置页只有管理员能打开（与「默认账号配置」的 token 处理一致），
// 因此**不做脱敏**：前端用密码框隐藏显示即可。
func TestUAPIAPIKeyRoundTripsInPlainText(t *testing.T) {
	db := newSystemSettingTestDB(t)

	const apiKey = "uapi-abcdef0123456789"
	detail, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{UAPIAPIKey: apiKey})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if detail.UAPIAPIKey != apiKey {
		t.Fatalf("expected the key to be stored as-is, got %q", detail.UAPIAPIKey)
	}

	// 读接口同样返回明文（供前端回填密码框）。
	loaded, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if loaded.UAPIAPIKey != apiKey {
		t.Fatalf("expected the key to be read back as-is, got %q", loaded.UAPIAPIKey)
	}

	if got := GetUAPIAPIKeyWithDB(db); got != apiKey {
		t.Fatalf("expected GetUAPIAPIKeyWithDB to return the key, got %q", got)
	}
}

// RedactSystemSettingDetail 必须**放过** UApiPro 密钥，同时仍然脱敏其它密钥。
func TestRedactSystemSettingDetailKeepsUAPIAPIKey(t *testing.T) {
	const apiKey = "uapi-abcdef0123456789"

	redacted := RedactSystemSettingDetail(SystemSettingDetail{
		UAPIAPIKey:             apiKey,
		CaptchaAPIKey:          "captcha-secret",
		SMSBaoSecret:           "smsbao-secret",
		WechatAPIBearerToken:   "wechat-secret",
		EPayMerchantPrivateKey: "epay-secret",
	})

	if redacted.UAPIAPIKey != apiKey {
		t.Fatalf("expected the UApiPro key to stay in plain text, got %q", redacted.UAPIAPIKey)
	}
	for name, got := range map[string]string{
		"captchaApiKey":          redacted.CaptchaAPIKey,
		"smsbaoSecret":           redacted.SMSBaoSecret,
		"wechatApiBearerToken":   redacted.WechatAPIBearerToken,
		"epayMerchantPrivateKey": redacted.EPayMerchantPrivateKey,
	} {
		if got != SecretMask {
			t.Fatalf("expected %s to stay masked, got %q", name, got)
		}
	}
}

// 留空即为清空密钥：不做「空值保留原值」的特殊处理。
func TestUpdateSystemSettingClearsUAPIAPIKeyWhenBlank(t *testing.T) {
	db := newSystemSettingTestDB(t)

	if _, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{UAPIAPIKey: "uapi-to-be-cleared"}); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	if got := GetUAPIAPIKeyWithDB(db); got == "" {
		t.Fatal("expected the seeded key to be readable")
	}

	detail, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{})
	if err != nil {
		t.Fatalf("clear key: %v", err)
	}
	if detail.UAPIAPIKey != "" {
		t.Fatalf("expected a blank input to clear the key, got %q", detail.UAPIAPIKey)
	}
	if got := GetUAPIAPIKeyWithDB(db); got != "" {
		t.Fatalf("expected the cleared key to be gone, got %q", got)
	}
}

// 密钥首尾空白必须被裁掉（用户从控制台复制时常带换行）。
func TestUpdateSystemSettingTrimsUAPIAPIKey(t *testing.T) {
	db := newSystemSettingTestDB(t)

	detail, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{UAPIAPIKey: "  uapi-trim-me  "})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if detail.UAPIAPIKey != "uapi-trim-me" {
		t.Fatalf("expected the key to be trimmed, got %q", detail.UAPIAPIKey)
	}
}

// 未配置时读取必须返回空串（调用方据此跳过外呼、不消耗积分）。
func TestGetUAPIAPIKeyReturnsEmptyWhenUnset(t *testing.T) {
	db := newSystemSettingTestDB(t)

	if got := GetUAPIAPIKeyWithDB(db); got != "" {
		t.Fatalf("expected an empty key when unset, got %q", got)
	}
}
