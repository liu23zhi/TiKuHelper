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

// 系统设置接口不脱敏任何密钥：全部明文返回。
//
// 背景：系统设置页只有管理员能打开（与「默认账号配置」的 token 一致），
// 返回 `********` 只会让管理员无法核对已配置的值。前端用密码框隐藏显示。
func TestRedactSystemSettingDetailReturnsAllSecretsInPlainText(t *testing.T) {
	detail := SystemSettingDetail{
		UAPIAPIKey:             "uapi-abcdef0123456789",
		CaptchaAPIKey:          "captcha-secret",
		SMSBaoSecret:           "smsbao-secret",
		WechatAPIBearerToken:   "wechat-secret",
		EPayMerchantPrivateKey: "epay-secret",
	}

	redacted := RedactSystemSettingDetail(detail)

	if redacted != detail {
		t.Fatalf("expected no field to be redacted, got %+v", redacted)
	}
	for name, got := range map[string]string{
		"uapiApiKey":             redacted.UAPIAPIKey,
		"captchaApiKey":          redacted.CaptchaAPIKey,
		"smsbaoSecret":           redacted.SMSBaoSecret,
		"wechatApiBearerToken":   redacted.WechatAPIBearerToken,
		"epayMerchantPrivateKey": redacted.EPayMerchantPrivateKey,
	} {
		if got == SecretMask {
			t.Fatalf("expected %s to be plain text, but it is masked", name)
		}
	}
}

// 回归：商户私钥与短信宝密钥必须明文返回（曾长期被脱敏成 ********）。
func TestSystemSettingSecretsAreNotMaskedAfterRoundTrip(t *testing.T) {
	db := newSystemSettingTestDB(t)

	if _, err := UpdateSystemSettingDetailWithDB(db, UpdateSystemSettingInput{
		EPayMerchantPrivateKey: "epay-private-key",
		SMSBaoSecret:           "smsbao-secret",
		CaptchaAPIKey:          "captcha-key",
		WechatAPIBearerToken:   "wechat-token",
		UAPIAPIKey:             "uapi-key",
	}); err != nil {
		t.Fatalf("save secrets: %v", err)
	}

	loaded, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	response := RedactSystemSettingDetail(loaded)

	for name, pair := range map[string][2]string{
		"epayMerchantPrivateKey": {response.EPayMerchantPrivateKey, "epay-private-key"},
		"smsbaoSecret":           {response.SMSBaoSecret, "smsbao-secret"},
		"captchaApiKey":          {response.CaptchaAPIKey, "captcha-key"},
		"wechatApiBearerToken":   {response.WechatAPIBearerToken, "wechat-token"},
		"uapiApiKey":             {response.UAPIAPIKey, "uapi-key"},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("expected %s to round-trip as plain text %q, got %q", name, pair[1], pair[0])
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
