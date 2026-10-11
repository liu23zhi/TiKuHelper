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
//
// 注意：UApiPro 密钥**不在此列**。系统设置页只有管理员能打开（与「默认账号配置」
// 的 token 一致），明文返回即可，由前端密码框隐藏显示；清空输入即为清空密钥。
func RedactSystemSettingDetail(detail SystemSettingDetail) SystemSettingDetail {
	detail.EPayMerchantPrivateKey = maskSecret(detail.EPayMerchantPrivateKey)
	detail.SMSBaoSecret = maskSecret(detail.SMSBaoSecret)
	detail.WechatAPIBearerToken = maskSecret(detail.WechatAPIBearerToken)
	detail.CaptchaAPIKey = maskSecret(detail.CaptchaAPIKey)
	return detail
}

// GetUAPIAPIKeyWithDB 读取 UApiPro 的 API 密钥。
//
// 归属地查询的唯一取值入口：未配置时返回空串，调用方据此跳过外呼（不消耗积分）。
func GetUAPIAPIKeyWithDB(db *gorm.DB) string {
	return strings.TrimSpace(getSystemSettingStringWithDefault(db, UAPIAPIKeySettingKey, ""))
}
