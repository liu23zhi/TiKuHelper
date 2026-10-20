package service

import (
	"encoding/json"
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
const MaxAISearchModelCallsSettingKey = "max_ai_search_model_calls"
const MaxAISearchesSettingKey = "max_ai_searches"
const MaxAISearchRetriesSettingKey = "max_ai_search_retries"
const AISearchPricePerCallSettingKey = "ai_search_price_per_call"
const MaxAIWebReadsSettingKey = "max_ai_web_reads"
const WebReadPricePerCallSettingKey = "web_read_price_per_call"
const AIStagedTokenBillingEnabledSettingKey = "enable_ai_staged_token_billing"
const SmoothAIStagedTokenMultiplierSettingKey = "smooth_ai_staged_token_multiplier"
const AIStagedTokenMultiplierPointsSettingKey = "ai_staged_token_multiplier_points"
const MinRechargeAmountSettingKey = "min_recharge_amount"

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
const DefaultMaxAISearchModelCalls = 4
const DefaultMaxAISearches = 3
const DefaultMaxAISearchRetries = 3
const DefaultAISearchPricePerCall = float64(0)
const DefaultMaxAIWebReads = 5
const DefaultWebReadPricePerCall = float64(0)
const DefaultMinRechargeAmount = float64(1)
const MaxAllowedAISearchModelCalls = 20
const MaxAllowedAISearches = 20
const MaxAllowedAISearchRetries = 10

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
	UAPIAPIKey            string  `json:"uapiApiKey"`
	MaxLogSizeBytes       int64   `json:"maxLogSizeBytes"`
	MaxAISearchModelCalls int     `json:"maxAISearchModelCalls"`
	MaxAISearches         int     `json:"maxAISearches"`
	MaxAISearchRetries    int     `json:"maxAISearchRetries"`
	MaxAIWebReads         int     `json:"maxAIWebReads"`
	WebReadPricePerCall   float64 `json:"webReadPricePerCall"`
	MinRechargeAmount     float64 `json:"minRechargeAmount"`
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
	UAPIAPIKey            string  `json:"uapiApiKey"`
	MaxLogSizeBytes       int64   `json:"maxLogSizeBytes"`
	MaxAISearchModelCalls int     `json:"maxAISearchModelCalls"`
	MaxAISearches         int     `json:"maxAISearches"`
	MaxAISearchRetries    int     `json:"maxAISearchRetries"`
	MaxAIWebReads         int     `json:"maxAIWebReads"`
	WebReadPricePerCall   float64 `json:"webReadPricePerCall"`
	MinRechargeAmount     float64 `json:"minRechargeAmount"`
}

var ErrInvalidNewUserBonusAmount = errors.New("新用户赠送金额不能为负数")
var ErrInvalidMinRechargeAmount = errors.New("最低充值金额必须是大于 0 的有效金额")

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
		MaxAISearchModelCalls:     getPositiveBoundedIntSystemSetting(db, MaxAISearchModelCallsSettingKey, DefaultMaxAISearchModelCalls, MaxAllowedAISearchModelCalls),
		MaxAISearches:             getPositiveBoundedIntSystemSetting(db, MaxAISearchesSettingKey, DefaultMaxAISearches, MaxAllowedAISearches),
		MaxAISearchRetries:        getPositiveBoundedIntSystemSetting(db, MaxAISearchRetriesSettingKey, DefaultMaxAISearchRetries, MaxAllowedAISearchRetries),
		MaxAIWebReads:             getPositiveBoundedIntSystemSetting(db, MaxAIWebReadsSettingKey, DefaultMaxAIWebReads, MaxAllowedAISearches),
		WebReadPricePerCall:       GetWebReadPricePerCallWithDB(db),
		MinRechargeAmount:         GetMinRechargeAmountWithDB(db),
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
	minRechargeAmount, err := resolveMinRechargeAmount(db, input.MinRechargeAmount)
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
		UAPIAPIKey:            strings.TrimSpace(input.UAPIAPIKey),
		MaxLogSizeBytes:       resolveMaxLogSizeBytes(db, input.MaxLogSizeBytes),
		MaxAISearchModelCalls: resolveMaxAISearchBudget(db, input.MaxAISearchModelCalls, MaxAISearchModelCallsSettingKey, DefaultMaxAISearchModelCalls, MaxAllowedAISearchModelCalls),
		MaxAISearches:         resolveMaxAISearchBudget(db, input.MaxAISearches, MaxAISearchesSettingKey, DefaultMaxAISearches, MaxAllowedAISearches),
		MaxAISearchRetries:    resolveMaxAISearchBudget(db, input.MaxAISearchRetries, MaxAISearchRetriesSettingKey, DefaultMaxAISearchRetries, MaxAllowedAISearchRetries),
		MaxAIWebReads:         resolveMaxAISearchBudget(db, input.MaxAIWebReads, MaxAIWebReadsSettingKey, DefaultMaxAIWebReads, MaxAllowedAISearches),
		WebReadPricePerCall:   resolveWebReadPrice(db, input.WebReadPricePerCall),
		MinRechargeAmount:     minRechargeAmount,
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
		{MaxAISearchModelCallsSettingKey, strconv.Itoa(normalized.MaxAISearchModelCalls), "AI 搜索模型最大调用次数"},
		{MaxAISearchesSettingKey, strconv.Itoa(normalized.MaxAISearches), "AI 搜索最大搜索次数"},
		{MaxAISearchRetriesSettingKey, strconv.Itoa(normalized.MaxAISearchRetries), "AI 搜索每轮失败重试次数（含首次调用）"},
		{MaxAIWebReadsSettingKey, strconv.Itoa(normalized.MaxAIWebReads), "AI 搜索网页读取最大次数"},
		{WebReadPricePerCallSettingKey, strconv.FormatFloat(normalized.WebReadPricePerCall, 'f', -1, 64), "网页读取单价（元/次）"},
		{MinRechargeAmountSettingKey, formatRechargeSettingAmount(normalized.MinRechargeAmount), "充值最低金额（元）"},
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

// GetMinRechargeAmountWithDB returns the configured minimum recharge amount.
// Invalid or missing persisted values fall back to the one-yuan default so a
// malformed setting cannot accidentally disable the server-side restriction.
func GetMinRechargeAmountWithDB(db *gorm.DB) float64 {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return DefaultMinRechargeAmount
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, MinRechargeAmountSettingKey, formatRechargeSettingAmount(DefaultMinRechargeAmount)))
	amount, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return DefaultMinRechargeAmount
	}
	normalized, err := normalizeMinRechargeAmount(amount)
	if err != nil {
		return DefaultMinRechargeAmount
	}
	return normalized
}

func normalizeMinRechargeAmount(amount float64) (float64, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return 0, ErrInvalidMinRechargeAmount
	}
	return util.RoundMoneyToDecimals(amount, util.RechargePrecision), nil
}

func resolveMinRechargeAmount(db *gorm.DB, requested float64) (float64, error) {
	if requested > 0 {
		return normalizeMinRechargeAmount(requested)
	}
	if requested < 0 || math.IsNaN(requested) || math.IsInf(requested, 0) {
		return 0, ErrInvalidMinRechargeAmount
	}
	// A zero value means the field was omitted by an older frontend. Preserve
	// the previously stored value instead of resetting it during unrelated
	// settings updates.
	return GetMinRechargeAmountWithDB(db), nil
}

func formatRechargeSettingAmount(amount float64) string {
	return strconv.FormatFloat(util.RoundMoneyToDecimals(amount, util.RechargePrecision), 'f', util.RechargePrecision, 64)
}

func getPositiveInt64SystemSetting(db *gorm.DB, key string, defaultValue int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(getSystemSettingStringWithDefault(db, key, strconv.FormatInt(defaultValue, 10))), 10, 64)
	if err != nil || value <= 0 {
		return defaultValue
	}
	return value
}

func resolveMaxAISearchBudget(db *gorm.DB, requested int, key string, defaultValue int, maxValue int) int {
	if requested > 0 {
		return normalizeAISearchBudget(requested, defaultValue, maxValue)
	}
	return getPositiveBoundedIntSystemSetting(db, key, defaultValue, maxValue)
}

// GetAISearchBudgetsWithDB returns the model-round and web-search limits used
// by every AI search entry point. Invalid or missing settings fall back to the
// safe defaults so a settings row cannot disable the search path accidentally.
func GetAISearchBudgetsWithDB(db *gorm.DB) (modelCalls int, searches int) {
	return getPositiveBoundedIntSystemSetting(db, MaxAISearchModelCallsSettingKey, DefaultMaxAISearchModelCalls, MaxAllowedAISearchModelCalls),
		getPositiveBoundedIntSystemSetting(db, MaxAISearchesSettingKey, DefaultMaxAISearches, MaxAllowedAISearches)
}

// GetAISearchRetriesWithDB returns the total number of attempts allowed for
// each AI model round. The first request is included in this count.
func GetAISearchRetriesWithDB(db *gorm.DB) int {
	return getPositiveBoundedIntSystemSetting(db, MaxAISearchRetriesSettingKey, DefaultMaxAISearchRetries, MaxAllowedAISearchRetries)
}

// SetAISearchRetriesWithDB updates the total per-round attempt count. A value
// of zero is treated as omitted by callers and therefore keeps the current
// setting; invalid persisted values fall back to the default on read.
func SetAISearchRetriesWithDB(db *gorm.DB, requested int) error {
	retries := GetAISearchRetriesWithDB(db)
	if requested > 0 {
		retries = normalizeAISearchBudget(requested, DefaultMaxAISearchRetries, MaxAllowedAISearchRetries)
	}
	return saveSystemSettingValue(db, MaxAISearchRetriesSettingKey, strconv.Itoa(retries), "AI 搜索每轮失败重试次数（含首次调用）")
}

// SetAISearchBudgetsWithDB updates only the AI search budget rows. It is used
// by the AI search configuration endpoint so that changing the AI account does
// not overwrite unrelated system settings.
func SetAISearchBudgetsWithDB(db *gorm.DB, requestedModelCalls int, requestedSearches int) error {
	modelCalls, searches := GetAISearchBudgetsWithDB(db)
	if requestedModelCalls > 0 {
		modelCalls = normalizeAISearchBudget(requestedModelCalls, DefaultMaxAISearchModelCalls, MaxAllowedAISearchModelCalls)
	}
	if requestedSearches > 0 {
		searches = normalizeAISearchBudget(requestedSearches, DefaultMaxAISearches, MaxAllowedAISearches)
	}
	if err := saveSystemSettingValue(db, MaxAISearchModelCallsSettingKey, strconv.Itoa(modelCalls), "AI 搜索模型最大调用次数"); err != nil {
		return err
	}
	return saveSystemSettingValue(db, MaxAISearchesSettingKey, strconv.Itoa(searches), "AI 搜索最大搜索次数")
}

func GetAISearchPricePerCallWithDB(db *gorm.DB) float64 {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return DefaultAISearchPricePerCall
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, AISearchPricePerCallSettingKey, "0"))
	price, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
		return DefaultAISearchPricePerCall
	}
	return util.RoundMoney(price)
}

func SetAISearchPricePerCallWithDB(db *gorm.DB, requested float64) error {
	if math.IsNaN(requested) || math.IsInf(requested, 0) || requested < 0 {
		return errors.New("联网搜索单价必须是大于或等于 0 的有效金额")
	}
	price := util.RoundMoney(requested)
	return saveSystemSettingValue(db, AISearchPricePerCallSettingKey, strconv.FormatFloat(price, 'f', -1, 64), "联网搜索单价（元/次）")
}

func GetWebReadPricePerCallWithDB(db *gorm.DB) float64 {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return DefaultWebReadPricePerCall
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, WebReadPricePerCallSettingKey, "0"))
	price, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
		return DefaultWebReadPricePerCall
	}
	return util.RoundMoney(price)
}

func SetWebReadPricePerCallWithDB(db *gorm.DB, requested float64) error {
	if math.IsNaN(requested) || math.IsInf(requested, 0) || requested < 0 {
		return errors.New("网页读取单价必须是大于或等于 0 的有效金额")
	}
	return saveSystemSettingValue(db, WebReadPricePerCallSettingKey, strconv.FormatFloat(util.RoundMoney(requested), 'f', -1, 64), "网页读取单价（元/次）")
}

type AIStagedTokenBillingSettings struct {
	Enabled          bool
	SmoothFactor     bool
	MultiplierPoints []AIStagedTokenMultiplierPoint
}

type AIStagedTokenMultiplierPoint struct {
	Tokens     int64   `json:"tokens"`
	Multiplier float64 `json:"multiplier"`
}

func DefaultAIStagedTokenMultiplierPoints() []AIStagedTokenMultiplierPoint {
	return []AIStagedTokenMultiplierPoint{
		{Tokens: 0, Multiplier: 4},
		{Tokens: 3000, Multiplier: 2},
		{Tokens: 10000, Multiplier: 1},
		{Tokens: 30000, Multiplier: 0.65},
		{Tokens: 100000, Multiplier: 0.3},
	}
}

func NormalizeAIStagedTokenMultiplierPoints(points []AIStagedTokenMultiplierPoint) ([]AIStagedTokenMultiplierPoint, error) {
	if len(points) < 1 {
		return nil, errors.New("阶段倍率价格表至少需要保留首个 0 token 节点")
	}
	normalized := make([]AIStagedTokenMultiplierPoint, len(points))
	copy(normalized, points)
	for index := range normalized {
		point := &normalized[index]
		if point.Tokens < 0 {
			return nil, errors.New("阶段倍率 token 阈值不能小于 0")
		}
		if math.IsNaN(point.Multiplier) || math.IsInf(point.Multiplier, 0) {
			return nil, errors.New("阶段倍率必须是有限数值")
		}
		if index > 0 {
			previous := normalized[index-1]
			if point.Tokens <= previous.Tokens {
				return nil, errors.New("阶段倍率 token 阈值必须严格递增")
			}
			if point.Multiplier > previous.Multiplier {
				return nil, errors.New("阶段倍率必须随 token 数量增加而不升高")
			}
		}
	}
	if normalized[0].Tokens != 0 {
		return nil, errors.New("阶段倍率价格表首个节点的 token 阈值必须为 0")
	}
	return normalized, nil
}

func cloneAIStagedTokenMultiplierPoints(points []AIStagedTokenMultiplierPoint) []AIStagedTokenMultiplierPoint {
	cloned := make([]AIStagedTokenMultiplierPoint, len(points))
	copy(cloned, points)
	return cloned
}

func GetAIStagedTokenBillingSettingsWithDB(db *gorm.DB) AIStagedTokenBillingSettings {
	points := DefaultAIStagedTokenMultiplierPoints()
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, AIStagedTokenMultiplierPointsSettingKey, ""))
	if raw != "" {
		var stored []AIStagedTokenMultiplierPoint
		if json.Unmarshal([]byte(raw), &stored) == nil {
			if normalized, err := NormalizeAIStagedTokenMultiplierPoints(stored); err == nil {
				points = normalized
			}
		}
	}
	return AIStagedTokenBillingSettings{
		Enabled:          getBoolSystemSetting(db, AIStagedTokenBillingEnabledSettingKey),
		SmoothFactor:     getBoolSystemSetting(db, SmoothAIStagedTokenMultiplierSettingKey),
		MultiplierPoints: cloneAIStagedTokenMultiplierPoints(points),
	}
}

func SetAIStagedTokenBillingSettingsWithDB(db *gorm.DB, enabled *bool, smoothFactor *bool, points ...*[]AIStagedTokenMultiplierPoint) error {
	if len(points) > 0 && points[0] != nil {
		normalized, err := NormalizeAIStagedTokenMultiplierPoints(*points[0])
		if err != nil {
			return err
		}
		payload, err := json.Marshal(normalized)
		if err != nil {
			return err
		}
		if err := saveSystemSettingValue(db, AIStagedTokenMultiplierPointsSettingKey, string(payload), "AI 单次模型调用阶段倍率价格表"); err != nil {
			return err
		}
	}
	if enabled != nil {
		if err := saveSystemSettingValue(db, AIStagedTokenBillingEnabledSettingKey, strconv.FormatBool(*enabled), "启用 AI 单次模型调用分阶段计费"); err != nil {
			return err
		}
	}
	if smoothFactor != nil {
		if err := saveSystemSettingValue(db, SmoothAIStagedTokenMultiplierSettingKey, strconv.FormatBool(*smoothFactor), "AI 阶段计费使用平滑倍率"); err != nil {
			return err
		}
	}
	return nil
}

func getBoolSystemSetting(db *gorm.DB, key string) bool {
	if db == nil || !db.Migrator().HasTable(&entity.SystemSetting{}) {
		return false
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, key, "false"))
	value, err := strconv.ParseBool(raw)
	return err == nil && value
}

func resolveWebReadPrice(db *gorm.DB, requested float64) float64 {
	if requested > 0 {
		return util.RoundMoney(requested)
	}
	return GetWebReadPricePerCallWithDB(db)
}

func getPositiveBoundedIntSystemSetting(db *gorm.DB, key string, defaultValue int, maxValue int) int {
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, key, strconv.Itoa(defaultValue)))
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultValue
	}
	return normalizeAISearchBudget(value, defaultValue, maxValue)
}

func normalizeAISearchBudget(value int, defaultValue int, maxValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if maxValue > 0 && value > maxValue {
		return maxValue
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
