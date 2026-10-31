package service

import (
	"errors"
	"reflect"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestTicketWecomSettingsLifecycleKeepsUnrelatedSettings(t *testing.T) {
	db := newTicketSettingsTestDB(t)
	untouched := entity.SystemSetting{Key: "site_title", Value: "existing site", Remark: "keep"}
	if err := db.Create(&untouched).Error; err != nil {
		t.Fatal(err)
	}
	enabled, disabled := true, false
	const webhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=lifecycle-test"
	settings, err := UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{Enabled: &enabled, WebhookURL: webhook})
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled || !settings.Configured || len(settings.MentionedList) != 0 {
		t.Fatalf("unexpected first save without members: %+v", settings)
	}
	for i := 0; i < 2; i++ {
		settings, err = UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{
			Enabled: &disabled, MentionedList: []string{"alice", " alice ", "bob"},
		})
		if err != nil {
			t.Fatal(err)
		}
		config, err := GetTicketWecomConfigWithDB(db)
		if err != nil {
			t.Fatal(err)
		}
		if settings.Enabled || !settings.Configured || config.WebhookURL != webhook || !reflect.DeepEqual(config.MentionedList, []string{"alice", "bob"}) {
			t.Fatalf("disable/repeated save changed preserved URL or members: settings=%+v config=%+v", settings, config)
		}
	}
	settings, err = UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{
		Enabled: &disabled, ClearWebhook: true, MentionedList: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Enabled || settings.Configured || settings.Preview != "" || len(settings.MentionedList) != 0 {
		t.Fatalf("explicit clear did not persist: %+v", settings)
	}
	var rows []entity.SystemSetting
	if err := db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || rows[0].ID != untouched.ID || rows[0].Key != untouched.Key || rows[0].Value != untouched.Value || rows[0].Remark != untouched.Remark || !rows[0].CreatedAt.Equal(untouched.CreatedAt) || !rows[0].UpdatedAt.Equal(untouched.UpdatedAt) {
		t.Fatalf("saving ticket settings must preserve other settings and avoid duplicate rows: %+v", rows)
	}
}

func TestTicketWecomSettingsRollsBackAllKeysOnWriteFailure(t *testing.T) {
	db := newTicketSettingsTestDB(t)
	enabled, disabled := true, false
	const initialWebhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=before-test"
	if _, err := UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{
		Enabled: &enabled, WebhookURL: initialWebhook, MentionedList: []string{"before"},
	}); err != nil {
		t.Fatal(err)
	}
	writeFailure := errors.New("simulated settings write failure")
	const callback = "test:reject_ticket_members_update"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if setting, ok := tx.Statement.Dest.(*entity.SystemSetting); ok && setting.Key == TicketWecomMentionedListSettingKey {
			tx.AddError(writeFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	_, err := UpdateTicketWecomSettingsWithDB(db, UpdateTicketWecomSettingsInput{
		Enabled: &disabled, WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=after-test", MentionedList: []string{"after"},
	})
	if !errors.Is(err, writeFailure) {
		t.Fatalf("expected simulated error, got %v", err)
	}
	config, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Enabled || config.WebhookURL != initialWebhook || !reflect.DeepEqual(config.MentionedList, []string{"before"}) {
		t.Fatalf("partial settings writes escaped transaction rollback: %+v", config)
	}
}
