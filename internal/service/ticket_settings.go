package service

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
)

type TicketWecomSettings struct {
	Enabled       bool     `json:"enabled"`
	Configured    bool     `json:"configured"`
	WebhookURL    string   `json:"webhookUrl"`
	Preview       string   `json:"preview"`
	MentionedList []string `json:"mentionedList"`
}

type UpdateTicketWecomSettingsInput struct {
	Enabled       *bool    `json:"enabled"`
	WebhookURL    string   `json:"webhookUrl"`
	ClearWebhook  bool     `json:"clearWebhook"`
	MentionedList []string `json:"mentionedList"`
}

var ErrInvalidTicketWecomWebhook = errors.New("企业微信工单 Webhook 必须是 HTTPS 群机器人地址")

func GetTicketWecomSettingsWithDB(db *gorm.DB) (TicketWecomSettings, error) {
	config, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		return TicketWecomSettings{}, err
	}
	return TicketWecomSettings{
		Enabled: config.Enabled, Configured: strings.TrimSpace(config.WebhookURL) != "",
		// Never return the credential-bearing URL; blank updates retain it.
		WebhookURL: "",
		Preview:    maskTicketWebhookURL(config.WebhookURL), MentionedList: config.MentionedList,
	}, nil
}

func UpdateTicketWecomSettingsWithDB(db *gorm.DB, input UpdateTicketWecomSettingsInput) (TicketWecomSettings, error) {
	if db == nil {
		return TicketWecomSettings{}, errors.New("数据库未初始化")
	}
	config, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		return TicketWecomSettings{}, err
	}
	webhook := config.WebhookURL
	switch {
	case input.ClearWebhook:
		webhook = ""
	case strings.TrimSpace(input.WebhookURL) != "" && input.WebhookURL != SecretMask:
		webhook = strings.TrimSpace(input.WebhookURL)
	}
	enabled := config.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if enabled {
		if err := validateTicketWecomWebhookURL(webhook); err != nil {
			return TicketWecomSettings{}, err
		}
	}
	mentioned := config.MentionedList
	if input.MentionedList != nil {
		mentioned = normalizeTicketMentionedList(input.MentionedList)
	}
	encoded, err := json.Marshal(mentioned)
	if err != nil {
		return TicketWecomSettings{}, err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		settings := []struct{ key, value, remark string }{
			{TicketWecomEnabledSettingKey, map[bool]string{true: "true", false: "false"}[enabled], "是否启用企业微信工单通知"},
			{TicketWecomWebhookURLSettingKey, webhook, "企业微信工单群机器人 Webhook"},
			{TicketWecomMentionedListSettingKey, string(encoded), "企业微信工单通知 @成员 ID"},
		}
		for _, item := range settings {
			if err := saveSystemSettingValue(tx, item.key, item.value, item.remark); err != nil {
				return err
			}
		}
		// Disabling or clearing the integration must make existing pending
		// outbox rows terminal in the same transaction as the configuration
		// change. They must never be replayed automatically after re-enabling.
		if configErr := validateTicketWecomConfig(TicketWecomConfig{Enabled: enabled, WebhookURL: webhook}); configErr != nil {
			_, err := skipPendingTicketNotificationsInTx(tx, configErr.Error(), time.Now())
			return err
		}
		return nil
	})
	if err != nil {
		return TicketWecomSettings{}, err
	}
	return GetTicketWecomSettingsWithDB(db)
}

func validateTicketWecomWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return ErrInvalidTicketWecomWebhook
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if !strings.EqualFold(parsed.Scheme, "https") || host != "qyapi.weixin.qq.com" || parsed.User != nil || (parsed.Port() != "" && parsed.Port() != "443") || parsed.Path != "/cgi-bin/webhook/send" || strings.TrimSpace(parsed.Query().Get("key")) == "" || strings.Contains(raw, "#") {
		return ErrInvalidTicketWecomWebhook
	}
	return nil
}

func maskTicketWebhookURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return ""
	}
	preview := url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: parsed.Path, RawQuery: "key=已配置"}
	return preview.String()
}

func normalizeTicketMentionedList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len([]rune(value)) > 64 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func TicketWebhookErrorMessage(err error, webhookURL string) string {
	if err == nil {
		return ""
	}
	return redactTicketWebhookError(err.Error(), webhookURL)
}
