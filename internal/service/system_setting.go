package service

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const RegisterEnabledSettingKey = "register_enabled"
const NewUserBonusAmountSettingKey = "new_user_bonus_amount"
const SiteNameSettingKey = "site_name"
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
const WechatScanLoginEnabledSettingKey = "wechat_scan_login_enabled"
const WechatCodeLoginEnabledSettingKey = "wechat_code_login_enabled"
const WechatDefaultLoginModeSettingKey = "wechat_default_login_mode"
const WechatCodeStaticQRCodeURLSettingKey = "wechat_code_static_qrcode_url"
const WechatAPIBaseURLSettingKey = "wechat_api_base_url"
const WechatAPIBearerTokenSettingKey = "wechat_api_bearer_token"
const CaptchaProviderSettingKey = "captcha_provider"
const CaptchaAPIBaseURLSettingKey = "captcha_api_base_url"
const CaptchaAPIKeySettingKey = "captcha_api_key"
const CaptchaTypeSettingKey = "captcha_type"
const SMSBaoCredentialTypeSettingKey = "smsbao_credential_type"
const SMSBaoRegionModeSettingKey = "smsbao_region_mode"
const MaxLogSizeBytesSettingKey = "max_log_size_bytes"

// UAPIAPIKeySettingKey 是 UApiPro（uapis.cn）的 API 密钥。
//
// 用于查询手机号 / IP 归属地（用户管理、日志列表的归属地展示）。
// 与其它密钥一样：只从数据库读取，不硬编码、不拼进 URL、响应里脱敏。
const UAPIAPIKeySettingKey = "uapi_api_key"

const DefaultPaymentProvider = "epay"
const DefaultPaymentMethod = "alipay"
const DefaultEPayVersion = "v2"
const DefaultSiteName = "题库"
const DefaultNewUserBonusAmount = float64(0)
const DefaultSMSProvider = "smsbao"
const DefaultSMSCodeTemplate = "【{sign}】您的验证码是{code}，5分钟内有效。"
const DefaultCaptchaProvider = "remote"
const DefaultCaptchaAPIBaseURL = "https://captcha.api.zelly.cn"
const DefaultCaptchaAPIKey = ""
const DefaultCaptchaType = "RANDOM"
const DefaultSMSBaoCredentialType = "apikey"
const DefaultSMSBaoRegionMode = "auto"
const DefaultWechatLoginMode = "scan"
const DefaultMaxLogSizeBytes int64 = 5 * 1024 * 1024 * 1024

const paymentMethodWXPay = "wxpay"
const wechatLoginModeScan = "scan"
const wechatLoginModeCode = "code"

type SystemSettingDetail struct {
	RegisterEnabled           bool    `json:"registerEnabled"`
	NewUserBonusAmount        float64 `json:"newUserBonusAmount"`
	SiteName                  string  `json:"siteName"`
	AlternativeLoginEnabled   bool    `json:"alternativeLoginEnabled"`
	EPayVersion               string  `json:"epayVersion"`
	EPayGatewayURL            string  `json:"epayGatewayUrl"`
	EPayPartnerID             string  `json:"epayPartnerId"`
	EPayPlatformPublicKey     string  `json:"epayPlatformPublicKey"`
	EPayMerchantPrivateKey    string  `json:"epayMerchantPrivateKey"`
	EPayNotifyURL             string  `json:"epayNotifyUrl"`
	EPayReturnURL             string  `json:"epayReturnUrl"`
	SMSProvider               string  `json:"smsProvider"`
	SMSBaoUsername            string  `json:"smsbaoUsername"`
	SMSBaoSecret              string  `json:"smsbaoSecret"`
	SMSCodeSignName           string  `json:"smsCodeSignName"`
	SMSCodeTemplate           string  `json:"smsCodeTemplate"`
	SMSLoginEnabled           bool    `json:"smsLoginEnabled"`
	WechatLoginEnabled        bool    `json:"wechatLoginEnabled"`
	WechatScanLoginEnabled    bool    `json:"wechatScanLoginEnabled"`
	WechatCodeLoginEnabled    bool    `json:"wechatCodeLoginEnabled"`
	WechatDefaultLoginMode    string  `json:"wechatDefaultLoginMode"`
	WechatCodeStaticQRCodeURL string  `json:"wechatCodeStaticQrcodeUrl"`
	WechatAPIBaseURL          string  `json:"wechatApiBaseUrl"`
	WechatAPIBearerToken      string  `json:"wechatApiBearerToken"`
	CaptchaProvider           string  `json:"captchaProvider"`
	CaptchaAPIBaseURL         string  `json:"captchaApiBaseUrl"`
	CaptchaAPIKey             string  `json:"captchaApiKey"`
	CaptchaType               string  `json:"captchaType"`
	SMSBaoCredentialType      string  `json:"smsbaoCredentialType"`
	SMSBaoRegionMode          string  `json:"smsbaoRegionMode"`
	// UAPIAPIKey 是 UApiPro 的 API 密钥；与其它密钥一样**明文**返回。
	UAPIAPIKey      string `json:"uapiApiKey"`
	MaxLogSizeBytes int64  `json:"maxLogSizeBytes"`
}

type UpdateSystemSettingInput struct {
	RegisterEnabled           bool    `json:"registerEnabled"`
	NewUserBonusAmount        float64 `json:"newUserBonusAmount"`
	SiteName                  string  `json:"siteName"`
	EPayVersion               string  `json:"epayVersion"`
	EPayGatewayURL            string  `json:"epayGatewayUrl"`
	EPayPartnerID             string  `json:"epayPartnerId"`
	EPayPlatformPublicKey     string  `json:"epayPlatformPublicKey"`
	EPayMerchantPrivateKey    string  `json:"epayMerchantPrivateKey"`
	EPayNotifyURL             string  `json:"epayNotifyUrl"`
	EPayReturnURL             string  `json:"epayReturnUrl"`
	SMSProvider               string  `json:"smsProvider"`
	SMSBaoUsername            string  `json:"smsbaoUsername"`
	SMSBaoSecret              string  `json:"smsbaoSecret"`
	SMSCodeSignName           string  `json:"smsCodeSignName"`
	SMSCodeTemplate           string  `json:"smsCodeTemplate"`
	SMSLoginEnabled           bool    `json:"smsLoginEnabled"`
	WechatLoginEnabled        bool    `json:"wechatLoginEnabled"`
	WechatScanLoginEnabled    bool    `json:"wechatScanLoginEnabled"`
	WechatCodeLoginEnabled    bool    `json:"wechatCodeLoginEnabled"`
	WechatDefaultLoginMode    string  `json:"wechatDefaultLoginMode"`
	WechatCodeStaticQRCodeURL string  `json:"wechatCodeStaticQrcodeUrl"`
	WechatAPIBaseURL          string  `json:"wechatApiBaseUrl"`
	WechatAPIBearerToken      string  `json:"wechatApiBearerToken"`
	CaptchaProvider           string  `json:"captchaProvider"`
	CaptchaAPIBaseURL         string  `json:"captchaApiBaseUrl"`
	CaptchaAPIKey             string  `json:"captchaApiKey"`
	CaptchaType               string  `json:"captchaType"`
	SMSBaoCredentialType      string  `json:"smsbaoCredentialType"`
	SMSBaoRegionMode          string  `json:"smsbaoRegionMode"`
	// UAPIAPIKey 是 UApiPro 的 API 密钥；与其它密钥一样**明文**返回。
	// 写入语义与其它密钥一致：空值或 ******** 表示保持原值，__CLEAR__ 表示清空。
	UAPIAPIKey      string `json:"uapiApiKey"`
	MaxLogSizeBytes int64  `json:"maxLogSizeBytes"`
}

var ErrInvalidNewUserBonusAmount = errors.New("新用户赠送金额不能为负数")

type PublicAuthSettings struct {
	RegisterEnabled           bool   `json:"registerEnabled"`
	SiteName                  string `json:"siteName"`
	SMSLoginEnabled           bool   `json:"smsLoginEnabled"`
	WechatLoginEnabled        bool   `json:"wechatLoginEnabled"`
	WechatScanLoginEnabled    bool   `json:"wechatScanLoginEnabled"`
	WechatCodeLoginEnabled    bool   `json:"wechatCodeLoginEnabled"`
	WechatDefaultLoginMode    string `json:"wechatDefaultLoginMode"`
	WechatCodeStaticQRCodeURL string `json:"wechatCodeStaticQrcodeUrl"`
	AlternativeLoginEnabled   bool   `json:"alternativeLoginEnabled"`
	CaptchaProvider           string `json:"captchaProvider"`
	CaptchaAPIBaseURL         string `json:"captchaApiBaseUrl"`
	CaptchaType               string `json:"captchaType"`
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

func GetNewUserBonusAmountWithDB(db *gorm.DB) (float64, error) {
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, NewUserBonusAmountSettingKey, "0"))
	amount, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return DefaultNewUserBonusAmount, nil
	}
	normalized, err := normalizeNewUserBonusAmount(amount)
	if err != nil {
		return DefaultNewUserBonusAmount, nil
	}
	return normalized, nil
}

func normalizeNewUserBonusAmount(amount float64) (float64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return 0, ErrInvalidNewUserBonusAmount
	}
	return util.RoundMoney(amount), nil
}

func GetPublicAuthSettingsWithDB(db *gorm.DB) (PublicAuthSettings, error) {
	registerEnabled, err := GetRegisterEnabledWithDB(db)
	if err != nil {
		return PublicAuthSettings{}, err
	}
	smsLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, SMSLoginEnabledSettingKey, "false")), "true")
	wechatAPIBaseURL := trimTrailingSlash(getSystemSettingStringWithDefault(db, WechatAPIBaseURLSettingKey, ""))
	wechatAPIBearerToken := strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatAPIBearerTokenSettingKey, ""))
	wechatSettings := getNormalizedWechatLoginSettingsWithDB(db)
	wechatConfigReady := wechatSettings.WechatLoginEnabled && wechatAPIBaseURL != "" && wechatAPIBearerToken != ""
	publicWechatScanLoginEnabled := wechatConfigReady && wechatSettings.WechatScanLoginEnabled
	publicWechatCodeLoginEnabled := wechatConfigReady && wechatSettings.WechatCodeLoginEnabled && wechatSettings.WechatCodeStaticQRCodeURL != ""
	publicWechatLoginEnabled := publicWechatScanLoginEnabled || publicWechatCodeLoginEnabled
	publicWechatDefaultLoginMode := normalizeEnabledWechatLoginMode(wechatSettings.WechatDefaultLoginMode, publicWechatScanLoginEnabled, publicWechatCodeLoginEnabled)
	return PublicAuthSettings{
		RegisterEnabled:           registerEnabled,
		SiteName:                  normalizeSiteName(getSystemSettingStringWithDefault(db, SiteNameSettingKey, DefaultSiteName)),
		SMSLoginEnabled:           smsLoginEnabled,
		WechatLoginEnabled:        publicWechatLoginEnabled,
		WechatScanLoginEnabled:    publicWechatScanLoginEnabled,
		WechatCodeLoginEnabled:    publicWechatCodeLoginEnabled,
		WechatDefaultLoginMode:    publicWechatDefaultLoginMode,
		WechatCodeStaticQRCodeURL: wechatSettings.WechatCodeStaticQRCodeURL,
		AlternativeLoginEnabled:   smsLoginEnabled || publicWechatLoginEnabled,
		CaptchaProvider:           normalizeCaptchaProvider(getSystemSettingStringWithDefault(db, CaptchaProviderSettingKey, DefaultCaptchaProvider)),
		CaptchaAPIBaseURL:         trimTrailingSlash(getSystemSettingStringWithDefault(db, CaptchaAPIBaseURLSettingKey, DefaultCaptchaAPIBaseURL)),
		CaptchaType:               normalizeCaptchaType(getSystemSettingStringWithDefault(db, CaptchaTypeSettingKey, DefaultCaptchaType)),
	}, nil
}

func GetSystemSettingDetailWithDB(db *gorm.DB) (SystemSettingDetail, error) {
	registerEnabled, err := GetRegisterEnabledWithDB(db)
	if err != nil {
		return SystemSettingDetail{}, err
	}

	smsLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, SMSLoginEnabledSettingKey, "false")), "true")
	wechatSettings := getNormalizedWechatLoginSettingsWithDB(db)
	detail := SystemSettingDetail{
		RegisterEnabled:           registerEnabled,
		NewUserBonusAmount:        DefaultNewUserBonusAmount,
		SiteName:                  normalizeSiteName(getSystemSettingStringWithDefault(db, SiteNameSettingKey, DefaultSiteName)),
		AlternativeLoginEnabled:   smsLoginEnabled || wechatSettings.WechatLoginEnabled,
		EPayVersion:               DefaultEPayVersion,
		EPayGatewayURL:            getSystemSettingStringWithDefault(db, EPayGatewayURLSettingKey, ""),
		EPayPartnerID:             getSystemSettingStringWithDefault(db, EPayPartnerIDSettingKey, ""),
		EPayPlatformPublicKey:     getSystemSettingStringWithDefault(db, EPayPlatformPublicKeySettingKey, ""),
		EPayMerchantPrivateKey:    getSystemSettingStringWithDefault(db, EPayMerchantPrivateKeySettingKey, ""),
		EPayNotifyURL:             getSystemSettingStringWithDefault(db, EPayNotifyURLSettingKey, ""),
		EPayReturnURL:             getSystemSettingStringWithDefault(db, EPayReturnURLSettingKey, ""),
		SMSProvider:               getSystemSettingStringWithDefault(db, SMSProviderSettingKey, DefaultSMSProvider),
		SMSBaoUsername:            getSystemSettingStringWithDefault(db, SMSBaoUsernameSettingKey, ""),
		SMSBaoSecret:              getSystemSettingStringWithDefault(db, SMSBaoSecretSettingKey, ""),
		SMSCodeSignName:           getSystemSettingStringWithDefault(db, SMSCodeSignNameSettingKey, ""),
		SMSCodeTemplate:           getSystemSettingStringWithDefault(db, SMSCodeTemplateSettingKey, DefaultSMSCodeTemplate),
		SMSLoginEnabled:           smsLoginEnabled,
		WechatLoginEnabled:        wechatSettings.WechatLoginEnabled,
		WechatScanLoginEnabled:    wechatSettings.WechatScanLoginEnabled,
		WechatCodeLoginEnabled:    wechatSettings.WechatCodeLoginEnabled,
		WechatDefaultLoginMode:    wechatSettings.WechatDefaultLoginMode,
		WechatCodeStaticQRCodeURL: wechatSettings.WechatCodeStaticQRCodeURL,
		WechatAPIBaseURL:          trimTrailingSlash(getSystemSettingStringWithDefault(db, WechatAPIBaseURLSettingKey, "")),
		WechatAPIBearerToken:      strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatAPIBearerTokenSettingKey, "")),
		CaptchaProvider:           normalizeCaptchaProvider(getSystemSettingStringWithDefault(db, CaptchaProviderSettingKey, DefaultCaptchaProvider)),
		CaptchaAPIBaseURL:         trimTrailingSlash(getSystemSettingStringWithDefault(db, CaptchaAPIBaseURLSettingKey, DefaultCaptchaAPIBaseURL)),
		CaptchaAPIKey:             strings.TrimSpace(getSystemSettingStringWithDefault(db, CaptchaAPIKeySettingKey, DefaultCaptchaAPIKey)),
		CaptchaType:               normalizeCaptchaType(getSystemSettingStringWithDefault(db, CaptchaTypeSettingKey, DefaultCaptchaType)),
		SMSBaoCredentialType:      normalizeSMSBaoCredentialType(getSystemSettingStringWithDefault(db, SMSBaoCredentialTypeSettingKey, DefaultSMSBaoCredentialType)),
		SMSBaoRegionMode:          normalizeSMSBaoRegionMode(getSystemSettingStringWithDefault(db, SMSBaoRegionModeSettingKey, DefaultSMSBaoRegionMode)),
		UAPIAPIKey:                strings.TrimSpace(getSystemSettingStringWithDefault(db, UAPIAPIKeySettingKey, "")),
		MaxLogSizeBytes:           getPositiveInt64SystemSetting(db, MaxLogSizeBytesSettingKey, DefaultMaxLogSizeBytes),
	}
	bonusAmount, err := GetNewUserBonusAmountWithDB(db)
	if err != nil {
		return SystemSettingDetail{}, err
	}
	detail.NewUserBonusAmount = bonusAmount
	return detail, nil
}

func UpdateSystemSettingDetailWithDB(db *gorm.DB, input UpdateSystemSettingInput) (SystemSettingDetail, error) {
	if _, err := SetRegisterEnabledWithDB(db, input.RegisterEnabled); err != nil {
		return SystemSettingDetail{}, err
	}
	bonusAmount, err := normalizeNewUserBonusAmount(input.NewUserBonusAmount)
	if err != nil {
		return SystemSettingDetail{}, err
	}

	normalizedWechatLoginEnabled := input.WechatLoginEnabled && (input.WechatScanLoginEnabled || input.WechatCodeLoginEnabled)
	normalizedWechatScanLoginEnabled := normalizedWechatLoginEnabled && input.WechatScanLoginEnabled
	normalizedWechatCodeLoginEnabled := normalizedWechatLoginEnabled && input.WechatCodeLoginEnabled
	normalizedWechatDefaultLoginMode := normalizeEnabledWechatLoginMode(input.WechatDefaultLoginMode, normalizedWechatScanLoginEnabled, normalizedWechatCodeLoginEnabled)
	if normalizedWechatDefaultLoginMode == "" {
		normalizedWechatDefaultLoginMode = DefaultWechatLoginMode
	}
	normalized := SystemSettingDetail{
		RegisterEnabled:           input.RegisterEnabled,
		NewUserBonusAmount:        bonusAmount,
		SiteName:                  normalizeSiteName(input.SiteName),
		EPayVersion:               normalizeEPayVersion(input.EPayVersion),
		EPayGatewayURL:            strings.TrimSpace(input.EPayGatewayURL),
		EPayPartnerID:             strings.TrimSpace(input.EPayPartnerID),
		EPayPlatformPublicKey:     strings.TrimSpace(input.EPayPlatformPublicKey),
		EPayMerchantPrivateKey:    mergeStoredSecretInput(db, EPayMerchantPrivateKeySettingKey, input.EPayMerchantPrivateKey),
		EPayNotifyURL:             strings.TrimSpace(input.EPayNotifyURL),
		EPayReturnURL:             strings.TrimSpace(input.EPayReturnURL),
		SMSProvider:               strings.TrimSpace(input.SMSProvider),
		SMSBaoUsername:            strings.TrimSpace(input.SMSBaoUsername),
		SMSBaoSecret:              mergeStoredSecretInput(db, SMSBaoSecretSettingKey, input.SMSBaoSecret),
		SMSCodeSignName:           strings.TrimSpace(input.SMSCodeSignName),
		SMSCodeTemplate:           strings.TrimSpace(input.SMSCodeTemplate),
		SMSLoginEnabled:           input.SMSLoginEnabled,
		WechatLoginEnabled:        normalizedWechatLoginEnabled,
		WechatScanLoginEnabled:    normalizedWechatScanLoginEnabled,
		WechatCodeLoginEnabled:    normalizedWechatCodeLoginEnabled,
		WechatDefaultLoginMode:    normalizedWechatDefaultLoginMode,
		WechatCodeStaticQRCodeURL: strings.TrimSpace(input.WechatCodeStaticQRCodeURL),
		WechatAPIBaseURL:          trimTrailingSlash(input.WechatAPIBaseURL),
		WechatAPIBearerToken:      mergeStoredSecretInput(db, WechatAPIBearerTokenSettingKey, input.WechatAPIBearerToken),
		CaptchaProvider:           normalizeCaptchaProvider(input.CaptchaProvider),
		CaptchaAPIBaseURL:         trimTrailingSlash(defaultString(input.CaptchaAPIBaseURL, DefaultCaptchaAPIBaseURL)),
		CaptchaAPIKey:             mergeStoredSecretInput(db, CaptchaAPIKeySettingKey, input.CaptchaAPIKey),
		CaptchaType:               normalizeCaptchaType(input.CaptchaType),
		SMSBaoCredentialType:      normalizeSMSBaoCredentialType(input.SMSBaoCredentialType),
		SMSBaoRegionMode:          normalizeSMSBaoRegionMode(input.SMSBaoRegionMode),
		// UApiPro 密钥不脱敏：系统设置页本身只有管理员能打开（与「默认账号配置」
		// 的 token 处理一致），明文存取、由前端密码框隐藏显示。
		// 直接 TrimSpace 而不是 mergeStoredSecretInput —— 留空即清空，语义直观。
		UAPIAPIKey:      strings.TrimSpace(input.UAPIAPIKey),
		MaxLogSizeBytes: resolveMaxLogSizeBytes(db, input.MaxLogSizeBytes),
	}
	normalized.AlternativeLoginEnabled = normalized.SMSLoginEnabled || normalized.WechatLoginEnabled

	settings := []struct {
		key    string
		value  string
		remark string
	}{
		{SiteNameSettingKey, normalized.SiteName, "网站名称"},
		{NewUserBonusAmountSettingKey, strconv.FormatFloat(normalized.NewUserBonusAmount, 'f', 2, 64), "新用户默认赠送金额"},
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
		{WechatLoginEnabledSettingKey, map[bool]string{true: "true", false: "false"}[normalized.WechatLoginEnabled], "是否开启微信登录"},
		{WechatScanLoginEnabledSettingKey, map[bool]string{true: "true", false: "false"}[normalized.WechatScanLoginEnabled], "是否开启微信扫描登录"},
		{WechatCodeLoginEnabledSettingKey, map[bool]string{true: "true", false: "false"}[normalized.WechatCodeLoginEnabled], "是否开启微信验证码登录"},
		{WechatDefaultLoginModeSettingKey, normalized.WechatDefaultLoginMode, "默认微信登录方式"},
		{WechatCodeStaticQRCodeURLSettingKey, normalized.WechatCodeStaticQRCodeURL, "微信验证码登录静态二维码"},
		{WechatAPIBaseURLSettingKey, normalized.WechatAPIBaseURL, "微信登录服务地址"},
		{WechatAPIBearerTokenSettingKey, normalized.WechatAPIBearerToken, "微信登录服务 Bearer Token"},
		{CaptchaProviderSettingKey, normalized.CaptchaProvider, "验证码提供方"},
		{CaptchaAPIBaseURLSettingKey, normalized.CaptchaAPIBaseURL, "远程验证码服务地址"},
		{CaptchaAPIKeySettingKey, normalized.CaptchaAPIKey, "远程验证码 X-Captcha-Key"},
		{CaptchaTypeSettingKey, normalized.CaptchaType, "验证码类型"},
		{SMSBaoCredentialTypeSettingKey, normalized.SMSBaoCredentialType, "短信宝凭证类型"},
		{SMSBaoRegionModeSettingKey, normalized.SMSBaoRegionMode, "短信宝发送区域模式"},
		{UAPIAPIKeySettingKey, normalized.UAPIAPIKey, "UApiPro API 密钥"},
		{MaxLogSizeBytesSettingKey, strconv.FormatInt(normalized.MaxLogSizeBytes, 10), "日志文件总大小上限（字节）"},
	}
	for _, item := range settings {
		if err := saveSystemSettingValue(db, item.key, item.value, item.remark); err != nil {
			return SystemSettingDetail{}, err
		}
	}

	return GetSystemSettingDetailWithDB(db)
}

func resolveMaxLogSizeBytes(db *gorm.DB, requested int64) int64 {
	if requested > 0 {
		return requested
	}
	return getPositiveInt64SystemSetting(db, MaxLogSizeBytesSettingKey, DefaultMaxLogSizeBytes)
}

func getPositiveInt64SystemSetting(db *gorm.DB, key string, defaultValue int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(getSystemSettingStringWithDefault(db, key, strconv.FormatInt(defaultValue, 10))), 10, 64)
	if err != nil || value <= 0 {
		return defaultValue
	}
	return value
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

func normalizeSiteName(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return DefaultSiteName
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

func getNormalizedWechatLoginSettingsWithDB(db *gorm.DB) SystemSettingDetail {
	wechatLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatLoginEnabledSettingKey, "false")), "true")
	legacyScanDefault := "false"
	if wechatLoginEnabled {
		legacyScanDefault = "true"
	}
	wechatScanLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatScanLoginEnabledSettingKey, legacyScanDefault)), "true")
	wechatCodeLoginEnabled := strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatCodeLoginEnabledSettingKey, "false")), "true")
	if !wechatScanLoginEnabled && !wechatCodeLoginEnabled {
		wechatLoginEnabled = false
	}
	if !wechatLoginEnabled {
		wechatScanLoginEnabled = false
		wechatCodeLoginEnabled = false
	}
	defaultMode := normalizeEnabledWechatLoginMode(
		getSystemSettingStringWithDefault(db, WechatDefaultLoginModeSettingKey, DefaultWechatLoginMode),
		wechatScanLoginEnabled,
		wechatCodeLoginEnabled,
	)
	if defaultMode == "" {
		defaultMode = DefaultWechatLoginMode
	}
	return SystemSettingDetail{
		WechatLoginEnabled:        wechatLoginEnabled,
		WechatScanLoginEnabled:    wechatScanLoginEnabled,
		WechatCodeLoginEnabled:    wechatCodeLoginEnabled,
		WechatDefaultLoginMode:    defaultMode,
		WechatCodeStaticQRCodeURL: strings.TrimSpace(getSystemSettingStringWithDefault(db, WechatCodeStaticQRCodeURLSettingKey, "")),
	}
}

func normalizeWechatLoginMode(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", wechatLoginModeScan, "qrcode", "qr", "scan_qr":
		return wechatLoginModeScan
	case wechatLoginModeCode, "captcha", "verification_code":
		return wechatLoginModeCode
	default:
		return DefaultWechatLoginMode
	}
}

func normalizeEnabledWechatLoginMode(value string, scanEnabled bool, codeEnabled bool) string {
	normalized := normalizeWechatLoginMode(value)
	if normalized == wechatLoginModeCode && codeEnabled {
		return wechatLoginModeCode
	}
	if normalized == wechatLoginModeScan && scanEnabled {
		return wechatLoginModeScan
	}
	if scanEnabled {
		return wechatLoginModeScan
	}
	if codeEnabled {
		return wechatLoginModeCode
	}
	return ""
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
