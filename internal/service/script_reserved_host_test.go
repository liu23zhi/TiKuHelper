package service

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestPlatformLabelCannotBeAssignedAsWildcardShortHost(t *testing.T) {
	db, _, _, _ := newManagedScriptFixture(t, 100)
	if err := ConfigureSiteHosts(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ConfigureSiteHosts(nil) })
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"*.example.test"}, DeniedDomains: []string{}}); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"tiku.example.test", "TIKU.example.test.:443/"} {
		if _, _, err := validateScriptShortAddressWithDB(db, address, "tiku.example.test"); err != nil {
			t.Fatalf("undeclared tiku host should be allocatable: %s: %v", address, err)
		}
	}
	if _, _, err := validateScriptShortAddressWithDB(db, "a.example.test", "tiku.example.test"); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureSiteHosts([]string{"tiku.example.test"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateScriptShortAddressWithDB(db, "tiku.example.test", "tiku.example.test"); err == nil {
		t.Fatal("explicitly declared tiku site host was allocatable to a script")
	}
}

func TestHostBoundaryBootstrapAndTrustedAliases(t *testing.T) {
	db, _, _, _ := newManagedScriptFixture(t, 100)
	if err := ConfigureSiteHosts(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ConfigureSiteHosts(nil) })
	check := func(raw string, want ScriptWebHostRole) {
		t.Helper()
		got, err := ScriptWebHostRoleWithDB(db, raw)
		if err != nil || got != want {
			t.Fatalf("%s: got %d want %d err %v", raw, got, want, err)
		}
	}
	check("existing-single-site.test", StandaloneWebHost)
	check("localhost:8061", PlatformWebHost)
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"entry.example.test/*"}, DeniedDomains: []string{"previous-console.test"}}); err != nil {
		t.Fatal(err)
	}
	check("previous-console.test", StandaloneWebHost)
	check("entry.example.test", StandaloneWebHost)
	if err := ConfigureSiteHosts([]string{"console.independent.test"}); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureSiteHosts([]string{"*.untrusted.test"}); err == nil {
		t.Fatal("wildcard is not a deployment site alias")
	}
	check("CONSOLE.INDEPENDENT.TEST.:9443", PlatformWebHost)
	check("unassigned.invalid", StandaloneWebHost)
	settings := ScriptDomainSettings{Domains: []string{"*.example.test"}, DeniedDomains: []string{"tiku.example.test", "console.independent.test", "blocked.example.test"}}
	if _, err := UpdateScriptDomainSettingsWithDB(db, settings); err != nil {
		t.Fatal(err)
	}
	check("tiku.example.test", StandaloneWebHost)
	check("blocked.example.test", StandaloneWebHost)
	check("console.independent.test", PlatformWebHost)
	settings.DeniedDomains = []string{}
	if _, err := UpdateScriptDomainSettingsWithDB(db, settings); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateScriptShortAddressWithDB(db, "tiku.example.test", "console.independent.test"); err != nil {
		t.Fatalf("undeclared tiku host should be allocatable after clearing denylist: %v", err)
	}
	if err := ConfigureSiteHosts([]string{"console.independent.test", "tiku.example.test"}); err != nil {
		t.Fatal(err)
	}
	check("tiku.example.test", PlatformWebHost)
	if _, _, err := validateScriptShortAddressWithDB(db, "tiku.example.test", "console.independent.test"); err == nil {
		t.Fatal("explicit siteHosts declaration did not reserve tiku host")
	}
}

func TestHostBoundaryUsesPermanentRootReservations(t *testing.T) {
	db := newScriptTestDB(t)
	if err := ConfigureSiteHosts(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ConfigureSiteHosts(nil) })
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"*.example.test", "shared.example.test/*"}, DeniedDomains: []string{"retired.example.test", "blocked.example.test"}}); err != nil {
		t.Fatal(err)
	}
	// No live script is required: replacement, deletion, or a conflicting historic
	// owner leaves the reservation in place and must never expose the WebUI.
	for _, address := range []string{"https://retired.example.test", "https://shared.example.test/old-entry", "https://localhost", "https://127.0.0.1"} {
		if err := reserveScriptAddress(db, address, scriptAddressShort, blockedScriptAddressOwnerID); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		host string
		want ScriptWebHostRole
	}{
		{"retired.example.test", EntryOnlyWebHost},
		{"RETIRED.EXAMPLE.TEST.:8680", EntryOnlyWebHost},
		{"shared.example.test", StandaloneWebHost},
		{"free.example.test", StandaloneWebHost},
		{"unknown.invalid", StandaloneWebHost},
		{"blocked.example.test", StandaloneWebHost},
		{"localhost:8680", PlatformWebHost},
		{"127.0.0.1:8680", PlatformWebHost},
		{"tiku.example.test", StandaloneWebHost},
	} {
		t.Run(tc.host, func(t *testing.T) {
			got, err := ScriptWebHostRoleWithDB(db, tc.host)
			if err != nil || got != tc.want {
				t.Fatalf("host role: got %d err %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestHostBoundaryRetainsHistoricalDedicatedHostsWithoutReservations(t *testing.T) {
	db := newScriptTestDB(t)
	if err := ConfigureSiteHosts(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ConfigureSiteHosts(nil) })
	past := time.Now().Add(-time.Hour)
	locator := "b4a98b0e-3184-4a69-b930-bfd76cad824b"
	pathLocator := "b0289f1f-4b98-45fb-9eab-acbbcb2d497b"
	for _, script := range []entity.UserScript{
		{ShortCode: "legacy", BaseDomain: "example.test", UseWildcardDomain: true},
		{ShortCode: "generated", BaseDomain: "example.test", UseWildcardDomain: true, LauncherLocator: &locator, LauncherBearerEnabled: true},
		{ShortCode: "disabled", BaseDomain: "shared.example.test", Enabled: false, ShortEntryAddress: "https://disabled.example.test"},
		{ShortCode: "deleted", ShortEntryAddress: "https://deleted.example.test", DeletedAt: &past},
		{ShortCode: "expired", ShortEntryAddress: "https://expired.example.test", ShortEntryExpiresAt: &past},
		{ShortCode: "spent", ShortEntryAddress: "https://spent.example.test", ShortEntryMaxUses: 1, ShortEntryUsedCount: 1, ShortEntryUsedAt: &past},
		{ShortCode: "alternate", ShortEntryAddress: "https://alternate.example.test:9043/"},
		{ShortCode: "path", BaseDomain: "path.example.test", LauncherLocator: &pathLocator, ShortEntryAddress: "https://path.example.test/private-entry"},
	} {
		// Direct rows model pre-reservation installations, including retired rows.
		// No assignment/availability helper may filter these hosts out.
		if err := db.Create(&script).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		host string
		want ScriptWebHostRole
	}{
		{"legacy.example.test", EntryOnlyWebHost},
		{locator + ".example.test", EntryOnlyWebHost},
		{"generated.example.test", EntryOnlyWebHost},
		{"disabled.example.test", EntryOnlyWebHost},
		{"deleted.example.test", EntryOnlyWebHost},
		{"expired.example.test", EntryOnlyWebHost},
		{"spent.example.test", EntryOnlyWebHost},
		{"alternate.example.test:8680", EntryOnlyWebHost},
		{"path.example.test", StandaloneWebHost},
		{"shared.example.test", StandaloneWebHost},
		{"free.example.test", StandaloneWebHost},
	} {
		t.Run(tc.host, func(t *testing.T) {
			got, err := ScriptWebHostRoleWithDB(db, tc.host)
			if err != nil || got != tc.want {
				t.Fatalf("host role: got %d err %v; want %d", got, err, tc.want)
			}
		})
	}
}
