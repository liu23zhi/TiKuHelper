package service

import (
	"errors"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const RegisterEnabledSettingKey = "register_enabled"
const PaymentProviderSettingKey = "payment_provider"
const PaymentMethodSettingKey = "payment_method"
const EPayVersionSettingKey = "epay_version"
const EPayGatewayURLSettingKey = "epay_gateway_url"
const EPayPartnerIDSettingKey = "epay_partner_id"
const EPayPlatformPublicKeySettingKey = "epay_platform_public_key"
const EPayMerchantPrivateKeySettingKey = "epay_merchant_private_key"
const EPayNotifyURLSettingKey = "epay_notify_url"
const EPayReturnURLSettingKey = "epay_return_url"
const SMSProviderSettingKey = "sms_provider"
const SMSBaoUsernameSettingKey = "smsbao_username"
const SMSBaoSecretSettingKey = "smsbao_secret"
const SMSCodeSignNameSettingKey = "sms_code_sign_name"
const SMSCodeTemplateSettingKey = "sms_code_template"
const SMSLoginEnabledSettingKey = "sms_login_enabled"
const WechatLoginEnabledSettingKey = "wechat_login_enabled"
const WechatAPIBaseURLSettingKey = "wechat_api_base_url"
const WechatAPIBearerTokenSettingKey = "wechat_api_bearer_token"
const CaptchaProviderSettingKey = "captcha_provider"
const CaptchaAPIBaseURLSettingKey = "captcha_api_base_url"
const CaptchaAPIKeySettingKey = "captcha_api_key"
const CaptchaTypeSettingKey = "captcha_type"
const SMSBaoCredentialTypeSettingKey = "smsbao_credential_type"
const SMSBaoRegionModeSettingKey = "smsbao_region_mode"

const DefaultPaymentProvider = "epay"
const DefaultPaymentMethod = "alipay"
const DefaultEPayVersion = "v2"
const DefaultSMSProvider = "smsbao"
const DefaultSMSCodeTemplate = "【{sign}】您的验证码是{code}，5分钟内有效。"
const DefaultCaptchaProvider = "remote"
const DefaultCaptchaAPIBaseURL = "https://captcha.api.zelly.cn"
const DefaultCaptchaAPIKey = "***REMOVED***"
const DefaultCaptchaType = "RANDOM"
const DefaultSMSBaoCredentialType = "apikey"
const DefaultSMSBaoRegionMode = "auto"

const paymentMethodWXPay = "wxpay"

type SystemSettingDetail struct {
	RegisterEnabled         bool   `json:"registerEnabled"`
	AlternativeLoginEnabled bool   `json:"alternativeLoginEnabled"`
	EPayVersion             string `json:"epayVersion"`
	EPayGatewayURL          string `json:"epayGatewayUrl"`
	EPayPartnerID           string `json:"epayPartnerId"`
	EPayPlatformPublicKey   string `json:"epayPlatformPublicKey"`
	EPayMerchantPrivateKey  string `json:"epayMerchantPrivateKey"`
	EPayNotifyURL           string `json:"epayNotifyUrl"`
	EPayReturnURL           string `json:"epayReturnUrl"`
	SMSProvider             string `json:"smsProvider"`
	SMSBaoUsername          string `json:"smsbaoUsername"`
	SMSBaoSecret            string `json:"smsbaoSecret"`
	SMSCodeSignName         string `json:"smsCodeSignName"`
	SMSCodeTemplate         string `json:"smsCodeTemplate"`
	SMSLoginEnabled         bool   `json:"smsLoginEnabled"`
	WechatLoginEnabled      bool   `json:"wechatLoginEnabled"`
	WechatAPIBaseURL        string `json:"wechatApiBaseUrl"`
	WechatAPIBearerToken    string `json:"wechatApiBearerToken"`
	CaptchaProvider         string `json:"captchaProvider"`
	CaptchaAPIBaseURL       string `json:"captchaApiBaseUrl"`
	CaptchaAPIKey           string `json:"captchaApiKey"`
	CaptchaType             string `json:"captchaType"`
	SMSBaoCredentialType    string `json:"smsbaoCredentialType"`
	SMSBaoRegionMode        string `json:"smsbaoRegionMode"`
}

type UpdateSystemSettingInput struct {
	RegisterEnabled        bool   `json:"registerEnabled"`
	EPayVersion            string `json:"epayVersion"`
	EPayGatewayURL         string `json:"epayGatewayUrl"`
	EPayPartnerID          string `json:"epayPartnerId"`
	EPayPlatformPublicKey  string `json:"epayPlatformPublicKey"`
	EPayMerchantPrivateKey string `json:"epayMerchantPrivateKey"`
	EPayNotifyURL          string `json:"epayNotifyUrl"`
	EPayReturnURL          string `json:"epayReturnUrl"`
	SMSProvider            string `json:"smsProvider"`
	SMSBaoUsername         string `json:"smsbaoUsername"`
	SMSBaoSecret           string `json:"smsbaoSecret"`
	SMSCodeSignName        string `json:"smsCodeSignName"`
	SMSCodeTemplate        string `json:"smsCodeTemplate"`
	SMSLoginEnabled        bool   `json:"smsLoginEnabled"`
	WechatLoginEnabled     bool   `json:"wechatLoginEnabled"`
	WechatAPIBaseURL       string `json:"wechatApiBaseUrl"`
	WechatAPIBearerToken   string `json:"wechatApiBearerToken"`
	CaptchaProvider        string `json:"captchaProvider"`
	CaptchaAPIBaseURL      string `json:"captchaApiBaseUrl"`
	CaptchaAPIKey          string `json:"captchaApiKey"`
	CaptchaType            string `json:"captchaType"`
	SMSBaoCredentialType   string `json:"smsbaoCredentialType"`
	SMSBaoRegionMode       string `json:"smsbaoRegionMode"`
}

type PublicAuthSettings struct {
	RegisterEnabled         bool   `json:"registerEnabled"`
	SMSLoginEnabled         bool   `json:"smsLoginEnabled"`
	WechatLoginEnabled      bool   `json:"wechatLoginEnabled"`
	AlternativeLoginEnabled bool   `json:"alternativeLoginEnabled"`
	CaptchaProvider         string `json:"captchaProvider"`
	CaptchaAPIBaseURL       string `json:"captchaApiBaseUrl"`
	CaptchaType             string `json:"captchaType"`
}

func GetRegisterEnabledWithDB(db *gorm.DB) (bool, error) {
	setting, exists, err := getSystemSettingByKey(db, RegisterEnabledSettingKey)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(setting.Value), "true"), nil
}

func SetRegisterEnabledWithDB(db *gorm.DB, enabled bool) (bool, error) {
	value := "false"
	if enabled {
		value = "true"
	}

	setting, _, err := getSystemSettingByKey(db, RegisterEnabledSettingKey)
	if err != nil {
		return false, err
	}
	if setting.Key == "" {
		setting.Key = RegisterEnabledSettingKey
		setting.Remark = "是否允许公开注册"
	}
	setting.Value = value

	freshDB := db.Session(&gorm.Session{NewDB: true})
	if err := freshDB.Save(&setting).Error; err != nil {
		return false, err
	}
	return enabled, nil
}

func GetPublicAuthSettingsWithDB(db *gorm.DB) (PublicAuthSettings, error) {
	registerEnabled, err := GetRegisterEnabledWithDB(db)
	if err != nil {
		return PublicAuthSettings{}, err
	}
	smsLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, SMSLoginEnabledSettingKey, "false")), "true")
	wechatLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatLoginEnabledSettingKey, "false")), "true")
	wechatAPIBaseURL := trimTrailingSlash(getSystemSettingStringWithDefault(db, WechatAPIBaseURLSettingKey, ""))
	wechatAPIBearerToken := strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatAPIBearerTokenSettingKey, ""))
	publicWechatLoginEnabled := wechatLoginEnabled && wechatAPIBaseURL != "" && wechatAPIBearerToken != ""
	return PublicAuthSettings{
		RegisterEnabled:         registerEnabled,
		SMSLoginEnabled:         smsLoginEnabled,
		WechatLoginEnabled:      publicWechatLoginEnabled,
		AlternativeLoginEnabled: smsLoginEnabled || publicWechatLoginEnabled,
		CaptchaProvider:         normalizeCaptchaProvider(getSystemSettingStringWithDefault(db, CaptchaProviderSettingKey, DefaultCaptchaProvider)),
		CaptchaAPIBaseURL:       trimTrailingSlash(getSystemSettingStringWithDefault(db, CaptchaAPIBaseURLSettingKey, DefaultCaptchaAPIBaseURL)),
		CaptchaType:             normalizeCaptchaType(getSystemSettingStringWithDefault(db, CaptchaTypeSettingKey, DefaultCaptchaType)),
	}, nil
}

func GetSystemSettingDetailWithDB(db *gorm.DB) (SystemSettingDetail, error) {
	registerEnabled, err := GetRegisterEnabledWithDB(db)
	if err != nil {
		return SystemSettingDetail{}, err
	}

	smsLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, SMSLoginEnabledSettingKey, "false")), "true")
	wechatLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatLoginEnabledSettingKey, "false")), "true")
	detail := SystemSettingDetail{
		RegisterEnabled:         registerEnabled,
		AlternativeLoginEnabled: smsLoginEnabled || wechatLoginEnabled,
		EPayVersion:             DefaultEPayVersion,
		EPayGatewayURL:          getSystemSettingStringWithDefault(db, EPayGatewayURLSettingKey, ""),
		EPayPartnerID:           getSystemSettingStringWithDefault(db, EPayPartnerIDSettingKey, ""),
		EPayPlatformPublicKey:   getSystemSettingStringWithDefault(db, EPayPlatformPublicKeySettingKey, ""),
		EPayMerchantPrivateKey:  getSystemSettingStringWithDefault(db, EPayMerchantPrivateKeySettingKey, ""),
		EPayNotifyURL:           getSystemSettingStringWithDefault(db, EPayNotifyURLSettingKey, ""),
		EPayReturnURL:           getSystemSettingStringWithDefault(db, EPayReturnURLSettingKey, ""),
		SMSProvider:             getSystemSettingStringWithDefault(db, SMSProviderSettingKey, DefaultSMSProvider),
		SMSBaoUsername:          getSystemSettingStringWithDefault(db, SMSBaoUsernameSettingKey, ""),
		SMSBaoSecret:            getSystemSettingStringWithDefault(db, SMSBaoSecretSettingKey, ""),
		SMSCodeSignName:         getSystemSettingStringWithDefault(db, SMSCodeSignNameSettingKey, ""),
		SMSCodeTemplate:         getSystemSettingStringWithDefault(db, SMSCodeTemplateSettingKey, DefaultSMSCodeTemplate),
		SMSLoginEnabled:         smsLoginEnabled,
		WechatLoginEnabled:      wechatLoginEnabled,
		WechatAPIBaseURL:        trimTrailingSlash(getSystemSettingStringWithDefault(db, WechatAPIBaseURLSettingKey, "")),
		WechatAPIBearerToken:    strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatAPIBearerTokenSettingKey, "")),
		CaptchaProvider:         normalizeCaptchaProvider(getSystemSettingStringWithDefault(db, CaptchaProviderSettingKey, DefaultCaptchaProvider)),
		CaptchaAPIBaseURL:       trimTrailingSlash(getSystemSettingStringWithDefault(db, CaptchaAPIBaseURLSettingKey, DefaultCaptchaAPIBaseURL)),
		CaptchaAPIKey:           strings.TrimSpace(getSystemSettingStringWithDefault(db, CaptchaAPIKeySettingKey, DefaultCaptchaAPIKey)),
		CaptchaType:             normalizeCaptchaType(getSystemSettingStringWithDefault(db, CaptchaTypeSettingKey, DefaultCaptchaType)),
		SMSBaoCredentialType:    normalizeSMSBaoCredentialType(getSystemSettingStringWithDefault(db, SMSBaoCredentialTypeSettingKey, DefaultSMSBaoCredentialType)),
		SMSBaoRegionMode:        normalizeSMSBaoRegionMode(getSystemSettingStringWithDefault(db, SMSBaoRegionModeSettingKey, DefaultSMSBaoRegionMode)),
	}
	return detail, nil
}

func UpdateSystemSettingDetailWithDB(db *gorm.DB, input UpdateSystemSettingInput) (SystemSettingDetail, error) {
	if _, err := SetRegisterEnabledWithDB(db, input.RegisterEnabled); err != nil {
		return SystemSettingDetail{}, err
	}

	normalized := SystemSettingDetail{
		RegisterEnabled:        input.RegisterEnabled,
		EPayVersion:            normalizeEPayVersion(input.EPayVersion),
		EPayGatewayURL:         strings.TrimSpace(input.EPayGatewayURL),
		EPayPartnerID:          strings.TrimSpace(input.EPayPartnerID),
		EPayPlatformPublicKey:  strings.TrimSpace(input.EPayPlatformPublicKey),
		EPayMerchantPrivateKey: strings.TrimSpace(input.EPayMerchantPrivateKey),
		EPayNotifyURL:          strings.TrimSpace(input.EPayNotifyURL),
		EPayReturnURL:          strings.TrimSpace(input.EPayReturnURL),
		SMSProvider:            strings.TrimSpace(input.SMSProvider),
		SMSBaoUsername:         strings.TrimSpace(input.SMSBaoUsername),
		SMSBaoSecret:           strings.TrimSpace(input.SMSBaoSecret),
		SMSCodeSignName:        strings.TrimSpace(input.SMSCodeSignName),
		SMSCodeTemplate:        strings.TrimSpace(input.SMSCodeTemplate),
		SMSLoginEnabled:        input.SMSLoginEnabled,
		WechatLoginEnabled:     input.WechatLoginEnabled,
		WechatAPIBaseURL:       trimTrailingSlash(input.WechatAPIBaseURL),
		WechatAPIBearerToken:   strings.TrimSpace(input.WechatAPIBearerToken),
		CaptchaProvider:        normalizeCaptchaProvider(input.CaptchaProvider),
		CaptchaAPIBaseURL:      trimTrailingSlash(defaultString(input.CaptchaAPIBaseURL, DefaultCaptchaAPIBaseURL)),
		CaptchaAPIKey:          strings.TrimSpace(defaultString(input.CaptchaAPIKey, DefaultCaptchaAPIKey)),
		CaptchaType:            normalizeCaptchaType(input.CaptchaType),
		SMSBaoCredentialType:   normalizeSMSBaoCredentialType(input.SMSBaoCredentialType),
		SMSBaoRegionMode:       normalizeSMSBaoRegionMode(input.SMSBaoRegionMode),
	}
	normalized.AlternativeLoginEnabled = normalized.SMSLoginEnabled || normalized.WechatLoginEnabled

	settings := []struct {
		key    string
		value  string
		remark string
	}{
		{PaymentProviderSettingKey, normalizePaymentProvider(DefaultPaymentProvider), "支付提供方"},
		{EPayVersionSettingKey, normalized.EPayVersion, "易支付接口版本"},
		{EPayGatewayURLSettingKey, normalized.EPayGatewayURL, "易支付网关地址"},
		{EPayPartnerIDSettingKey, normalized.EPayPartnerID, "易支付商户编号"},
		{EPayPlatformPublicKeySettingKey, normalized.EPayPlatformPublicKey, "易支付平台公钥"},
		{EPayMerchantPrivateKeySettingKey, normalized.EPayMerchantPrivateKey, "易支付商户私钥"},
		{EPayNotifyURLSettingKey, normalized.EPayNotifyURL, "易支付异步通知地址"},
		{EPayReturnURLSettingKey, normalized.EPayReturnURL, "易支付同步跳转地址"},
		{SMSProviderSettingKey, normalized.SMSProvider, "短信提供方"},
		{SMSBaoUsernameSettingKey, normalized.SMSBaoUsername, "短信宝用户名"},
		{SMSBaoSecretSettingKey, normalized.SMSBaoSecret, "短信宝密钥"},
		{SMSCodeSignNameSettingKey, normalized.SMSCodeSignName, "短信签名"},
		{SMSCodeTemplateSettingKey, normalized.SMSCodeTemplate, "短信验证码模板"},
		{SMSLoginEnabledSettingKey, map[bool]string{true: "true", false: "false"}[normalized.SMSLoginEnabled], "是否开启短信验证码登录"},
		{WechatLoginEnabledSettingKey, map[bool]string{true: "true", false: "false"}[normalized.WechatLoginEnabled], "是否开启微信扫码登录"},
		{WechatAPIBaseURLSettingKey, normalized.WechatAPIBaseURL, "微信登录服务地址"},
		{WechatAPIBearerTokenSettingKey, normalized.WechatAPIBearerToken, "微信登录服务 Bearer Token"},
		{CaptchaProviderSettingKey, normalized.CaptchaProvider, "验证码提供方"},
		{CaptchaAPIBaseURLSettingKey, normalized.CaptchaAPIBaseURL, "远程验证码服务地址"},
		{CaptchaAPIKeySettingKey, normalized.CaptchaAPIKey, "远程验证码 X-Captcha-Key"},
		{CaptchaTypeSettingKey, normalized.CaptchaType, "验证码类型"},
		{SMSBaoCredentialTypeSettingKey, normalized.SMSBaoCredentialType, "短信宝凭证类型"},
		{SMSBaoRegionModeSettingKey, normalized.SMSBaoRegionMode, "短信宝发送区域模式"},
	}
	for _, item := range settings {
		if err := saveSystemSettingValue(db, item.key, item.value, item.remark); err != nil {
			return SystemSettingDetail{}, err
		}
	}

	return GetSystemSettingDetailWithDB(db)
}

func getSystemSettingStringWithDefault(db *gorm.DB, key string, defaultValue string) string {
	setting, exists, err := getSystemSettingByKey(db, key)
	if err != nil || !exists {
		return defaultValue
	}
	value := strings.TrimSpace(setting.Value)
	if value == "" {
		return defaultValue
	}
	return value
}

func saveSystemSettingValue(db *gorm.DB, key string, value string, remark string) error {
	setting, _, err := getSystemSettingByKey(db, key)
	if err != nil {
		return err
	}
	if setting.Key == "" {
		setting.Key = key
		setting.Remark = remark
	}
	setting.Value = value
	freshDB := db.Session(&gorm.Session{NewDB: true})
	return freshDB.Save(&setting).Error
}

func normalizePaymentProvider(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), DefaultPaymentProvider) {
		return DefaultPaymentProvider
	}
	return DefaultPaymentProvider
}

func normalizePaymentMethod(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", DefaultPaymentMethod:
		return DefaultPaymentMethod
	case "wechat", paymentMethodWXPay:
		return paymentMethodWXPay
	default:
		return normalized
	}
}

func defaultString(value string, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func trimTrailingSlash(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func normalizeCaptchaProvider(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", DefaultCaptchaProvider:
		return DefaultCaptchaProvider
	case "local":
		return "local"
	default:
		return DefaultCaptchaProvider
	}
}

func normalizeCaptchaType(value string) string {
	trimmed := strings.TrimSpace(value)
	switch trimmed {
	case "随机":
		return "RANDOM"
	case "滑块", "滑块验证", "滑块验证码":
		return "SLIDER"
	case "旋转", "旋转验证", "旋转验证码":
		return "ROTATE"
	case "滑动还原", "滑动还原验证码":
		return "CONCAT"
	case "文字点选", "点选", "点选验证码", "文字点选验证码":
		return "WORD_IMAGE_CLICK"
	}
	normalized := strings.ToUpper(strings.TrimSpace(value))
	switch normalized {
	case "RANDOM", "SLIDER", "ROTATE", "CONCAT", "WORD_IMAGE_CLICK":
		return normalized
	default:
		return DefaultCaptchaType
	}
}

func normalizeSMSBaoCredentialType(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "password" {
		return "password"
	}
	return DefaultSMSBaoCredentialType
}

func normalizeSMSBaoRegionMode(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "domestic", "international":
		return normalized
	default:
		return DefaultSMSBaoRegionMode
	}
}

func normalizeEPayVersion(value string) string {
	return DefaultEPayVersion
}

func getSystemSettingByKey(db *gorm.DB, key string) (entity.SystemSetting, bool, error) {
	var setting entity.SystemSetting
	freshDB := db.Session(&gorm.Session{NewDB: true})
	if err := freshDB.Where("key = ?", key).First(&setting).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.SystemSetting{}, false, nil
		}
		return entity.SystemSetting{}, false, err
	}
	return setting, true, nil
}
