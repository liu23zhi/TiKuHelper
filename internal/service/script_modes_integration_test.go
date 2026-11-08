package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newManagedScriptFixture(t *testing.T, balance float64) (*gorm.DB, entity.User, entity.User, entity.ScriptVersion) {
	t.Helper()
	db := newScriptTestDB(t)
	if err := db.AutoMigrate(&entity.ScriptEscrow{}, &entity.ScriptEscrowEntry{}, &entity.ScriptActivationEntitlement{}); err != nil {
		t.Fatal(err)
	}
	owner := entity.User{Username: "mode-owner", Role: "user", Balance: balance}
	admin := entity.User{Username: "mode-admin", Role: "admin"}
	bindSyntheticScriptOwner(&owner)
	for _, user := range []*entity.User{&owner, &admin} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	version, err := UploadScriptVersionWithDB(db, admin, "mode-fixture", "fixture.exe", []byte("MZsynthetic"))
	if err != nil {
		t.Fatal(err)
	}
	return db, owner, admin, version
}

func TestManagedEscrowScriptCreateAndRelaunchShareAtomicState(t *testing.T) {
	db, owner, _, version := newManagedScriptFixture(t, 79.999999)
	input := CreateUserScriptInput{Name: "fixture", VersionID: version.ID, BaseDomain: "lease.example",
		BillingMode: entity.ScriptBillingModeEscrow, AutoTopUp: true, RunDurationMinutes: 90}
	if _, err := CreateUserScriptWithDB(db, owner, input); !errors.Is(err, ErrScriptEscrowInsufficient) {
		t.Fatalf("insufficient initial wallet: %v", err)
	}
	var scripts int64
	if err := db.Model(&entity.UserScript{}).Count(&scripts).Error; err != nil || scripts != 0 {
		t.Fatalf("rejected create must roll back script: count=%d err=%v", scripts, err)
	}
	if err := db.Model(&owner).Update("balance", 100).Error; err != nil {
		t.Fatal(err)
	}
	item, err := CreateUserScriptWithDB(db, owner, input)
	if err != nil || item.ReservedFee != 0 || !item.AutoTopUp || item.RunDurationMinutes != 90 {
		t.Fatalf("managed create must not retain legacy money: %v", err)
	}
	var state entity.ScriptEscrow
	if err := db.First(&state, item.ID).Error; err != nil || state.AvailableMicros != 80_000_000 {
		t.Fatalf("creation did not freeze ¥80: %d err=%v", state.AvailableMicros, err)
	}
	if err := db.First(&owner, owner.ID).Error; err != nil || owner.Balance != 20 {
		t.Fatalf("unexpected wallet after freeze: %v err=%v", owner.Balance, err)
	}
	insertLeaseTicket(t, db, item, "managed-first")
	a, b := strings.Repeat("12", 32), strings.Repeat("34", 32)
	first, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "managed-first", a, "127.0.0.1", 0)
	if err != nil || time.Until(first.ExpiresAt) > 90*time.Minute || time.Until(first.ExpiresAt) < 89*time.Minute {
		t.Fatalf("first managed lease must use configured duration: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := SettleScriptEscrowMicrosInTx(tx, item.ID, owner.ID, "spent-fixture", 40_000_000)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.ScriptRunSession{}).Where("script_id = ?", item.ID).
		Update("last_heartbeat_at", time.Now().Add(-ScriptHeartbeatTimeout)).Error; err != nil {
		t.Fatal(err)
	}
	insertLeaseTicket(t, db, item, "managed-second")
	second, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "managed-second", b, "127.0.0.1", 0)
	if err != nil || second.Token == first.Token {
		t.Fatalf("second activation failed to refresh key after top-up: %v", err)
	}
	if err := db.First(&state, item.ID).Error; err != nil || state.AvailableMicros != 60_000_000 {
		t.Fatalf("second activation should restore exactly ¥60: %d err=%v", state.AvailableMicros, err)
	}
	if err := db.First(&owner, owner.ID).Error; err != nil || owner.Balance != 0 {
		t.Fatalf("top-up must use exactly the available wallet: %v err=%v", owner.Balance, err)
	}
	if retry, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "managed-second", b, "127.0.0.1", 0); err != nil || retry.Token != second.Token {
		t.Fatalf("retry must not require another transfer: %v", err)
	}
}

func TestManagedActivationCodeBindsOnlyOneScriptAndOneRun(t *testing.T) {
	db, owner, admin, version := newManagedScriptFixture(t, 1)
	if _, err := SetScriptActivationCodePriceWithDB(db, admin.ID, "1"); err != nil {
		t.Fatal(err)
	}
	purchase, err := PurchaseScriptActivationCodeWithDB(db, owner.ID, "fixture-purchase", []byte(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	grant, err := RedeemScriptActivationCodeWithDB(db, owner.ID, purchase.Code)
	if err != nil {
		t.Fatal(err)
	}
	input := CreateUserScriptInput{Name: "fixture", VersionID: version.ID, BaseDomain: "lease.example",
		BillingMode: entity.ScriptBillingModeActivation, EntitlementID: grant.ID}
	item, err := CreateUserScriptWithDB(db, owner, input)
	if err != nil || item.ReservedFee != 0 {
		t.Fatalf("create at empty wallet must not freeze: %v", err)
	}
	if _, err := CreateUserScriptWithDB(db, owner, input); !errors.Is(err, ErrScriptActivationEntitlementUnavailable) {
		t.Fatalf("same entitlement created a second script: %v", err)
	}
	exchange, err := IssueScriptExchangeTokenWithDB(db, owner, item.ID, 0)
	if err != nil {
		t.Fatalf("empty wallet must not block activation mode: %v", err)
	}
	ticket, err := ExchangeScriptTokenWithDB(db, item.ShortCode, exchange.Token)
	if err != nil {
		t.Fatal(err)
	}
	a, b := strings.Repeat("12", 32), strings.Repeat("34", 32)
	first, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, ticket.ActivationToken, a, "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, ticket.ActivationToken, a, "127.0.0.1", 0)
	if err != nil || retry.Token != first.Token {
		t.Fatalf("same activation retry should return original token: %v", err)
	}
	if _, err := IssueScriptExchangeTokenWithDB(db, owner, item.ID, 0); !errors.Is(err, ErrScriptActivationRunConsumed) {
		t.Fatalf("a consumed script must not issue another launcher: %v", err)
	}
	if err := db.Model(&entity.ScriptRunSession{}).Where("script_id = ?", item.ID).
		Update("last_heartbeat_at", time.Now().Add(-ScriptHeartbeatTimeout)).Error; err != nil {
		t.Fatal(err)
	}
	insertLeaseTicket(t, db, item, "managed-alternate")
	if _, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "managed-alternate", b, "127.0.0.1", 0); !errors.Is(err, ErrScriptActivationRunConsumed) {
		t.Fatalf("expired single-use script still must not start again: %v", err)
	}
}
