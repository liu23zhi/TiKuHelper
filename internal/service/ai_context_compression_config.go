package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"gorm.io/gorm"
)

var ErrAIContextCompressionSettingsInvalid = errors.New("上下文压缩配置无效")

const (
	AIContextCompressionEnabledSettingKey        = "ai_context_compression_enabled"
	AIUseRemoteContextCompressionSettingKey      = "ai_use_remote_context_compression"
	AIContextCompressionTriggerPercentSettingKey = "ai_context_compression_trigger_percent"
	AIContextCompressionTargetPercentSettingKey  = "ai_context_compression_target_percent"
	AIContextCompressionMaxAttemptsSettingKey    = "ai_context_compression_max_attempts"
	AIContextCompressionOrderSettingKey          = "ai_context_compression_order"
)

type AIContextCompressionSettings struct {
	Enabled        bool
	UseRemote      bool
	TriggerPercent int
	TargetPercent  int
	MaxAttempts    int
	Order          []string
}

func defaultAIContextCompressionOrder() []string {
	return []string{"server", "remote", "local"}
}

func validAIContextCompressionOrder(order []string) bool {
	if len(order) != 3 {
		return false
	}
	seen := make(map[string]bool, 3)
	for _, strategy := range order {
		if (strategy != "server" && strategy != "remote" && strategy != "local") || seen[strategy] {
			return false
		}
		seen[strategy] = true
	}
	return true
}

func readAIContextCompressionOrder(db *gorm.DB) []string {
	var order []string
	value := getSystemSettingStringWithDefault(db, AIContextCompressionOrderSettingKey, "")
	if json.Unmarshal([]byte(value), &order) != nil || !validAIContextCompressionOrder(order) {
		return defaultAIContextCompressionOrder()
	}
	return order
}

func GetAIContextCompressionSettingsWithDB(db *gorm.DB) AIContextCompressionSettings {
	readBool := func(key string, fallback bool) bool {
		value, err := strconv.ParseBool(getSystemSettingStringWithDefault(db, key, strconv.FormatBool(fallback)))
		if err != nil {
			return fallback
		}
		return value
	}
	readInt := func(key string, fallback, minimum, maximum int) int {
		value, err := strconv.Atoi(getSystemSettingStringWithDefault(db, key, strconv.Itoa(fallback)))
		if err != nil || value < minimum || value > maximum {
			return fallback
		}
		return value
	}
	trigger := readInt(AIContextCompressionTriggerPercentSettingKey, 80, 50, 95)
	target := readInt(AIContextCompressionTargetPercentSettingKey, 50, 10, 94)
	if target >= trigger {
		target = min(50, trigger-1)
	}
	return AIContextCompressionSettings{
		Enabled:        readBool(AIContextCompressionEnabledSettingKey, true),
		UseRemote:      readBool(AIUseRemoteContextCompressionSettingKey, false),
		TriggerPercent: trigger,
		TargetPercent:  target,
		MaxAttempts:    getPositiveIntSystemSetting(db, AIContextCompressionMaxAttemptsSettingKey, 3),
		Order:          readAIContextCompressionOrder(db),
	}
}

func mergeAIContextCompressionSettings(db *gorm.DB, input UpdateAISearchConfigInput) (AIContextCompressionSettings, error) {
	settings := GetAIContextCompressionSettingsWithDB(db)
	if input.ContextCompressionEnabled != nil {
		settings.Enabled = *input.ContextCompressionEnabled
	}
	if input.UseRemoteContextCompression != nil {
		settings.UseRemote = *input.UseRemoteContextCompression
	}
	if input.ContextCompressionTriggerPercent != nil {
		settings.TriggerPercent = *input.ContextCompressionTriggerPercent
	}
	if input.ContextCompressionTargetPercent != nil {
		settings.TargetPercent = *input.ContextCompressionTargetPercent
	}
	if input.ContextCompressionMaxAttempts != nil {
		settings.MaxAttempts = *input.ContextCompressionMaxAttempts
	}
	if input.ContextCompressionOrder != nil {
		settings.Order = append([]string(nil), (*input.ContextCompressionOrder)...)
	}
	if !validAIContextCompressionOrder(settings.Order) {
		return settings, fmt.Errorf("%w: 压缩顺序必须完整包含 server、remote、local，且不能重复", ErrAIContextCompressionSettingsInvalid)
	}
	if settings.TriggerPercent < 50 || settings.TriggerPercent > 95 {
		return settings, fmt.Errorf("%w: 触发比例必须是 50～95 的整数", ErrAIContextCompressionSettingsInvalid)
	}
	if settings.TargetPercent < 10 || settings.TargetPercent >= settings.TriggerPercent {
		return settings, fmt.Errorf("%w: 目标比例必须至少为 10，且小于触发比例", ErrAIContextCompressionSettingsInvalid)
	}
	if !isValidAISearchCount(settings.MaxAttempts) {
		return settings, fmt.Errorf("%w: 最大尝试次数必须是可精确表示的正整数（含首次）", ErrAIContextCompressionSettingsInvalid)
	}
	return settings, nil
}

func saveAIContextCompressionSettingsWithDB(db *gorm.DB, input UpdateAISearchConfigInput) error {
	if input.ContextCompressionEnabled == nil && input.UseRemoteContextCompression == nil && input.ContextCompressionTriggerPercent == nil && input.ContextCompressionTargetPercent == nil && input.ContextCompressionMaxAttempts == nil && input.ContextCompressionOrder == nil {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		settings, err := mergeAIContextCompressionSettings(tx, input)
		if err != nil {
			return err
		}
		orderJSON, err := json.Marshal(settings.Order)
		if err != nil {
			return err
		}
		updates := []struct {
			provided           bool
			key, value, remark string
		}{
			{input.ContextCompressionEnabled != nil, AIContextCompressionEnabledSettingKey, strconv.FormatBool(settings.Enabled), "启用 AI 上下文自动压缩"},
			{input.UseRemoteContextCompression != nil, AIUseRemoteContextCompressionSettingKey, strconv.FormatBool(settings.UseRemote), "使用上游上下文压缩接口"},
			{input.ContextCompressionTriggerPercent != nil, AIContextCompressionTriggerPercentSettingKey, strconv.Itoa(settings.TriggerPercent), "AI 上下文压缩触发比例"},
			{input.ContextCompressionTargetPercent != nil, AIContextCompressionTargetPercentSettingKey, strconv.Itoa(settings.TargetPercent), "AI 上下文压缩目标比例"},
			{input.ContextCompressionMaxAttempts != nil, AIContextCompressionMaxAttemptsSettingKey, strconv.Itoa(settings.MaxAttempts), "AI 上下文压缩最大尝试次数（含首次）"},
			{input.ContextCompressionOrder != nil, AIContextCompressionOrderSettingKey, string(orderJSON), "AI 上下文压缩自动回退顺序"},
		}
		for _, update := range updates {
			if update.provided {
				if err := saveSystemSettingValue(tx, update.key, update.value, update.remark); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
