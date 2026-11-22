package service

import "testing"

func TestPlatformLabelCannotBeAssignedAsWildcardShortHost(t *testing.T) {
	db, _, _, _ := newManagedScriptFixture(t, 100)
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"*.example.test"}, DeniedDomains: []string{}}); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"tiku.example.test", "TIKU.example.test.:443/"} {
		if _, _, err := validateScriptShortAddressWithDB(db, address, "tiku.example.test"); err == nil {
			t.Fatalf("reserved platform host was allocatable: %s", address)
		}
	}
	if _, _, err := validateScriptShortAddressWithDB(db, "a.example.test", "tiku.example.test"); err != nil {
		t.Fatal(err)
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
	check("localhost:8061", StandaloneWebHost)
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
	check("unassigned.invalid", EntryOnlyWebHost)
	settings := ScriptDomainSettings{Domains: []string{"*.example.test"}, DeniedDomains: []string{"tiku.example.test", "console.independent.test", "blocked.example.test"}}
	if _, err := UpdateScriptDomainSettingsWithDB(db, settings); err != nil {
		t.Fatal(err)
	}
	check("tiku.example.test", PlatformWebHost)
	check("blocked.example.test", RejectScriptWebHost)
	check("console.independent.test", PlatformWebHost)
	settings.DeniedDomains = []string{}
	if _, err := UpdateScriptDomainSettingsWithDB(db, settings); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateScriptShortAddressWithDB(db, "tiku.example.test", "console.independent.test"); err == nil {
		t.Fatal("clearing the editable denylist removed the reserved platform label")
	}
}
