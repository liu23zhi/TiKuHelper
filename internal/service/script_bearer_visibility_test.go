package service

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestScriptBearerAddressIsAbsentFromOtherOwnerAndShortExchangeResponses(t *testing.T) {
	db, owner, admin, version := newManagedScriptFixture(t, 200)
	_, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"fixture.invalid/*"}})
	if err != nil {
		t.Fatal(err)
	}
	script, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "private URL", VersionID: version.ID, LauncherOrigin: &ScriptLauncherOrigin{Host: "fixture.invalid", HTTPS: true}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := ListUserScriptsWithDB(db, admin, true)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivate := func(value any) {
		t.Helper()
		wire, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(wire), *script.LauncherLocator) {
			t.Fatal("UUID bearer address leaked in non-owner response")
		}
	}
	assertPrivate(rows)
	updated, err := UpdateUserScriptWithDB(db, admin, script.ID, UpdateUserScriptInput{Name: "admin edit"})
	if err != nil {
		t.Fatal(err)
	}
	assertPrivate(updated)
	_, err = ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "fixture.invalid/quick", DurationMinutes: 10})
	if err != nil {
		t.Fatal(err)
	}
	_, grant, err := ConsumeScriptShortAuthorizationWithDB(db, "fixture.invalid", "/quick")
	if err != nil {
		t.Fatal(err)
	}
	exchange, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivate(exchange)
	if exchange.CommandURL != "https://fixture.invalid/quick" || exchange.Instruction != exchange.CommandURL {
		t.Fatalf("short launch escaped its own entry: %+v", exchange)
	}
	longURL, _ := url.Parse(script.DownloadURL)
	longGrant, err := IssuePublicScriptExchangeTokenByLocatorWithDB(db, longURL.Host, longURL.Path, 0)
	if err != nil {
		t.Fatal(err)
	}
	longExchange, err := ExchangeScriptTokenWithDB(db, longGrant.ShortCode, longGrant.Token)
	if err != nil {
		t.Fatal(err)
	}
	if longExchange.LaunchOrigin != "https://fixture.invalid" {
		t.Fatal("path UUID leaked into launch origin")
	}
}

func TestScriptOneTimeTicketNeverDisclosesFullBearer(t *testing.T) {
	for _, domainFormat := range []string{"*.fixture.invalid", "fixture.invalid/*"} {
		t.Run(domainFormat, func(t *testing.T) {
			db, owner, _, version := newManagedScriptFixture(t, 200)
			if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{domainFormat}}); err != nil {
				t.Fatal(err)
			}
			item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "once", VersionID: version.ID, LauncherOrigin: &ScriptLauncherOrigin{Host: "fixture.invalid", HTTPS: true}})
			if err != nil {
				t.Fatal(err)
			}
			grant, err := IssueScriptExchangeTokenWithDB(db, owner, item.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			result, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(result)
			if err != nil || strings.Contains(string(wire), *item.LauncherLocator) || strings.Contains(result.LaunchOrigin, *item.LauncherLocator) {
				t.Fatal("one-time ticket revealed a long-lived bearer in JSON or generated client configuration")
			}
			if result.CommandURL != "" || result.Instruction != "" || !strings.HasPrefix(result.LaunchOrigin, "https://") {
				t.Fatal("one-time response must use an API origin without a reusable command")
			}
			if item.UseWildcardDomain {
				origin, _ := url.Parse(result.LaunchOrigin)
				if _, err := ResolvePublicScriptWithDB(db, origin.Host, "/"); err == nil {
					t.Fatal("API-only origin unexpectedly launches a script")
				}
			}
			if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, result.ActivationToken, strings.Repeat("a", 64), "127.0.0.1", 0); err != nil {
				t.Fatalf("one-time grant lost its activation capability: %v", err)
			}
			var script entity.UserScript
			if err := db.First(&script, item.ID).Error; err != nil || script.LauncherUsedCount != 0 {
				t.Fatal("one-time owner grant consumed a full-entry budget")
			}
		})
	}
}
