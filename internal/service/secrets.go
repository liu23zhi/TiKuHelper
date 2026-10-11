package service

import (
	"strings"

	"gorm.io/gorm"
)

const (
	// SecretMask 是历史遗留的脱敏占位符。
	//
	// 系统设置读接口**已不再**返回它（改为明文）；保留常量有两个用途：
	//   - 写入路径把它当作「保持原值」，兼容可能仍回填旧值的客户端；
	//   - AI 搜索配置用它表示「不修改该条配置的密钥」。
	SecretMask = "********"
	// ClearSecret 是显式的「清空密钥」更新值。
	ClearSecret = "__CLEAR__"
)

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

// RedactSystemSettingDetail 现在是**恒等函数**：系统设置接口不再脱敏任何密钥。
//
// 为什么去掉脱敏：系统设置页只有管理员能打开（与「默认账号配置」的 token 一致），
// 拿到 `********` 反而让管理员无法核对已配置的值。前端统一用 `a-input-password`
// 隐藏显示，需要时点「眼睛」即可查看，因此明文返回不增加暴露面。
//
// 保留函数名与调用点，是为了让"响应是否脱敏"这件事有一个**集中的开关位置** ——
// 将来若要把系统设置暴露给非管理员，只改这里即可，不必翻遍控制器。
func RedactSystemSettingDetail(detail SystemSettingDetail) SystemSettingDetail {
	return detail
}

// GetUAPIAPIKeyWithDB 读取 UApiPro 的 API 密钥。
//
// 归属地查询的唯一取值入口：未配置时返回空串，调用方据此跳过外呼（不消耗积分）。
func GetUAPIAPIKeyWithDB(db *gorm.DB) string {
	return strings.TrimSpace(getSystemSettingStringWithDefault(db, UAPIAPIKeySettingKey, ""))
}
