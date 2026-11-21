package service

import (
	"errors"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestShortAuthorizationGeneratesFreshAddressFromAllowedFormat(t *testing.T) {
	db, owner, _, script := shortAuthorizationFixture(t)
	patterns := map[string]*regexp.Regexp{
		"*.muioi.com": regexp.MustCompile(`^https://[a-z0-9-]{25,63}\.muioi\.com$`),
		"muioi.com/*": regexp.MustCompile(`^https://muioi\.com/[a-z0-9-]{25,63}$`),
	}
	for format, pattern := range patterns {
		input := ConfigureScriptShortAuthorizationInput{Enabled: true, DurationMinutes: 5, DomainFormat: format, RequestHost: "muioi.com"}
		first, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input)
		if err != nil || !pattern.MatchString(first.Address) {
			t.Fatalf("generated address for %s: %+v, %v", format, first, err)
		}
		second, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input)
		if err != nil || !pattern.MatchString(second.Address) || first.Address == second.Address {
			t.Fatalf("renewed address for %s: %+v, %v (previous %s)", format, second, err, first.Address)
		}
		previous, _ := url.Parse(first.Address)
		if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, previous.Host, previous.Path); !errors.Is(err, ErrScriptShortAuthorizationInactive) {
			t.Fatalf("previous address still active: %v", err)
		}
	}
}

func TestShortAuthorizationRetainsAddressesAfterExpiryRevocationAndReplacement(t *testing.T) {
	db, owner, _, first := shortAuthorizationFixture(t)
	second, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "second", VersionID: first.VersionID, BaseDomain: "b.muioi.com"})
	if err != nil {
		t.Fatal(err)
	}
	input := ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "muioi.com/kept", DurationMinutes: 5}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, first.ID, input); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.UserScript{}).Where("id = ?", first.ID).Update("short_entry_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	assertOccupied := func() {
		t.Helper()
		if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, second.ID, input); !errors.Is(err, ErrScriptShortAddressInUse) {
			t.Fatalf("old address reassigned to another script: %v", err)
		}
	}
	assertOccupied()
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, first.ID, ConfigureScriptShortAuthorizationInput{}); err != nil {
		t.Fatal(err)
	}
	assertOccupied()
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, first.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "muioi.com/new", DurationMinutes: 5}); err != nil {
		t.Fatal(err)
	}
	assertOccupied()
}

func TestShortAuthorizationRetainsHistoricalExchangeAddress(t *testing.T) {
	db, owner, _, first := shortAuthorizationFixture(t)
	second, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "second", VersionID: first.VersionID, BaseDomain: "b.muioi.com"})
	if err != nil {
		t.Fatal(err)
	}
	const previous = "https://muioi.com/previous"
	for i, script := range []ScriptListItem{first, second} {
		address := previous
		if i == 1 {
			address = "https://muioi.com/ambiguous"
		}
		token := entity.ScriptExchangeToken{ScriptID: script.ID, VersionID: script.VersionID, TokenHash: hashScriptSecret(address), LaunchAddress: address, AuthorizationKind: "short", ExpiresAt: time.Now().Add(-time.Minute)}
		if err := db.Create(&token).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&entity.UserScript{}).Where("id = ?", first.ID).Updates(map[string]any{
		"short_entry_address":     "https://muioi.com/ambiguous",
		"short_entry_address_key": shortEntryAddressKey("muioi.com", "/ambiguous"),
		"short_entry_expires_at":  time.Now().Add(time.Minute),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := BackfillScriptAddressReservationsWithDB(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Where("launch_address <> ''").Delete(&entity.ScriptExchangeToken{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{previous, "https://muioi.com/ambiguous"} {
		if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, second.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: address, DurationMinutes: 5}); !errors.Is(err, ErrScriptShortAddressInUse) {
			t.Fatalf("historical address %s reassigned: %v", address, err)
		}
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/ambiguous"); !errors.Is(err, ErrScriptShortAuthorizationInactive) {
		t.Fatalf("ambiguous legacy address remained active: %v", err)
	}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, first.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "https://muioi.com/ambiguous", DurationMinutes: 5}); !errors.Is(err, ErrScriptShortAddressInUse) {
		t.Fatalf("ambiguous legacy address returned to one of its owners: %v", err)
	}
}

func TestShortAuthorizationCannotReuseDisabledLegacyLauncherAddress(t *testing.T) {
	db, owner, _, script := shortAuthorizationFixture(t)
	if _, err := ConfigureScriptLauncherAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: script.DownloadURL, DurationMinutes: 5}); !errors.Is(err, ErrScriptShortAddressInUse) {
		t.Fatalf("short command took legacy long address: %v", err)
	}
}

func TestLegacyLauncherCannotMoveToPreviouslyRevokedShortAddress(t *testing.T) {
	db, owner, _, first := shortAuthorizationFixture(t)
	second, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "second", VersionID: first.VersionID, BaseDomain: "b.muioi.com"})
	if err != nil {
		t.Fatal(err)
	}
	address := second.ShortCode + ".new.muioi.com"
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, first.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: address, DurationMinutes: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, first.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "different.muioi.com", DurationMinutes: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateUserScriptWithDB(db, owner, second.ID, UpdateUserScriptInput{BaseDomain: "new.muioi.com"}); !errors.Is(err, ErrScriptShortAddressInUse) {
		t.Fatalf("legacy launcher took a retired short address: %v", err)
	}
	var stored entity.UserScript
	if err := db.First(&stored, second.ID).Error; err != nil || stored.BaseDomain != "b.muioi.com" {
		t.Fatalf("failed move changed the launcher: %q, %v", stored.BaseDomain, err)
	}
}

func TestScriptCreateSetsTwentyFourHourLauncherWhenPermanentFullIsForbidden(t *testing.T) {
	db, owner, _, first := shortAuthorizationFixture(t)
	allowed := false
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{AllowFullNeverExpire: &allowed}); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	second, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "second", VersionID: first.VersionID, BaseDomain: "b.muioi.com"})
	view := second.LauncherAuthorization
	if err != nil || view == nil || view.Status != "active" || view.Command == "" || view.ExpiresAt == nil {
		t.Fatalf("new script lacks a finite full launcher: %+v, %v", view, err)
	}
	if view.ExpiresAt.Before(before.Add(24*time.Hour)) || view.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
		t.Fatalf("default full launcher must expire after 24h: %v", view.ExpiresAt)
	}
	if _, err := IssuePublicScriptExchangeTokenWithDB(db, second.ShortCode, scriptLauncherCapability(second.UserScript), 0); err != nil {
		t.Fatalf("finite full launcher could not issue a ticket: %v", err)
	}
}

func TestScriptDomainSettingsConcurrentDomainsUpdatePreservesPolicy(t *testing.T) {
	db, _, _, _ := shortAuthorizationFixture(t)
	full, short := false, true
	const callback = "test:concurrent_script_policy"
	if err := db.Callback().Update().Before("gorm:begin_transaction").Register(callback, func(statement *gorm.DB) {
		if statement.Statement.Table != entity.TableNameSystemSetting {
			return
		}
		if err := db.Callback().Update().Remove(callback); err != nil {
			t.Fatal(err)
		}
		if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{AllowFullNeverExpire: &full, AllowShortNeverExpire: &short}); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	result, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"elsewhere.test/*"}})
	if err != nil || len(result.Domains) != 1 || result.Domains[0] != "elsewhere.test/*" || *result.AllowFullNeverExpire || !*result.AllowShortNeverExpire {
		t.Fatalf("domains update lost a concurrent policy edit: %+v, %v", result, err)
	}
}

func TestScriptAuthorizationNeverExpiryPolicyChangesOnlyFutureWrites(t *testing.T) {
	db, owner, _, script := shortAuthorizationFixture(t)
	input := ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "muioi.com/lasting", MaxUses: 1}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); !errors.Is(err, ErrScriptShortDurationInvalid) {
		t.Fatalf("default allowed a permanent short command: %v", err)
	}
	if view, err := ConfigureScriptLauncherAuthorizationWithDB(db, owner, script.ID, input); err != nil || view.ExpiresAt != nil {
		t.Fatalf("default denied permanent full command: %+v, %v", view, err)
	}
	value := `{"domains":["*.muioi.com","muioi.com/*"],"allowFullNeverExpire":false,"allowShortNeverExpire":true}`
	if err := db.Model(&entity.SystemSetting{}).Where("key = ?", ScriptDomainSettingsKey).Update("value", value).Error; err != nil {
		t.Fatal(err)
	}
	var saved entity.UserScript
	if err := db.First(&saved, script.ID).Error; err != nil {
		t.Fatal(err)
	}
	if scriptLauncherAuthorizationView(saved).Status != "active" || saved.LauncherExpiresAt != nil {
		t.Fatalf("policy changed existing full command: %+v", scriptLauncherAuthorizationView(saved))
	}
	if _, err := ConfigureScriptLauncherAuthorizationWithDB(db, owner, script.ID, input); err == nil {
		t.Fatal("policy allowed updating a permanent full command")
	}
	if view, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); err != nil || view.ExpiresAt != nil {
		t.Fatalf("policy denied permanent short command: %+v, %v", view, err)
	}
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"*.muioi.com", "muioi.com/*"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureScriptLauncherAuthorizationWithDB(db, owner, script.ID, input); err == nil {
		t.Fatal("domains-only update cleared the separate expiry policies")
	}
	_, grant, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/lasting")
	if err != nil {
		t.Fatalf("permanent short command could not be used: %v", err)
	}
	if _, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token); err != nil {
		t.Fatalf("permanent short ticket could not be exchanged: %v", err)
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/lasting"); !errors.Is(err, ErrScriptShortAuthorizationInactive) {
		t.Fatalf("permanent short command exceeded its use limit: %v", err)
	}
}
