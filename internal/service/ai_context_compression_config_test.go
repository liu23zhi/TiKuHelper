package service

import (
	"net/url"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestAISearchContextCompressionDefaultsAndPartialUpdates(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	initial, err := GetAISearchConfigWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if !initial.ContextCompressionEnabled || initial.UseRemoteContextCompression || initial.ContextCompressionTriggerPercent != 80 || initial.ContextCompressionTargetPercent != 50 || initial.ContextCompressionMaxAttempts != 3 {
		t.Fatalf("unexpected compression defaults: %+v", initial)
	}
	disabled, remote, trigger, target, attempts := false, true, 85, 45, 40
	updated, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
		ContextCompressionEnabled: &disabled, UseRemoteContextCompression: &remote,
		ContextCompressionTriggerPercent: &trigger, ContextCompressionTargetPercent: &target, ContextCompressionMaxAttempts: &attempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ContextCompressionEnabled || !updated.UseRemoteContextCompression || updated.ContextCompressionTriggerPercent != trigger || updated.ContextCompressionTargetPercent != target || updated.ContextCompressionMaxAttempts != attempts {
		t.Fatalf("compression settings were not saved: %+v", updated)
	}
	legacy, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Model: "legacy-client"})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.ContextCompressionEnabled || !legacy.UseRemoteContextCompression || legacy.ContextCompressionTriggerPercent != trigger || legacy.ContextCompressionTargetPercent != target || legacy.ContextCompressionMaxAttempts != attempts {
		t.Fatalf("legacy update overwrote compression settings: %+v", legacy)
	}
	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Config.ContextCompressionEnabled || !resolved.Config.UseRemoteContextCompression || resolved.Config.ContextCompressionTriggerPercent != trigger || resolved.Config.ContextCompressionTargetPercent != target || resolved.Config.ContextCompressionMaxAttempts != attempts {
		t.Fatalf("request settings do not match saved settings: %+v", resolved.Config)
	}
	var count int64
	db.Model(&entity.SystemSetting{}).Where("key IN ?", []string{AIContextCompressionEnabledSettingKey, AIUseRemoteContextCompressionSettingKey, AIContextCompressionTriggerPercentSettingKey, AIContextCompressionTargetPercentSettingKey, AIContextCompressionMaxAttemptsSettingKey}).Count(&count)
	if count != 5 {
		t.Fatalf("expected five persisted compression settings, got %d", count)
	}
}

func TestAISearchContextCompressionRejectsInvalidSettingsWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name                      string
		trigger, target, attempts int
	}{
		{"trigger-low", 49, 20, 3}, {"trigger-high", 96, 50, 3},
		{"target-low", 80, 9, 3}, {"target-equal", 80, 80, 3}, {"target-high", 80, 81, 3},
		{"attempts-low", 80, 50, 0}, {"attempts-negative", 80, 50, -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newSearchAccountConfigTestDB(t)
			_, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{
				Model: "must-not-persist", ContextCompressionTriggerPercent: &test.trigger,
				ContextCompressionTargetPercent: &test.target, ContextCompressionMaxAttempts: &test.attempts,
			})
			if err == nil {
				t.Fatal("expected compression validation error")
			}
			var count int64
			db.Model(&entity.AISearchConfig{}).Count(&count)
			if count != 0 {
				t.Fatalf("invalid compression input changed config: %d", count)
			}
		})
	}
}
