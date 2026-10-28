package service

import (
	"encoding/json"
	"net/url"
	"reflect"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func assertContextCompressionOrder(t *testing.T, value any, want []string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Order []string `json:"contextCompressionOrder"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Order, want) {
		t.Fatalf("compression order: got %v, want %v", got.Order, want)
	}
}

func TestAISearchContextCompressionOrderPersistsAndSurvivesLegacyUpdates(t *testing.T) {
	db := newSearchAccountConfigTestDB(t)
	initial, err := GetAISearchConfigWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	assertContextCompressionOrder(t, initial, []string{"server", "remote", "local"})
	var input UpdateAISearchConfigInput
	if err := json.Unmarshal([]byte(`{"contextCompressionOrder":["local","server","remote"]}`), &input); err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateAISearchConfigWithDB(db, input)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"local", "server", "remote"}
	assertContextCompressionOrder(t, updated, want)
	var setting entity.SystemSetting
	if err := db.Where("key = ?", "ai_context_compression_order").First(&setting).Error; err != nil {
		t.Fatal(err)
	}
	if setting.Value != `["local","server","remote"]` {
		t.Fatalf("unexpected saved order: %q", setting.Value)
	}
	legacyRemote := false
	legacy, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{Model: "legacy-client", UseRemoteContextCompression: &legacyRemote})
	if err != nil {
		t.Fatal(err)
	}
	assertContextCompressionOrder(t, legacy, want)
	resolved, err := BuildAISearchRequestConfigWithDB(db, url.Values{}, true)
	if err != nil {
		t.Fatal(err)
	}
	assertContextCompressionOrder(t, resolved.Config, want)
}

func TestAISearchContextCompressionOrderRejectsInvalidWithoutMutation(t *testing.T) {
	for _, raw := range []string{`[]`, `["remote","local"]`, `["server","remote","local","server"]`, `["server","server","local"]`, `["server","unknown","local"]`, `["SERVER","remote","local"]`} {
		t.Run(raw, func(t *testing.T) {
			db := newSearchAccountConfigTestDB(t)
			var input UpdateAISearchConfigInput
			if err := json.Unmarshal([]byte(`{"model":"must-not-persist","contextCompressionEnabled":false,"webReadPricePerCall":7,"contextCompressionOrder":`+raw+`}`), &input); err != nil {
				t.Fatal(err)
			}
			if _, err := UpdateAISearchConfigWithDB(db, input); err == nil {
				t.Fatal("expected invalid compression order to be rejected")
			}
			var configs, settings int64
			db.Model(&entity.AISearchConfig{}).Count(&configs)
			db.Model(&entity.SystemSetting{}).Count(&settings)
			if configs != 0 || settings != 0 {
				t.Fatalf("invalid order mutated configuration: configs=%d settings=%d", configs, settings)
			}
		})
	}
}

func TestAISearchContextCompressionOrderAcceptsEveryPermutation(t *testing.T) {
	for _, order := range [][]string{
		{"server", "remote", "local"}, {"server", "local", "remote"},
		{"remote", "server", "local"}, {"remote", "local", "server"},
		{"local", "server", "remote"}, {"local", "remote", "server"},
	} {
		t.Run(order[0]+"-"+order[1], func(t *testing.T) {
			db := newSearchAccountConfigTestDB(t)
			view, err := UpdateAISearchConfigWithDB(db, UpdateAISearchConfigInput{ContextCompressionOrder: &order})
			if err != nil {
				t.Fatal(err)
			}
			assertContextCompressionOrder(t, view, order)
		})
	}
}

func TestAISearchContextCompressionOrderInvalidStoredDataUsesDefaults(t *testing.T) {
	for _, value := range []string{`broken`, `null`, `[]`, `["server","remote"]`, `["server","server","local"]`, `["server","remote","unknown"]`} {
		t.Run(value, func(t *testing.T) {
			db := newSearchAccountConfigTestDB(t)
			if err := saveSystemSettingValue(db, "ai_context_compression_order", value, "test"); err != nil {
				t.Fatal(err)
			}
			view, err := GetAISearchConfigWithDB(db)
			if err != nil {
				t.Fatal(err)
			}
			assertContextCompressionOrder(t, view, []string{"server", "remote", "local"})
			var row entity.SystemSetting
			if err := db.Where("key = ?", "ai_context_compression_order").First(&row).Error; err != nil {
				t.Fatal(err)
			}
			if row.Value != value {
				t.Fatalf("read modified stored data: got %q, want %q", row.Value, value)
			}
		})
	}
}

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
