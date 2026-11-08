package service

import (
	"errors"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func seedScriptReleaseGuard(t *testing.T) (*gorm.DB, entity.User, ScriptListItem) {
	t.Helper()
	db := newScriptTestDB(t)
	owner := entity.User{Username: "script-fixture-owner", Role: "user", Balance: 50}
	admin := entity.User{Username: "script-fixture-admin", Role: "admin"}
	for _, user := range []*entity.User{&owner, &admin} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	version, err := UploadScriptVersionWithDB(db, admin, "fixture-v1", "fixture.exe", []byte("MZfixture"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "fixture", VersionID: version.ID, BaseDomain: "example.test"})
	if err != nil {
		t.Fatal(err)
	}
	return db, owner, item
}

func TestScriptRunSessionRejectsInvalidDedicatedQuota(t *testing.T) {
	db, owner, item := seedScriptReleaseGuard(t)
	exchange, err := IssueScriptExchangeTokenWithDB(db, owner, item.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := ExchangeScriptTokenWithDB(db, item.ShortCode, exchange.Token)
	if err != nil {
		t.Fatal(err)
	}
	session, err := ActivateScriptRunSessionWithDB(db, item.ShortCode, ticket.ActivationToken, "fixture-identity")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.AccessToken{}).Where("id = ?", item.AccessTokenID).
		Update("total_quota_limit", entity.AccessTokenQuotaLimitMaxExclusive).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveScriptRunSessionWithDB(db, "Bearer "+session.Token, "fixture-identity"); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("invalid active quota must fail authorization, got %v", err)
	}
}

func TestScriptTicketsFailClosedWhenOwnerOrDomainWasRevoked(t *testing.T) {
	t.Run("disabled-owner-cannot-mint", func(t *testing.T) {
		db, owner, item := seedScriptReleaseGuard(t)
		if err := db.Model(&owner).Update("status", "disabled").Error; err != nil {
			t.Fatal(err)
		}
		if _, err := IssuePublicScriptExchangeTokenWithDB(db, item.ShortCode, scriptLauncherCapability(item.UserScript), 0); !errors.Is(err, ErrScriptTokenInvalid) {
			t.Fatalf("disabled owner must not mint a ticket, got %v", err)
		}
	})
	t.Run("revoked-domain-cannot-exchange", func(t *testing.T) {
		db, owner, item := seedScriptReleaseGuard(t)
		grant, err := IssueScriptExchangeTokenWithDB(db, owner, item.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := UpdateScriptDomainSettingsWithDB(db, ScriptDomainSettings{Domains: []string{"other.test"}, AllowWildcard: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := ExchangeScriptTokenWithDB(db, item.ShortCode, grant.Token); !errors.Is(err, ErrScriptDomainUnavailable) {
			t.Fatalf("revoked domain must not redeem a ticket, got %v", err)
		}
	})
}

func TestScriptLauncherLocatorAloneDoesNotGrantOwnerSpending(t *testing.T) {
	db, _, item := seedScriptReleaseGuard(t)
	for _, capability := range []string{"", item.AccessKey, "incorrect-capability"} {
		if _, err := IssuePublicScriptExchangeTokenWithDB(db, item.ShortCode, capability, 0); !errors.Is(err, ErrScriptNotFound) {
			t.Fatalf("locator, raw Key or wrong capability must not launch, got %v", err)
		}
	}
	capability := scriptLauncherCapability(item.UserScript)
	grant, err := IssuePublicScriptExchangeTokenWithDB(db, item.ShortCode, capability, 0)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ExchangeScriptTokenWithDB(db, item.ShortCode, grant.Token)
	if err != nil || result.LaunchOrigin != "https://"+item.ShortCode+".example.test" || result.Instruction != result.CommandURL {
		t.Fatalf("validated launcher must preserve configured origin and return only locator, err=%v", err)
	}
	if item.DownloadURL != result.CommandURL {
		t.Fatal("the locator returned to the executable must not contain a launcher capability")
	}
}

func TestScriptLauncherCapabilityIsOwnerOnlyEvenInAdminManagement(t *testing.T) {
	db, owner, item := seedScriptReleaseGuard(t)
	var admin entity.User
	if err := db.Where("role = ?", "admin").First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := ListUserScriptsWithDB(db, admin, true)
	if err != nil || len(rows) != 1 || rows[0].ID != item.ID || rows[0].Command != "" || rows[0].Instruction != "" {
		t.Fatalf("admin must manage the row without accessing the owner's launch capability, err=%v", err)
	}
	if _, err := IssueScriptExchangeTokenWithDB(db, admin, item.ID, 0); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("admin must not mint a ticket spending another user's balance, got %v", err)
	}
	rows, err = ListUserScriptsWithDB(db, owner, false)
	if err != nil || len(rows) != 1 || rows[0].Command == "" {
		t.Fatalf("owner's authorized list must retain its launcher, err=%v", err)
	}
}

func TestScriptDomainPolicyReadFailureDoesNotAllowAll(t *testing.T) {
	db := newScriptTestDB(t)
	if err := db.Migrator().DropTable(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateScriptDomainWithDB(db, "example.test", true); err == nil {
		t.Fatal("cannot treat a failed policy read as allow-all")
	}
}

func TestScriptCreateIdempotencyCannotReserveTwice(t *testing.T) {
	db, owner, first := seedScriptReleaseGuard(t)
	if err := db.AutoMigrate(&entity.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	input := CreateUserScriptInput{Name: "second", VersionID: first.VersionID, BaseDomain: "example.test", IdempotencyKey: "script-fixture-create"}
	created, err := CreateUserScriptWithDB(db, owner, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&owner).Update("balance", -1).Error; err != nil {
		t.Fatal(err)
	}
	replayed, err := CreateUserScriptWithDB(db, owner, input)
	if err != nil || created.ID != replayed.ID {
		t.Fatalf("replay must return same script before new-spend checks: IDs %d/%d err=%v", created.ID, replayed.ID, err)
	}
	input.Name = "changed"
	if _, err := CreateUserScriptWithDB(db, owner, input); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting retry must be rejected, got %v", err)
	}
	var reservations int64
	if err := db.Model(&entity.BillingRecord{}).Where("user_id = ? AND type = ?", owner.ID, "script_reservation").Count(&reservations).Error; err != nil {
		t.Fatal(err)
	}
	if reservations != 2 {
		t.Fatalf("expected one reservation for each of two scripts, got %d", reservations)
	}
}
