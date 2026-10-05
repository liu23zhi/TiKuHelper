package service

import (
	"strings"

	"gorm.io/gorm"
)

const (
	// SecretMask is returned for configured write-only secrets.
	SecretMask = "********"
	// ClearSecret is an explicit update value for removing a configured secret.
	ClearSecret = "__CLEAR__"
)

func maskSecret(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return SecretMask
}

func mergeSecretInput(input string, current string) string {
	trimmed := strings.TrimSpace(input)
	switch trimmed {
	case ClearSecret:
		return ""
	case "", SecretMask:
		return current
	default:
		return trimmed
	}
}

func mergeStoredSecretInput(db *gorm.DB, key string, input string) string {
	current := getSystemSettingStringWithDefault(db, key, "")
	return mergeSecretInput(input, current)
}

// RedactSystemSettingDetail removes write-only secret values from API responses.
func RedactSystemSettingDetail(detail SystemSettingDetail) SystemSettingDetail {
	detail.EPayMerchantPrivateKey = maskSecret(detail.EPayMerchantPrivateKey)
	detail.SMSBaoSecret = maskSecret(detail.SMSBaoSecret)
	detail.WechatAPIBearerToken = maskSecret(detail.WechatAPIBearerToken)
	detail.CaptchaAPIKey = maskSecret(detail.CaptchaAPIKey)
	return detail
}
