package service

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func activateEscrowCloseFixture(t *testing.T) (*gorm.DB, entity.User, ScriptListItem, entity.ScriptRunSession, ScriptRunSessionGrant) {
	t.Helper()
	db, owner, _, version := newManagedScriptFixture(t, 100)
	item, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{
		Name: "close fixture", VersionID: version.ID, BaseDomain: "lease.example", BillingMode: entity.ScriptBillingModeEscrow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !item.EscrowWorkTracked {
		t.Fatal("new escrow script must have trusted admission tracking")
	}
	insertLeaseTicket(t, db, item, "synthetic-close-ticket")
	grant, err := ActivateLeasedScriptRunSessionWithDB(db, item.ShortCode, "synthetic-close-ticket", strings.Repeat("ab", 32), "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	var run entity.ScriptRunSession
	if err := db.Where("script_id = ?", item.ID).Take(&run).Error; err != nil {
		t.Fatal(err)
	}
	return db, owner, item, run, grant
}

func TestUsedEscrowCloseConfirmsCurrentLeaseAndWaitsForSettlement(t *testing.T) {
	db, owner, item, run, grant := activateEscrowCloseFixture(t)
	const requestID = "synthetic-prior-search"
	if err := BeginScriptEscrowWorkWithDB(db, item.ID, requestID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := CloseUserScriptWithDB(db, entity.User{ID: owner.ID + 100, Role: "user"}, item.ID, run.ID); !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("non-owner close must be rejected, got %v", err)
	}
	for _, supplied := range []int64{0, run.ID + 1} {
		result, err := CloseUserScriptWithDB(db, owner, item.ID, supplied)
		if !errors.Is(err, ErrScriptActiveCloseConfirmationRequired) || result.ActiveRunSessionID != run.ID {
			t.Fatalf("expected current confirmation hint for %d: %+v %v", supplied, result, err)
		}
	}
	if _, _, err := ResolveScriptRunSessionWithDB(db, grant.Token, strings.Repeat("ab", 32), "127.0.0.1"); err != nil {
		t.Fatalf("confirmation challenge changed runtime: %v", err)
	}
	result, err := CloseUserScriptWithDB(db, owner, item.ID, run.ID)
	if err != nil || result.Status != ScriptCloseStatusSettling || result.NeedsReconciliation {
		t.Fatalf("confirmed close should stop immediately without early refund: %+v %v", result, err)
	}
	var key entity.AccessToken
	if err := db.First(&key, item.AccessTokenID).Error; err != nil || key.Status != "disabled" ||
		key.TokenValue != "" || key.TokenPrefix != "" {
		t.Fatalf("dedicated credential was not disabled and scrubbed: %v", err)
	}
	if _, err := HeartbeatScriptRunSessionWithDB(db, item.ShortCode, grant.Token, strings.Repeat("ab", 32), "127.0.0.1"); !errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		t.Fatalf("closed session must reject heartbeat: %v", err)
	}
	if err := BeginScriptEscrowWorkWithDB(db, item.ID, "synthetic-late-search", run.ID); !errors.Is(err, ErrScriptEscrowWorkDenied) {
		t.Fatalf("new work after close must be denied: %v", err)
	}
	enabled := true
	if _, err := UpdateUserScriptWithDB(db, owner, item.ID, UpdateUserScriptInput{BaseDomain: "lease.example", Enabled: &enabled}); !errors.Is(err, ErrScriptEscrowClosePending) {
		t.Fatalf("settling script must not be re-enabled: %v", err)
	}
	if err := FinalizeClosingScriptEscrowsWithDB(db, 32); err != nil {
		t.Fatal(err)
	}
	assertEscrowClosingBalances(t, db, owner.ID, item.ID, 20, 80_000_000, false)
	if err := db.Transaction(func(tx *gorm.DB) error {
		if _, err := SettleScriptEscrowMicrosInTx(tx, item.ID, owner.ID, requestID, 13_250_000); err != nil {
			return err
		}
		return CompleteScriptEscrowWorkInTx(tx, item.ID, requestID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeClosingScriptEscrowsWithDB(db, 32); err != nil {
		t.Fatal(err)
	}
	result, err = CloseUserScriptWithDB(db, owner, item.ID, run.ID)
	if err != nil || result.Status != ScriptCloseStatusRefunded || result.RefundedMicros != 66_750_000 {
		t.Fatalf("idempotent close result should reflect remaining refund: %+v %v", result, err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := SettleScriptEscrowMicrosInTx(tx, item.ID, owner.ID, "synthetic-untracked-late-charge", 1)
		return err
	}); !errors.Is(err, ErrScriptEscrowClosePending) {
		t.Fatalf("new charge must not debit a released escrow: %v", err)
	}
	assertEscrowClosingBalances(t, db, owner.ID, item.ID, 86.75, 0, true)
}

func TestUsedEscrowCloseDoesNotInferOldHistoryWasSettled(t *testing.T) {
	db, owner, item, run, _ := activateEscrowCloseFixture(t)
	if err := db.Model(&entity.UserScript{}).Where("id = ?", item.ID).Update("escrow_work_tracked", false).Error; err != nil {
		t.Fatal(err)
	}
	result, err := CloseUserScriptWithDB(db, owner, item.ID, run.ID)
	if err != nil || result.Status != ScriptCloseStatusSettling || !result.NeedsReconciliation {
		t.Fatalf("untracked history must wait for reconciliation: %+v %v", result, err)
	}
	if err := FinalizeClosingScriptEscrowsWithDB(db, 32); err != nil {
		t.Fatal(err)
	}
	assertEscrowClosingBalances(t, db, owner.ID, item.ID, 20, 80_000_000, false)
}

func TestUsedEscrowRefundFailureRetainsRevocationAndRetryRefundsOnce(t *testing.T) {
	db, owner, item, run, _ := activateEscrowCloseFixture(t)
	result, err := CloseUserScriptWithDB(db, owner, item.ID, run.ID)
	if err != nil || result.Status != ScriptCloseStatusSettling {
		t.Fatalf("close: %+v %v", result, err)
	}
	const callback = "test:escrow-release-failure"
	injected := errors.New("synthetic release ledger failure")
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if entry, ok := tx.Statement.Dest.(*entity.ScriptEscrowEntry); ok && entry.Kind == scriptEscrowRelease {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeClosingScriptEscrowsWithDB(db, 32); !errors.Is(err, injected) {
		t.Fatalf("refund failure was not returned: %v", err)
	}
	assertEscrowClosingBalances(t, db, owner.ID, item.ID, 20, 80_000_000, false)
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}

	// Both workers may see the same candidate. On SQLite a contending writer
	// may be rejected with a busy/locked error; either way money moves once.
	start, errs := make(chan struct{}), make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- FinalizeClosingScriptEscrowsWithDB(db, 32)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !strings.Contains(strings.ToLower(err.Error()), "lock") && !strings.Contains(strings.ToLower(err.Error()), "busy") {
			t.Fatal(err)
		}
	}
	if err := FinalizeClosingScriptEscrowsWithDB(db, 32); err != nil {
		t.Fatal(err)
	}
	assertEscrowClosingBalances(t, db, owner.ID, item.ID, 100, 0, true)
}

func assertEscrowClosingBalances(t *testing.T, db *gorm.DB, userID, scriptID int32, wallet float64, escrow int64, deleted bool) {
	t.Helper()
	var account entity.User
	var funds entity.ScriptEscrow
	var script entity.UserScript
	for _, item := range []struct {
		value any
		id    int32
	}{{&account, userID}, {&funds, scriptID}, {&script, scriptID}} {
		if err := db.First(item.value, item.id).Error; err != nil {
			t.Fatal(err)
		}
	}
	if account.Balance != wallet || funds.AvailableMicros != escrow || script.Enabled ||
		script.EscrowClosingAt == nil || (script.DeletedAt != nil) != deleted {
		t.Fatalf("unexpected close state: wallet=%v escrow=%d enabled=%t closing=%t deleted=%t",
			account.Balance, funds.AvailableMicros, script.Enabled, script.EscrowClosingAt != nil, script.DeletedAt != nil)
	}
	var count int64
	if err := db.Model(&entity.ScriptEscrowEntry{}).Where("script_id = ? AND kind = ?", scriptID, scriptEscrowRelease).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if (deleted && count != 1) || (!deleted && count != 0) {
		t.Fatalf("wrong release receipt count: %d", count)
	}
}
