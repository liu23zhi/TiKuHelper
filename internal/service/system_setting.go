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

const DefaultPaymentProvider = "epay"
const DefaultPaymentMethod = "alipay"
const DefaultEPayVersion = "v2"
const DefaultSMSProvider = "smsbao"
const DefaultSMSCodeTemplate = "【{sign}】您的验证码是{code}，5分钟内有效。"

const paymentMethodWXPay = "wxpay"

type SystemSettingDetail struct {
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
}

type PublicAuthSettings struct {
	RegisterEnabled bool `json:"registerEnabled"`
	SMSLoginEnabled bool `json:"smsLoginEnabled"`
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
	return PublicAuthSettings{
		RegisterEnabled: registerEnabled,
		SMSLoginEnabled: strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, SMSLoginEnabledSettingKey, "false")), "true"),
	}, nil
}

func GetSystemSettingDetailWithDB(db *gorm.DB) (SystemSettingDetail, error) {
	registerEnabled, err := GetRegisterEnabledWithDB(db)
	if err != nil {
		return SystemSettingDetail{}, err
	}

	detail := SystemSettingDetail{
		RegisterEnabled:        registerEnabled,
		EPayVersion:            DefaultEPayVersion,
		EPayGatewayURL:         getSystemSettingStringWithDefault(db, EPayGatewayURLSettingKey, ""),
		EPayPartnerID:          getSystemSettingStringWithDefault(db, EPayPartnerIDSettingKey, ""),
		EPayPlatformPublicKey:  getSystemSettingStringWithDefault(db, EPayPlatformPublicKeySettingKey, ""),
		EPayMerchantPrivateKey: getSystemSettingStringWithDefault(db, EPayMerchantPrivateKeySettingKey, ""),
		EPayNotifyURL:          getSystemSettingStringWithDefault(db, EPayNotifyURLSettingKey, ""),
		EPayReturnURL:          getSystemSettingStringWithDefault(db, EPayReturnURLSettingKey, ""),
		SMSProvider:            getSystemSettingStringWithDefault(db, SMSProviderSettingKey, DefaultSMSProvider),
		SMSBaoUsername:         getSystemSettingStringWithDefault(db, SMSBaoUsernameSettingKey, ""),
		SMSBaoSecret:           getSystemSettingStringWithDefault(db, SMSBaoSecretSettingKey, ""),
		SMSCodeSignName:        getSystemSettingStringWithDefault(db, SMSCodeSignNameSettingKey, ""),
		SMSCodeTemplate:        getSystemSettingStringWithDefault(db, SMSCodeTemplateSettingKey, DefaultSMSCodeTemplate),
		SMSLoginEnabled:        strings.EqualFold(strings.TrimSpace(getSystemSettingStringWithDefault(db, SMSLoginEnabledSettingKey, "false")), "true"),
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
	}

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
