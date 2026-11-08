package service

import (
	"errors"
	"math"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newScriptEscrowFixture(t *testing.T, wallet string) (*gorm.DB, entity.User, entity.UserScript) {
	t.Helper()
	db := newScriptTestDB(t)
	if err := db.AutoMigrate(&entity.ScriptEscrow{}, &entity.ScriptEscrowEntry{}); err != nil {
		t.Fatal(err)
	}
	owner := entity.User{Username: "synthetic-escrow-owner", Role: "user", Status: "enabled"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&owner).Update("balance", wallet).Error; err != nil {
		t.Fatal(err)
	}
	script := entity.UserScript{UserID: owner.ID, VersionID: 1, Name: "synthetic-escrow-script", ShortCode: "test01", AccessKey: "not-a-credential"}
	if err := db.Create(&script).Error; err != nil {
		t.Fatal(err)
	}
	return db, owner, script
}

func scriptEscrowTx(t *testing.T, db *gorm.DB, fn func(*gorm.DB) (ScriptEscrowMovement, error)) (ScriptEscrowMovement, error) {
	t.Helper()
	var got ScriptEscrowMovement
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		got, err = fn(tx)
		return err
	})
	return got, err
}

func assertScriptEscrowState(t *testing.T, db *gorm.DB, userID, scriptID int32, wallet, escrow int64, entries int64) {
	t.Helper()
	var count int64
	if err := db.Model(&entity.ScriptEscrowEntry{}).Where("script_id = ?", scriptID).Count(&count).Error; err != nil || count != entries {
		t.Fatalf("entries = %d (%v), want %d", count, err, entries)
	}
	var row entity.ScriptEscrow
	err := db.First(&row, scriptID).Error
	if escrow < 0 {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("want no escrow row, got %+v (%v)", row, err)
		}
	} else if err != nil || row.AvailableMicros != escrow || row.UserID != userID {
		t.Fatalf("escrow=%+v (%v), want %d owner %d", row, err, escrow, userID)
	}
	read, err := lockScriptEscrowWallet(db, userID)
	if err != nil || read.micros != wallet {
		t.Fatalf("wallet=%+v (%v), want %d", read, err, wallet)
	}
}

func TestScriptEscrowPolicyDefaultsAndAdminMutation(t *testing.T) {
	db, user, _ := newScriptEscrowFixture(t, "80")
	if got, err := GetScriptEscrowPolicyWithDB(db); err != nil || got != (ScriptEscrowPolicy{80_000_000, 60_000_000, 1_000_000}) {
		t.Fatalf("default = %+v, %v", got, err)
	}
	policy := ScriptEscrowPolicy{100_000_001, 70_000_001, 1_250_001}
	if err := UpdateScriptEscrowPolicyWithDB(db, user.ID, policy); !errors.Is(err, ErrScriptForbidden) {
		t.Fatalf("non-admin policy update: %v", err)
	}
	admin := entity.User{Username: "synthetic-policy-admin", Role: "admin", Status: "enabled"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := UpdateScriptEscrowPolicyWithDB(db, admin.ID, policy); err != nil {
		t.Fatal(err)
	}
	if got, err := GetScriptEscrowPolicyWithDB(db); err != nil || got != policy {
		t.Fatalf("configured = %+v, %v", got, err)
	}
	policy.MinimumMicros = policy.TargetMicros + 1
	if err := UpdateScriptEscrowPolicyWithDB(db, admin.ID, policy); !errors.Is(err, ErrScriptEscrowInvalid) {
		t.Fatalf("bad thresholds: %v", err)
	}
	if err := db.Model(&entity.SystemSetting{}).Where(map[string]any{"key": ScriptEscrowPolicySettingKey}).Update("value", "{}").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := GetScriptEscrowPolicyWithDB(db); !errors.Is(err, ErrScriptEscrowInvalid) {
		t.Fatalf("corrupt policy silently accepted: %v", err)
	}
}

func TestScriptEscrowFreezeIsOwnedExactAndTransactional(t *testing.T) {
	db, user, script := newScriptEscrowFixture(t, "79.999999")
	freeze := func(tx *gorm.DB) (ScriptEscrowMovement, error) {
		return FreezeScriptEscrowOnCreateInTx(tx, script.ID, user.ID)
	}
	if _, err := freeze(db); !errors.Is(err, ErrScriptEscrowTransaction) {
		t.Fatalf("non-transactional write allowed: %v", err)
	}
	if _, err := scriptEscrowTx(t, db, freeze); !errors.Is(err, ErrScriptEscrowInsufficient) {
		t.Fatalf("underfunded creation: %v", err)
	}
	assertScriptEscrowState(t, db, user.ID, script.ID, 79_999_999, -1, 0)
	if err := db.Model(&user).Update("balance", "80.000001").Error; err != nil {
		t.Fatal(err)
	}
	failAfter := errors.New("synthetic outer create failure")
	err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := freeze(tx); err != nil {
			return err
		}
		return failAfter
	})
	if !errors.Is(err, failAfter) {
		t.Fatalf("freeze should participate in outer transaction: %v", err)
	}
	assertScriptEscrowState(t, db, user.ID, script.ID, 80_000_001, -1, 0)
	got, err := scriptEscrowTx(t, db, freeze)
	if err != nil || got != (ScriptEscrowMovement{AmountMicros: 80_000_000, EscrowAfterMicros: 80_000_000, WalletBeforeMicros: 80_000_001, WalletAfterMicros: 1}) {
		t.Fatalf("freeze = %+v, %v", got, err)
	}
	replay, err := scriptEscrowTx(t, db, freeze)
	if err != nil || !replay.Replayed {
		t.Fatalf("retry = %+v, %v", replay, err)
	}
	assertScriptEscrowState(t, db, user.ID, script.ID, 1, 80_000_000, 1)
	var after entity.UserScript
	if err := db.First(&after, script.ID).Error; err != nil || after.ReservedFee != script.ReservedFee {
		t.Fatalf("legacy reservation field changed: %v, %v", after.ReservedFee, err)
	}
}

func TestScriptEscrowLaunchChecksMinimumAndDoesNotPartiallyFundDenial(t *testing.T) {
	for _, tc := range []struct {
		name        string
		wallet      string
		startWallet int64
		remaining   int64
		auto        bool
		wantErr     bool
		afterWallet int64
		afterEscrow int64
	}{
		{"one-micro-short", "5.000000", 5_000_000, 54_999_999, true, true, 5_000_000, 54_999_999},
		{"exactly-minimum", "5.000001", 5_000_001, 54_999_999, true, false, 0, 60_000_000},
		{"replenish-target", "30.000001", 30_000_001, 55_000_000, true, false, 5_000_001, 80_000_000},
		{"disabled-below-minimum", "80", 80_000_000, 59_999_999, false, true, 80_000_000, 59_999_999},
		{"disabled-at-minimum", "9", 9_000_000, 60_000_000, false, false, 9_000_000, 60_000_000},
		{"negative-at-target", "-0.000001", -1, 80_000_000, true, true, -1, 80_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, user, script := newScriptEscrowFixture(t, tc.wallet)
			row := entity.ScriptEscrow{ScriptID: script.ID, UserID: user.ID, AvailableMicros: tc.remaining, Revision: 1}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			launch := func(tx *gorm.DB) (ScriptEscrowMovement, error) {
				return PrepareScriptEscrowLaunchInTx(tx, script.ID, user.ID, tc.auto, "synthetic-activation-1")
			}
			got, err := scriptEscrowTx(t, db, launch)
			if tc.wantErr {
				if !errors.Is(err, ErrScriptEscrowInsufficient) {
					t.Fatalf("expected denied launch, got %+v, %v", got, err)
				}
				assertScriptEscrowState(t, db, user.ID, script.ID, tc.afterWallet, tc.afterEscrow, 0)
				return
			}
			if err != nil || got.WalletBeforeMicros != tc.startWallet || got.EscrowAfterMicros != tc.afterEscrow || got.WalletAfterMicros != tc.afterWallet {
				t.Fatalf("launch = %+v, %v", got, err)
			}
			replay, err := scriptEscrowTx(t, db, launch)
			if err != nil || !replay.Replayed {
				t.Fatalf("activation retry = %+v, %v", replay, err)
			}
			assertScriptEscrowState(t, db, user.ID, script.ID, tc.afterWallet, tc.afterEscrow, 1)
		})
	}
}

func TestScriptEscrowSettlementPrioritizesEscrowAndAllowsExecutedDebt(t *testing.T) {
	db, user, script := newScriptEscrowFixture(t, "2.000001")
	if err := db.Create(&entity.ScriptEscrow{ScriptID: script.ID, UserID: user.ID, AvailableMicros: 3_000_001, Revision: 1}).Error; err != nil {
		t.Fatal(err)
	}
	settle := func(tx *gorm.DB) (ScriptEscrowMovement, error) {
		return SettleScriptEscrowInTx(tx, script.ID, user.ID, "synthetic-search-1", 5.000003)
	}
	got, err := scriptEscrowTx(t, db, settle)
	if err != nil || got != (ScriptEscrowMovement{AmountMicros: 5_000_003, EscrowBeforeMicros: 3_000_001, WalletBeforeMicros: 2_000_001, WalletAfterMicros: -1}) {
		t.Fatalf("settle = %+v, %v", got, err)
	}
	replay, err := scriptEscrowTx(t, db, settle)
	if err != nil || !replay.Replayed || replay.WalletAfterMicros != -1 {
		t.Fatalf("settlement retry = %+v, %v", replay, err)
	}
	_, err = scriptEscrowTx(t, db, func(tx *gorm.DB) (ScriptEscrowMovement, error) {
		return SettleScriptEscrowMicrosInTx(tx, script.ID, user.ID, "synthetic-search-1", 1)
	})
	if !errors.Is(err, ErrScriptEscrowConflict) {
		t.Fatalf("settlement mismatch = %v", err)
	}
	_, err = scriptEscrowTx(t, db, func(tx *gorm.DB) (ScriptEscrowMovement, error) {
		return SettleScriptEscrowInTx(tx, script.ID, user.ID, "invalid-nonfinite", math.Inf(1))
	})
	if !errors.Is(err, ErrScriptEscrowInvalid) {
		t.Fatalf("non-finite settlement = %v", err)
	}
	assertScriptEscrowState(t, db, user.ID, script.ID, -1, 0, 1)
}

func TestScriptEscrowDoesNotAdoptLegacyReservations(t *testing.T) {
	db, user, script := newScriptEscrowFixture(t, "100")
	_, err := scriptEscrowTx(t, db, func(tx *gorm.DB) (ScriptEscrowMovement, error) {
		return PrepareScriptEscrowLaunchInTx(tx, script.ID, user.ID, true, "legacy-launch")
	})
	if !errors.Is(err, ErrScriptEscrowMissing) {
		t.Fatalf("legacy script should not be charged/funded: %v", err)
	}
	assertScriptEscrowState(t, db, user.ID, script.ID, 100_000_000, -1, 0)
}
