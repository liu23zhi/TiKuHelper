package service

import (
	"errors"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestScriptDomainSettingsNormalizeAndValidate(t *testing.T) {
	db := newScriptTestDB(t)
	settings, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{
		Domains:       []string{" Example.COM ", "example.com", "foo.example.net"},
		AllowWildcard: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Domains) != 2 || settings.Domains[0] != "example.com" || settings.Domains[1] != "foo.example.net" || settings.AllowWildcard {
		t.Fatalf("unexpected normalized settings: %+v", settings)
	}
	if err := ValidateScriptDomainWithDB(db, "https://example.com", false); err != nil {
		t.Fatalf("allowed domain rejected: %v", err)
	}
	if !errors.Is(ValidateScriptDomainWithDB(db, "example.com", true), ErrScriptWildcardDisabled) {
		t.Fatal("wildcard domain should be rejected when disabled")
	}
	if !errors.Is(ValidateScriptDomainWithDB(db, "other.com", false), ErrScriptDomainUnavailable) {
		t.Fatal("unlisted domain should be rejected")
	}
	if err := db.Create(&entity.SystemSetting{Key: "unrelated", Value: "keep"}).Error; err != nil {
		t.Fatal(err)
	}
	var preserved entity.SystemSetting
	if err := db.Where("key = ?", "unrelated").First(&preserved).Error; err != nil || preserved.Value != "keep" {
		t.Fatalf("unrelated settings were not preserved: %+v err=%v", preserved, err)
	}
}

func TestScriptDomainSettingsKeepWildcardDefaultWhenLegacyJSONOmitsFlag(t *testing.T) {
	db := newScriptTestDB(t)
	if err := db.Create(&entity.SystemSetting{Key: ScriptDomainSettingsKey, Value: `{"domains":["example.com"]}`}).Error; err != nil {
		t.Fatal(err)
	}
	settings, err := GetScriptDomainSettingsWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.AllowWildcard || len(settings.Domains) != 1 || settings.Domains[0] != "example.com" {
		t.Fatalf("legacy settings should keep wildcard default: %+v", settings)
	}
}
