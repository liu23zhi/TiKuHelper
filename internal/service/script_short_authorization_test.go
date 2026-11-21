package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func shortAuthorizationFixture(t *testing.T) (*gorm.DB, entity.User, entity.User, ScriptListItem) {
	t.Helper()
	db := newScriptTestDB(t)
	owner := entity.User{Username: "short-owner", Role: "user", Balance: 40}
	admin := entity.User{Username: "short-admin", Role: "admin"}
	bindSyntheticScriptOwner(&owner)
	for _, user := range []*entity.User{&owner, &admin} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"*.muioi.com", "muioi.com/*"}}); err != nil {
		t.Fatal(err)
	}
	version, err := UploadScriptVersionWithDB(db, admin, "short-v1", "fixture.exe", []byte("MZfixture"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "short fixture", VersionID: version.ID, BaseDomain: "a.muioi.com"})
	if err != nil {
		t.Fatal(err)
	}
	return db, owner, admin, script
}

func TestScriptShortAuthorizationIsExplicitExpiringAndSingleUse(t *testing.T) {
	db, owner, _, script := shortAuthorizationFixture(t)
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "a.muioi.com", "/"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("default entry must stay disabled: %v", err)
	}
	view, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "a.muioi.com", DurationMinutes: 5, MaxUses: 1})
	if err != nil {
		t.Fatal(err)
	}
	if view.Address != "https://a.muioi.com" || view.Command != `powershell -c "iex(irm https://a.muioi.com -MaximumRedirection 0 -TimeoutSec 15)"` || view.Status != "active" || view.ExpiresAt.Before(time.Now().Add(4*time.Minute)) {
		t.Fatalf("wrong short authorization: %+v", view)
	}
	resolved, grant, err := ConsumeScriptShortAuthorizationWithDB(db, "a.muioi.com", "/")
	if err != nil || resolved.ID != script.ID || grant.Token == "" || grant.LaunchOrigin != "https://a.muioi.com" {
		t.Fatalf("short entry did not mint scoped exchange: %v", err)
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "a.muioi.com", "/"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("short entry replay accepted: %v", err)
	}
	result, err := ExchangeScriptTokenWithDB(db, grant.ShortCode, grant.Token)
	if err != nil || result.LaunchOrigin != "https://a.muioi.com" {
		t.Fatalf("exchange must preserve validated short origin: %v / %q", err, result.LaunchOrigin)
	}
	if strings.Contains(result.Instruction, script.AccessKey) {
		t.Fatal("short flow leaked owner capability")
	}
	if _, err := IssuePublicScriptExchangeTokenWithDB(db, script.ShortCode, "", 0); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("full launcher auth was weakened: %v", err)
	}
}

func TestScriptShortAuthorizationEnforcesOwnerDomainExpiryAndRevocation(t *testing.T) {
	db, owner, admin, script := shortAuthorizationFixture(t)
	input := ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "muioi.com/a", DurationMinutes: 15}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, admin, script.ID, input); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("admin must not mint another owner's launcher: %v", err)
	}
	for _, address := range []string{"evil.test/a", "muioi.com", "muioi.com/api/a", "muioi.com/a/../b", "muioi.com/a?key=x", "http://muioi.com/a", "muioi.com:443/a", "a.muioi.com/path", "muioi.com/a%2Fb"} {
		input.Address = address
		if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); err == nil {
			t.Fatalf("unapproved address accepted: %s", address)
		}
	}
	input.Address = "muioi.com/a"
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "evil.test", "/a"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("wrong host accepted: %v", err)
	}
	if err := db.Model(&entity.UserScript{}).Where("id = ?", script.ID).Update("short_entry_expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/a"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("expired entry accepted: %v", err)
	}
	input.Address = "muioi.com/new-entry"
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, ConfigureScriptShortAuthorizationInput{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/a"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("revoked entry accepted: %v", err)
	}
	input.DurationMinutes = 0
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); !errors.Is(err, ErrScriptShortDurationInvalid) {
		t.Fatalf("missing lifetime accepted: %v", err)
	}
}

func TestScriptShortAuthorizationReservesAddressAndRechecksCurrentDomainPolicy(t *testing.T) {
	db, owner, admin, script := shortAuthorizationFixture(t)
	input := ConfigureScriptShortAuthorizationInput{Enabled: true, Address: "muioi.com/a", DurationMinutes: 5}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, script.ID, input); err != nil {
		t.Fatal(err)
	}
	version, err := UploadScriptVersionWithDB(db, admin, "short-v2", "fixture.exe", []byte("MZsecond"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "second", VersionID: version.ID, BaseDomain: "b.muioi.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConfigureScriptShortAuthorizationWithDB(db, owner, second.ID, input); !errors.Is(err, ErrScriptShortAddressInUse) {
		t.Fatalf("duplicate public address accepted: %v", err)
	}
	if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"*.other.test"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ConsumeScriptShortAuthorizationWithDB(db, "muioi.com", "/a"); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("revoked domain policy accepted: %v", err)
	}
}
