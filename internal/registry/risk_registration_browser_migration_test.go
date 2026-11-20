package registry

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestRiskRegistrationBrowserMigrationUpgradesReadyDatabase(t *testing.T) {
	db := newRiskMigrationTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.RiskRule{}, &entity.RiskRuleAudit{}, &entity.RiskWarning{}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"risk_rules_schema_v2", "risk_warnings_schema_v1"} {
		if err := setSystemSettingFlag(db, key); err != nil {
			t.Fatal(err)
		}
	}
	legacy := entity.RiskRule{RuleID: "register_burst", Type: "register_burst", Name: "已有网段规则", Threshold: 9}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := EnsureRiskSchema(db); err != nil {
			t.Fatal(err)
		}
	}
	if !db.Migrator().HasTable("registration_browser_events") {
		t.Fatal("already-ready installation did not acquire the new event table")
	}
	if !db.Migrator().HasIndex(&entity.RegistrationBrowserEvent{}, "idx_registration_browser_time") {
		t.Fatal("missing time/retention index")
	}
	var added entity.RiskRule
	if err := db.Where("rule_id = ?", "device_register_burst").First(&added).Error; err != nil {
		t.Fatal(err)
	}
	if !added.Enabled || added.Type != "device_register_burst" || added.Threshold != 3 || added.ThrottleSeconds != 21600 {
		t.Fatalf("unexpected new rule: %+v", added)
	}
	if err := db.First(&legacy, legacy.ID).Error; err != nil || legacy.Threshold != 9 || legacy.Name != "已有网段规则" {
		t.Fatalf("legacy rule changed: %+v, %v", legacy, err)
	}
}
