package service

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestScriptBearerLocatorChecksFullWindowBudgetAndRevokesOldAddress(t *testing.T) {
	db, owner, _, version := newManagedScriptFixture(t, 200)
	script, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "locator", VersionID: version.ID, LauncherOrigin: &ScriptLauncherOrigin{Host: "fixture.invalid", HTTPS: true}})
	if err != nil {
		t.Fatal(err)
	}
	old, _ := url.Parse(script.DownloadURL)
	view, err := ConfigureScriptLauncherAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, DurationMinutes: 15, MaxUses: 1})
	if err != nil {
		t.Fatal(err)
	}
	if view.Address == old.String() {
		t.Fatal("updating bearer authorization must rotate its UUID address")
	}
	if _, err := IssuePublicScriptExchangeTokenByLocatorWithDB(db, old.Host, old.Path, 0); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("old bearer locator survived rotation: %v", err)
	}
	address, _ := url.Parse(view.Address)
	grant, err := IssuePublicScriptExchangeTokenByLocatorWithDB(db, address.Host, address.Path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := IssuePublicScriptExchangeTokenByLocatorWithDB(db, address.Host, address.Path, 0); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("exhausted full grant issued a second launch: %v", err)
	}
	var stored entity.UserScript
	if err := db.First(&stored, script.ID).Error; err != nil || stored.LauncherUsedCount != 1 {
		t.Fatalf("unexpected launch budget: %v %+v", err, stored)
	}
	for _, test := range []struct {
		name         string
		starts, ends *time.Time
		disabled     bool
	}{
		{name: "scheduled", starts: timePtr(time.Now().Add(time.Hour))},
		{name: "expired", ends: timePtr(time.Now().Add(-time.Second))},
		{name: "revoked", disabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := db.Model(&entity.UserScript{}).Where("id = ?", script.ID).Updates(map[string]any{"launcher_starts_at": test.starts, "launcher_expires_at": test.ends, "launcher_disabled": test.disabled, "launcher_max_uses": 0}).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := IssuePublicScriptExchangeTokenByLocatorWithDB(db, address.Host, address.Path, 0); !errors.Is(err, ErrScriptNotFound) {
				t.Fatalf("inactive bearer entry admitted: %v", err)
			}
		})
	}
}

func TestScriptShortReconfigurationCannotReviveReplacedOrRevokedAddress(t *testing.T) {
	db, owner, _, script := shortAuthorizationFixture(t)
	input := ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "muioi.com/first", DurationMinutes: 5}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); err != nil {
		t.Fatalf("editing live address should retain it: %v", err)
	}
	input.Address = "muioi.com/second"
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); err != nil {
		t.Fatal(err)
	}
	input.Address = "muioi.com/first"
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); !errors.Is(err, ErrScriptShortAddressInUse) {
		t.Fatalf("replaced address was revived: %v", err)
	}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	input.Address = "muioi.com/second"
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); !errors.Is(err, ErrScriptShortAddressInUse) {
		t.Fatalf("revoked address was revived: %v", err)
	}
}

func TestScriptOldUUIDRequiresHeaderUntilOwnerRotates(t *testing.T) {
	db, owner, _, version := newManagedScriptFixture(t, 200)
	item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "upgraded", VersionID: version.ID, LauncherOrigin: &ScriptLauncherOrigin{Host: "fixture.invalid", HTTPS: true}})
	if err != nil {
		t.Fatal(err)
	}
	// The added column defaults to false for pre-upgrade UUID rows.
	if err := db.Model(&entity.UserScript{}).Where("id = ?", item.ID).UpdateColumn("launcher_bearer_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	address, _ := url.Parse(item.DownloadURL)
	if _, err := IssuePublicScriptExchangeTokenByLocatorWithDB(db, address.Host, address.Path, 0); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("old public UUID was silently enrolled: %v", err)
	}
	var legacy entity.UserScript
	if err := db.First(&legacy, item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(BuildScriptCommand(legacy), "powershell -EncodedCommand ") {
		t.Fatal("pre-upgrade UUID lost its protected compatibility command")
	}
	grant, err := IssuePublicScriptExchangeTokenWithDB(db, legacy.ShortCode, scriptLauncherCapability(legacy), 0)
	if err != nil {
		t.Fatalf("legacy header stopped working: %v", err)
	}
	if _, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token); err != nil {
		t.Fatal(err)
	}
	view, err := ConfigureScriptLauncherAuthorizationWithDB(db, owner, item.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, DurationMinutes: 10})
	if err != nil || view.Address == item.DownloadURL || !strings.HasPrefix(view.Command, "powershell -c ") {
		t.Fatalf("owner did not enroll a fresh address: %v", err)
	}
	if _, err := IssuePublicScriptExchangeTokenByLocatorWithDB(db, address.Host, address.Path, 0); !errors.Is(err, ErrScriptNotFound) {
		t.Fatal("old public UUID survived enrollment")
	}
	current, _ := url.Parse(view.Address)
	if _, err := IssuePublicScriptExchangeTokenByLocatorWithDB(db, current.Host, current.Path, 0); err != nil {
		t.Fatalf("new private UUID cannot launch: %v", err)
	}
}
