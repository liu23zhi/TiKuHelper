package service

import (
	"testing"

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
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
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
	if !settings.Enabled || !settings.Configured || settings.Preview != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=已配置" {
		t.Fatalf("unexpected settings: %+v", settings)
	}
	if len(settings.MentionedList) != 2 {
		t.Fatalf("expected deduplicated mentions: %+v", settings.MentionedList)
	}
	settings, err = UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{Enabled: &enabled, WebhookURL: SecretMask})
	if err != nil || !settings.Configured {
		t.Fatalf("masked update must preserve URL: %+v %v", settings, err)
	}
}

func TestTicketWecomSettingsRejectsNonWecomURL(t *testing.T) {
	db := newTicketSettingsTestDB(t)
	enabled := true
	if _, err := UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{Enabled: &enabled, WebhookURL: "http://example.com/hook"}); err == nil {
		t.Fatal("expected invalid webhook URL")
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
