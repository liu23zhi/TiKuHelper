package service

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newTicketSettingsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.TicketNotification{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestTicketWecomSettingsUpdateRedactsAndPreservesWebhook(t *testing.T) {
	db := newTicketSettingsTestDB(t)
	enabled := true
	settings, err := UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{
		Enabled:       &enabled,
		WebhookURL:    "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret",
		MentionedList: []string{"alice", "alice", "bob"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled || !settings.Configured || settings.WebhookURL != "" || settings.Preview != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=已配置" {
		t.Fatalf("unexpected settings: %+v", settings)
	}
	if len(settings.MentionedList) != 2 {
		t.Fatalf("expected deduplicated mentions: %+v", settings.MentionedList)
	}
	settings, err = UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{Enabled: &enabled, WebhookURL: SecretMask})
	if err != nil || !settings.Configured {
		t.Fatalf("masked update must preserve URL: %+v %v", settings, err)
	}
	if settings.WebhookURL != "" {
		t.Fatal("masked update returned a stored credential")
	}
	settings, err = UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{Enabled: &enabled})
	if err != nil || !settings.Configured || settings.WebhookURL != "" {
		t.Fatalf("blank update must preserve without exposing URL: configured=%t error=%v", settings.Configured, err)
	}
	config, err := GetTicketWecomConfigWithDB(db)
	if err != nil || config.WebhookURL != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret" {
		t.Fatal("mask/blank update changed the stored webhook")
	}
	disabled := false
	settings, err = UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{Enabled: &disabled, ClearWebhook: true})
	if err != nil || settings.Configured || settings.WebhookURL != "" {
		t.Fatal("explicit clear did not remove the stored webhook")
	}
}

func TestTicketWecomSettingsRejectsNonWecomURL(t *testing.T) {
	db := newTicketSettingsTestDB(t)
	enabled := true
	if _, err := UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{Enabled: &enabled, WebhookURL: "http://example.com/hook"}); err == nil {
		t.Fatal("expected invalid webhook URL")
	}
}

func TestTicketWecomSettingsDisableSkipsPendingNotificationsAtomically(t *testing.T) {
	db := newTicketSettingsTestDB(t)
	enabled := true
	if _, err := UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{
		Enabled: &enabled, WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	notice := entity.TicketNotification{TicketID: 1, EventType: "new_ticket", Payload: "pending", Status: ticketNotificationPending, NextAttemptAt: &now}
	if err := db.Create(&notice).Error; err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	var stored entity.TicketNotification
	if err := db.First(&stored, notice.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != ticketNotificationSkipped || stored.NextAttemptAt != nil || !strings.Contains(stored.LastError, "未启用") {
		t.Fatalf("pending notice was not skipped: %+v", stored)
	}
}

func TestTicketWecomWebhookRejectsFragments(t *testing.T) {
	for _, value := range []string{
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret#private-fragment",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret#",
	} {
		if err := validateTicketWecomWebhookURL(value); err == nil {
			t.Fatalf("accepted webhook fragment: %s", value)
		}
	}
}

func TestTicketWebhookPreviewOmitsHistoricalSensitiveURLParts(t *testing.T) {
	for _, value := range []string{
		"https://private-user:private-password@qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret&token=extra-secret#private-fragment",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send#private-fragment",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send",
	} {
		if actual := maskTicketWebhookURL(value); actual != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=已配置" {
			t.Fatalf("unsafe preview %q for %s", actual, value)
		}
	}
}
