package service

import (
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func scriptDeniedSettings(t *testing.T, db *gorm.DB, payload string) (ScriptDomainSettings, error) {
	t.Helper()
	var input ScriptDomainSettings
	if err := json.Unmarshal([]byte(payload), &input); err != nil {
		t.Fatal(err)
	}
	return UpdateScriptDomainSettingsWithDB(db, input)
}

func TestBuiltinLocalHostsCannotBeAllocatedEvenWithEmptyDenylist(t *testing.T) {
	settings := defaultScriptDomainSettings()
	for _, host := range []string{
		"localhost", "LOCALHOST.:8443", "app.localhost", "localhost.localdomain",
		"127.0.0.1", "127.12.34.56:8443", "0.0.0.0", "[::1]", "[::]:8443", "[::ffff:127.0.0.1]",
	} {
		t.Run(host, func(t *testing.T) {
			for _, path := range []string{"", "/", "/script-entry"} {
				err := settings.validateAddressNotDenied("https://" + host + path)
				if !errors.Is(err, ErrScriptDomainDenied) {
					t.Errorf("local script address accepted: %s%s: %v", host, path, err)
				}
			}
		})
	}
	for _, host := range []string{"my-localhost.example.test", "localhost.example.test", "192.168.1.10", "[2001:db8::1]"} {
		if err := settings.validateAddressNotDenied("https://" + host + "/script-entry"); err != nil {
			t.Errorf("non-local address unexpectedly reserved: %s: %v", host, err)
		}
	}
}

func TestLocalFallbackCannotCreateLongOrShortScriptEntry(t *testing.T) {
	db, owner, seed := seedScriptReleaseGuard(t)
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{}, DeniedDomains: []string{}}); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"localhost:9043", "127.0.0.1:9043", "[::1]:9043"} {
		origin := ScriptLauncherOrigin{Host: host, HTTPS: true}
		if _, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "local", VersionID: seed.VersionID, LauncherOrigin: &origin}); !errors.Is(err, ErrScriptDomainDenied) {
			t.Errorf("generated local long entry was accepted: %s: %v", host, err)
		}
		if _, _, err := validateScriptShortAddressWithDB(db, "https://"+host+"/short", host); !errors.Is(err, ErrScriptDomainDenied) {
			t.Errorf("custom local short entry was accepted: %s: %v", host, err)
		}
	}
}

func TestScriptDomainDenyPrecedenceAndNormalization(t *testing.T) {
	db := newScriptTestDB(t)
	settings, err := scriptDeniedSettings(t, db, `{"domains":["*.a.com"],"deniedDomains":[" ADMIN.A.COM ","*.INTERNAL.a.com","admin.a.com"]}`)
	if err != nil {
		t.Fatalf("overlapping allow/deny entries must be editable: %v", err)
	}
	wire, _ := json.Marshal(settings)
	if !strings.Contains(string(wire), `"deniedDomains":["*.internal.a.com","admin.a.com"]`) {
		t.Fatalf("deny list was not normalized and retained: %s", wire)
	}
	for _, host := range []string{"admin.a.com", "ADMIN.A.COM", "x.internal.a.com", "x.y.internal.a.com"} {
		if err := ValidateScriptDomainWithDB(db, host); !errors.Is(err, ErrScriptDomainUnavailable) || !strings.Contains(err.Error(), "禁止") {
			t.Errorf("denied host %q accepted or lacks useful reason: %v", host, err)
		}
	}
	for _, host := range []string{"internal.a.com", "x.a.com", "notadmin.a.com"} {
		if err := ValidateScriptDomainWithDB(db, host); err != nil {
			t.Errorf("allowed exception boundary %q blocked: %v", host, err)
		}
	}
}

func TestScriptDomainDeniedSettingsAtomicAndBackwardCompatible(t *testing.T) {
	db := newScriptTestDB(t)
	initial, err := scriptDeniedSettings(t, db, `{"domains":["a.com/*"],"deniedDomains":["admin.a.com"]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"https://a.com", "a.com:443", "a.com/path", "a.com?x=y", "127.0.0.1", "::1", "a.*.com", "*a.com", "a..com", "a.com.", "-a.com"} {
		payload, _ := json.Marshal(map[string]any{"domains": []string{"other.test/*"}, "deniedDomains": []string{rule}})
		if _, err := scriptDeniedSettings(t, db, string(payload)); err == nil || !strings.Contains(err.Error(), rule) {
			t.Errorf("invalid deny rule %q accepted or error lacks its value: %v", rule, err)
		}
		stored, err := GetScriptDomainSettingsWithDB(db)
		if err != nil || !reflect.DeepEqual(stored, initial) {
			t.Fatalf("invalid batch changed settings: %+v, %v", stored, err)
		}
	}
	// Old clients omit the added field; this must preserve the administrator's list.
	if _, err := scriptDeniedSettings(t, db, `{"domains":["*.a.com"],"allowFullNeverExpire":false}`); err != nil {
		t.Fatal(err)
	}
	if err := ValidateScriptDomainWithDB(db, "admin.a.com"); err == nil {
		t.Fatal("old payload erased deniedDomains")
	}
	if _, err := scriptDeniedSettings(t, db, `{"deniedDomains":[]}`); err != nil {
		t.Fatal(err)
	}
	if err := ValidateScriptDomainWithDB(db, "admin.a.com"); err != nil {
		t.Fatalf("explicit empty array did not restore use: %v", err)
	}
	stored, err := GetScriptDomainSettingsWithDB(db)
	if err != nil || len(stored.Domains) != 1 || *stored.AllowFullNeverExpire {
		t.Fatalf("deny-only update changed other settings: %+v, %v", stored, err)
	}
	if err := db.Model(&entity.SystemSetting{}).Where("key = ?", ScriptDomainSettingsKey).Update("value", `{"domains":["*.a.com"]}`).Error; err != nil {
		t.Fatal(err)
	}
	stored, err = GetScriptDomainSettingsWithDB(db)
	wire, _ := json.Marshal(stored)
	if err != nil || !strings.Contains(string(wire), `"deniedDomains":[]`) {
		t.Fatalf("old database record must return an empty deny list: %s, %v", wire, err)
	}
}

func TestScriptDomainDeniedConcurrentUpdatePreservesFields(t *testing.T) {
	db := newScriptTestDB(t)
	if _, err := scriptDeniedSettings(t, db, `{"domains":["*.a.com"]}`); err != nil {
		t.Fatal(err)
	}
	const callback = "test:concurrent_script_deny_policy"
	if err := db.Callback().Update().Before("gorm:begin_transaction").Register(callback, func(statement *gorm.DB) {
		if statement.Statement.Table != entity.TableNameSystemSetting {
			return
		}
		if err := db.Callback().Update().Remove(callback); err != nil {
			t.Fatal(err)
		}
		if _, err := scriptDeniedSettings(t, db, `{"domains":["*.b.com"],"allowFullNeverExpire":false,"allowShortNeverExpire":true}`); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	settings, err := scriptDeniedSettings(t, db, `{"deniedDomains":["admin.b.com"]}`)
	if err != nil || !reflect.DeepEqual(settings.Domains, []string{"*.b.com"}) || *settings.AllowFullNeverExpire || !*settings.AllowShortNeverExpire {
		t.Fatalf("CAS retry lost concurrent fields: %+v, %v", settings, err)
	}
	if err := ValidateScriptDomainWithDB(db, "admin.b.com"); err == nil {
		t.Fatal("CAS retry lost deniedDomains")
	}
}

func TestScriptDomainDeniedBlocksGeneratedAndFallbackAddresses(t *testing.T) {
	for _, format := range []string{"a.com/*", "*.a.com", ""} {
		name := map[string]string{"a.com/*": "path", "*.a.com": "wildcard", "": "fallback"}[format]
		t.Run(name, func(t *testing.T) {
			db, owner, seed := seedScriptReleaseGuard(t)
			domains := []string{}
			if format != "" {
				domains = append(domains, format)
			}
			if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: domains}); err != nil {
				t.Fatal(err)
			}
			origin := ScriptLauncherOrigin{Host: "a.com:8443", HTTPS: true}
			item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "generated", VersionID: seed.VersionID, LauncherOrigin: &origin, DomainFormat: format})
			if err != nil {
				t.Fatal(err)
			}
			address, _ := url.Parse(BuildScriptCommandURL(item.UserScript))
			payload, _ := json.Marshal(map[string]any{"deniedDomains": []string{address.Hostname()}})
			if _, err := scriptDeniedSettings(t, db, string(payload)); err != nil {
				t.Fatal(err)
			}
			if _, err := ResolvePublicScriptWithDB(db, address.Host, address.Path); !errors.Is(err, ErrScriptNotFound) || strings.Contains(err.Error(), address.Hostname()) {
				t.Fatalf("existing generated entry not blocked anonymously: %v", err)
			}
			if _, _, err := validateScriptShortAddressWithDB(db, address.String(), origin.Host); err == nil {
				t.Fatal("short address bypassed deny rule")
			}
			if format != "*.a.com" {
				if _, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "blocked", VersionID: seed.VersionID, LauncherOrigin: &origin, DomainFormat: format}); err == nil {
					t.Fatal("new generated/fallback address bypassed deny rule")
				}
			}
			if _, err := scriptDeniedSettings(t, db, `{"deniedDomains":["*.a.com","a.com"]}`); err != nil {
				t.Fatal(err)
			}
			if _, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "all-blocked", VersionID: seed.VersionID, LauncherOrigin: &origin, DomainFormat: format}); err == nil {
				t.Fatal("fully denied format was still allocated")
			}
			if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, item.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, DomainFormat: format, RequestHost: origin.Host, RequestHTTPS: true, DurationMinutes: 5}); err == nil {
				t.Fatal("automatic short address bypassed deny rule")
			}
		})
	}
}

func TestScriptDomainDeniedImmediatelyBlocksExistingGrantAndRestores(t *testing.T) {
	db, owner, script := seedScriptReleaseGuard(t)
	grant, err := IssueScriptExchangeTokenWithDB(db, owner, script.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	host := script.ShortCode + ".example.test"
	payload, _ := json.Marshal(map[string]any{"deniedDomains": []string{host}})
	if _, err := scriptDeniedSettings(t, db, string(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePublicScriptWithDB(db, host, ""); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("legacy entry bypassed concrete host deny: %v", err)
	}
	if _, err := ExchangeScriptTokenWithDB(db, script.ShortCode, grant.Token); err == nil {
		t.Fatal("issued exchange grant bypassed deny rule")
	}
	if _, err := scriptDeniedSettings(t, db, `{"deniedDomains":[]}`); err != nil {
		t.Fatal(err)
	}
	result, err := ExchangeScriptTokenWithDB(db, script.ShortCode, grant.Token)
	if err != nil {
		t.Fatalf("removing deny should restore existing unused grant: %v", err)
	}
	if _, err := scriptDeniedSettings(t, db, string(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := DownloadScriptVersionByActivationTokenWithDB(db, script.ShortCode, result.ActivationToken); err == nil {
		t.Fatal("activation download bypassed domain deny")
	}
	if _, err := ActivateScriptRunSessionWithDB(db, script.ShortCode, result.ActivationToken, "deny-fixture"); err == nil {
		t.Fatal("activation bypassed domain deny")
	}
}

func TestScriptDomainDeniedShortGrantExchangeIsRechecked(t *testing.T) {
	db, owner, _, script := shortAuthorizationFixture(t)
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "muioi.com/denied", DurationMinutes: 5}); err != nil {
		t.Fatal(err)
	}
	_, grant, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/denied")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scriptDeniedSettings(t, db, `{"deniedDomains":["muioi.com"]}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/denied"); !errors.Is(err, ErrScriptNotFound) || strings.Contains(err.Error(), "muioi.com") {
		t.Fatalf("short entry leaked or bypassed deny policy: %v", err)
	}
	if _, err := ExchangeScriptTokenWithDB(db, script.ShortCode, grant.Token); err == nil {
		t.Fatal("short issued grant bypassed concrete launch host denial")
	}
	if _, err := scriptDeniedSettings(t, db, `{"deniedDomains":[]}`); err != nil {
		t.Fatal(err)
	}
	result, err := ExchangeScriptTokenWithDB(db, script.ShortCode, grant.Token)
	if err != nil {
		t.Fatalf("clearing deny must restore unused short grant: %v", err)
	}
	if _, err := scriptDeniedSettings(t, db, `{"deniedDomains":["muioi.com"]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := DownloadScriptVersionByActivationTokenWithDB(db, script.ShortCode, result.ActivationToken); err == nil {
		t.Fatal("short activation download ignored a deny added after exchange")
	}
	if _, err := ActivateScriptRunSessionWithDB(db, script.ShortCode, result.ActivationToken, "short-deny-fixture"); err == nil {
		t.Fatal("short activation ignored a deny added after exchange")
	}
}

func TestScriptDomainDeniedLegacyCreationAndAddressMoveUseConcreteHost(t *testing.T) {
	db, owner, script := seedScriptReleaseGuard(t)
	if _, err := scriptDeniedSettings(t, db, `{"deniedDomains":["b.example.test"]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "blocked", VersionID: script.VersionID, BaseDomain: "example.test"}); err == nil {
		t.Fatal("legacy creation allocated a denied concrete host")
	}
	payload, _ := json.Marshal(map[string]any{"deniedDomains": []string{script.ShortCode + ".blocked.test"}})
	if _, err := scriptDeniedSettings(t, db, string(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateUserScriptWithDB(db, owner, script.ID, UpdateUserScriptInput{BaseDomain: "blocked.test"}); err == nil {
		t.Fatal("legacy move allocated a denied concrete host")
	}
	var stored entity.UserScript
	if err := db.First(&stored, script.ID).Error; err != nil || stored.BaseDomain != script.BaseDomain {
		t.Fatalf("denied move partially saved: %+v, %v", stored, err)
	}
}

func TestScriptDomainDeniedRunningSessionStopsAndRestores(t *testing.T) {
	db, owner, script := seedScriptReleaseGuard(t)
	grant, err := IssueScriptExchangeTokenWithDB(db, owner, script.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := ExchangeScriptTokenWithDB(db, script.ShortCode, grant.Token)
	if err != nil {
		t.Fatal(err)
	}
	session, err := ActivateScriptRunSessionWithDB(db, script.ShortCode, ticket.ActivationToken, "deny-running")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scriptDeniedSettings(t, db, `{"deniedDomains":["*.example.test"]}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveScriptRunSessionWithDB(db, "Bearer "+session.Token, "deny-running"); err == nil || strings.Contains(err.Error(), "*.example.test") {
		t.Fatalf("running session bypassed or leaked deny policy: %v", err)
	}
	if _, err := scriptDeniedSettings(t, db, `{"deniedDomains":[]}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveScriptRunSessionWithDB(db, "Bearer "+session.Token, "deny-running"); err != nil {
		t.Fatalf("removing deny did not restore running session: %v", err)
	}
}

func TestScriptDomainDeniedLegacyRootDoesNotDenyConcreteChild(t *testing.T) {
	for _, operation := range []string{"create", "move", "entry-and-ticket"} {
		t.Run(operation, func(t *testing.T) {
			db, owner, script := seedScriptReleaseGuard(t)
			if _, err := scriptDeniedSettings(t, db, `{"deniedDomains":["example.test","other.test"]}`); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "create":
				created, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "allowed child", VersionID: script.VersionID, BaseDomain: "example.test"})
				if err != nil {
					t.Fatalf("exact root deny must allow newly allocated child: %v", err)
				}
				if created.BaseDomain != "example.test" || !created.UseWildcardDomain {
					t.Fatalf("legacy child creation changed address semantics: %+v", created.UserScript)
				}
			case "move":
				moved, err := UpdateUserScriptWithDB(db, owner, script.ID, UpdateUserScriptInput{BaseDomain: "other.test"})
				if err != nil || moved.BaseDomain != "other.test" {
					t.Fatalf("exact root deny must allow moving to a concrete child: %v", err)
				}
				script = moved
			}
			host := script.ShortCode + "." + script.BaseDomain
			if _, err := ResolvePublicScriptWithDB(db, host, ""); err != nil {
				t.Errorf("root deny blocked an allowed legacy child entry %q: %v", host, err)
			}
			grant, err := IssueScriptExchangeTokenWithDB(db, owner, script.ID, 0)
			if err != nil {
				t.Fatalf("root deny blocked allowed child ticket: %v", err)
			}
			if _, err := ExchangeScriptTokenWithDB(db, script.ShortCode, grant.Token); err != nil {
				t.Fatalf("root deny blocked allowed child exchange: %v", err)
			}
		})
	}
}

func TestScriptDomainDeniedShortOriginPersistsThroughRunningSession(t *testing.T) {
	for _, leased := range []bool{false, true} {
		t.Run(map[bool]string{false: "run-download", true: "lease-heartbeat"}[leased], func(t *testing.T) {
			db, owner, _, script := shortAuthorizationFixture(t)
			if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "muioi.com/short-run", DurationMinutes: 5}); err != nil {
				t.Fatal(err)
			}
			_, grant, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/short-run")
			if err != nil {
				t.Fatal(err)
			}
			ticket, err := ExchangeScriptTokenWithDB(db, script.ShortCode, grant.Token)
			if err != nil {
				t.Fatal(err)
			}
			const identity = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
			var session ScriptRunSessionGrant
			if leased {
				session, err = ActivateLeasedScriptRunSessionWithDB(db, script.ShortCode, ticket.ActivationToken, identity, "127.0.0.1", 0)
			} else {
				session, err = ActivateScriptRunSessionWithDB(db, script.ShortCode, ticket.ActivationToken, identity)
			}
			if err != nil {
				t.Fatal(err)
			}
			check := func(blocked bool) {
				t.Helper()
				_, stored, authErr := ResolveScriptRunSessionWithDB(db, "Bearer "+session.Token, identity, "127.0.0.1")
				if (authErr != nil) != blocked || (authErr != nil && strings.Contains(authErr.Error(), "muioi.com")) {
					t.Errorf("short origin auth blocked=%v: %v", blocked, authErr)
				}
				if authErr == nil {
					wire, _ := json.Marshal(stored)
					if strings.Contains(string(wire), "short-run") || strings.Contains(string(wire), "launchAddress") {
						t.Errorf("run session JSON exposed launcher address: %s", wire)
					}
				}
				if leased {
					_, err = HeartbeatScriptRunSessionWithDB(db, script.ShortCode, "Bearer "+session.Token, identity, "127.0.0.1")
				} else {
					_, err = DownloadScriptVersionByRunSessionWithDB(db, script.ShortCode, "Bearer "+session.Token, identity)
				}
				if (err != nil) != blocked {
					t.Errorf("short origin heartbeat/download blocked=%v: %v", blocked, err)
				}
			}
			check(false)
			if _, err := scriptDeniedSettings(t, db, `{"deniedDomains":["muioi.com"]}`); err != nil {
				t.Fatal(err)
			}
			check(true)
			if _, err := scriptDeniedSettings(t, db, `{"deniedDomains":[]}`); err != nil {
				t.Fatal(err)
			}
			check(false)
		})
	}
}

func TestScriptDomainDeniedRunSessionMigrationKeepsExistingRows(t *testing.T) {
	db := newScriptTestDB(t)
	if db.Migrator().HasColumn(&entity.ScriptRunSession{}, "launch_address") {
		if err := db.Migrator().DropColumn(&entity.ScriptRunSession{}, "launch_address"); err != nil {
			t.Fatal(err)
		}
	}
	activated := time.Now().Truncate(time.Second)
	expires := activated.Add(time.Hour)
	legacy := map[string]any{"id": 1, "script_id": 1, "user_id": 2, "access_token_id": 3, "token_hash": "existing-token-digest", "identity_hash": "existing-identity-digest", "activated_at": activated, "expires_at": expires}
	if err := db.Table(entity.TableNameScriptRunSession).Create(legacy).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.AutoMigrate(&entity.ScriptRunSession{}); err != nil {
			t.Fatal(err)
		}
	}
	if !db.Migrator().HasColumn(&entity.ScriptRunSession{}, "launch_address") {
		t.Fatal("AutoMigrate did not add launch_address to existing sessions")
	}
	var stored struct {
		ID            int64
		UserID        int32
		TokenHash     string
		IdentityHash  string
		LaunchAddress string
		ActivatedAt   time.Time
		ExpiresAt     time.Time
	}
	if err := db.Table(entity.TableNameScriptRunSession).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ID != 1 || stored.UserID != 2 || stored.TokenHash != "existing-token-digest" || stored.IdentityHash != "existing-identity-digest" || stored.LaunchAddress != "" || !stored.ActivatedAt.Equal(activated) || !stored.ExpiresAt.Equal(expires) {
		t.Fatalf("session migration changed existing data: %+v", stored)
	}
}
