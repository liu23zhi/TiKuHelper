package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

var syntheticActivationSigningKey = []byte("unit-test-script-activation-signing-key-32")

func activationEntitlementFixture(t *testing.T) (*gorm.DB, entity.User) {
	t.Helper()
	db := newScriptTestDB(t)
	if err := db.AutoMigrate(&entity.ScriptActivationEntitlement{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	user := entity.User{Username: "activation-buyer", Role: "user", Status: "enabled", Balance: 50}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return db, user
}

func setSyntheticActivationPrice(t *testing.T, db *gorm.DB, value string) {
	t.Helper()
	s, exists, err := getSystemSettingByKey(db, ScriptActivationCodePriceSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		err = db.Model(&s).Update("value", value).Error
	} else {
		err = db.Create(&entity.SystemSetting{Key: ScriptActivationCodePriceSettingKey, Value: value}).Error
	}
	if err != nil {
		t.Fatal(err)
	}
}

func assertActivationWallet(t *testing.T, db *gorm.DB, userID int32, balance float64, bills int64) {
	t.Helper()
	var user entity.User
	if err := db.First(&user, userID).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&entity.BillingRecord{}).Where("user_id = ? AND type = ?", userID, ScriptActivationPurchaseBillingType).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if user.Balance != balance || count != bills {
		t.Fatalf("balance=%v billing records=%d; want %v/%d", user.Balance, count, balance, bills)
	}
}

func TestScriptActivationPurchaseIsFailClosedAndIdempotent(t *testing.T) {
	db, user := activationEntitlementFixture(t)
	for _, value := range []string{"", "0", "-1", "NaN", "Inf", "1e-3", "1.0000001", "1000000000"} {
		if value != "" {
			setSyntheticActivationPrice(t, db, value)
		}
		if _, err := PurchaseScriptActivationCodeWithDB(db, user.ID, "buy-1", syntheticActivationSigningKey); !errors.Is(err, ErrScriptActivationSalesDisabled) {
			t.Fatalf("price %q: wanted disabled, got %v", value, err)
		}
		assertActivationWallet(t, db, user.ID, 50, 0)
	}
	setSyntheticActivationPrice(t, db, "50.000001")
	if _, err := PurchaseScriptActivationCodeWithDB(db, user.ID, "buy-1", syntheticActivationSigningKey); !errors.Is(err, ErrScriptBalanceInsufficient) {
		t.Fatalf("expected insufficient funds, got %v", err)
	}
	assertActivationWallet(t, db, user.ID, 50, 0)

	setSyntheticActivationPrice(t, db, "12.345678")
	purchase, err := PurchaseScriptActivationCodeWithDB(db, user.ID, "buy-1", syntheticActivationSigningKey)
	if err != nil || purchase.EntitlementID <= 0 || purchase.Price != 12.345678 || len(purchase.Code) != 68 {
		t.Fatalf("bad purchase shape; id=%v price=%v code length=%d error=%v", purchase.EntitlementID, purchase.Price, len(purchase.Code), err)
	}
	assertActivationWallet(t, db, user.ID, 37.654322, 1)
	setSyntheticActivationPrice(t, db, "0")
	replay, err := PurchaseScriptActivationCodeWithDB(db, user.ID, "buy-1", syntheticActivationSigningKey)
	if err != nil || replay != purchase {
		t.Fatalf("retry changed receipt: %v", err)
	}
	if _, err := PurchaseScriptActivationCodeWithDB(db, user.ID, "buy-1", []byte("rotated-unit-test-activation-signing-key")); !errors.Is(err, ErrScriptActivationReceiptUnavailable) {
		t.Fatalf("unexpected key rotation result: %v", err)
	}
	assertActivationWallet(t, db, user.ID, 37.654322, 1)

	var stored entity.ScriptActivationEntitlement
	if err := db.First(&stored, purchase.EntitlementID).Error; err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CodeHash != hashScriptSecret(purchase.Code) || stored.CodeHash == purchase.Code || strings.Contains(string(encoded), stored.CodeHash) || strings.Contains(string(encoded), purchase.Code) || strings.Contains(string(encoded), stored.PurchaseNonce) {
		t.Fatal("credential material leaked or not hashed")
	}
}

func TestScriptActivationPurchaseConcurrentRetriesChargeExactlyOnce(t *testing.T) {
	db, user := activationEntitlementFixture(t)
	setSyntheticActivationPrice(t, db, "1.000001")
	results := make(chan ScriptActivationCodePurchase, 8)
	errs := make(chan error, 8)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := PurchaseScriptActivationCodeWithDB(db, user.ID, "same-idempotency-key", syntheticActivationSigningKey)
			results <- result
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent retry failed: %v", err)
		}
	}
	var first ScriptActivationCodePurchase
	for result := range results {
		if first.EntitlementID == 0 {
			first = result
		}
		if result != first {
			t.Fatal("same-idempotency retry minted a different grant")
		}
	}
	assertActivationWallet(t, db, user.ID, 48.999999, 1)
}

func TestScriptActivationEntitlementOwnerBindAndRunOnce(t *testing.T) {
	db, user := activationEntitlementFixture(t)
	setSyntheticActivationPrice(t, db, "2.5")
	purchase, err := PurchaseScriptActivationCodeWithDB(db, user.ID, "entitlement", syntheticActivationSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	other := entity.User{Username: "other-buyer", Role: "user", Status: "enabled", Balance: 20}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := RedeemScriptActivationCodeWithDB(db, other.ID, purchase.Code); !errors.Is(err, ErrScriptActivationCodeInvalid) {
		t.Fatalf("unexpected wrong-owner result: %v", err)
	}
	// Purchase and redemption do not require either binding. Actual script
	// creation validates both in CreateUserScriptWithDB before this helper.
	redeemed, err := RedeemScriptActivationCodeWithDB(db, user.ID, purchase.Code)
	if err != nil || redeemed.ID != purchase.EntitlementID || redeemed.RedeemedAt == nil {
		t.Fatalf("redemption failed: %v", err)
	}
	replay, err := RedeemScriptActivationCodeWithDB(db, user.ID, purchase.Code)
	if err != nil || replay.ID != redeemed.ID || !replay.RedeemedAt.Equal(*redeemed.RedeemedAt) {
		t.Fatalf("redemption retry changed entitlement: %v", err)
	}

	script := entity.UserScript{UserID: user.ID, VersionID: 1, ShortCode: "entitled-1", Name: "activation", Enabled: true}
	otherScript := entity.UserScript{UserID: user.ID, VersionID: 1, ShortCode: "entitled-2", Name: "second", Enabled: true}
	for _, item := range []*entity.UserScript{&script, &otherScript} {
		if err := db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return BindScriptActivationEntitlementInTx(tx, user.ID, redeemed.ID, script.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return BindScriptActivationEntitlementInTx(tx, user.ID, redeemed.ID, otherScript.ID)
	}); !errors.Is(err, ErrScriptActivationEntitlementUnavailable) {
		t.Fatalf("entitlement bound to second script: %v", err)
	}
	if _, found, err := GetScriptActivationEntitlementByScriptWithDB(db, other.ID, script.ID); err != nil || found {
		t.Fatalf("wrong owner found entitlement: %v", err)
	}
	if _, found, err := GetScriptActivationEntitlementByScriptWithDB(db, user.ID, script.ID); err != nil || !found {
		t.Fatalf("missing bound entitlement: %v", err)
	}
	now := time.Now()
	firstTicket := entity.ScriptExchangeToken{ScriptID: script.ID, Purpose: "activation", TokenHash: strings.Repeat("1", 64), ExpiresAt: now.Add(time.Minute)}
	secondTicket := entity.ScriptExchangeToken{ScriptID: script.ID, Purpose: "activation", TokenHash: strings.Repeat("2", 64), ExpiresAt: now.Add(time.Minute)}
	for _, ticket := range []*entity.ScriptExchangeToken{&firstTicket, &secondTicket} {
		if err := db.Create(ticket).Error; err != nil {
			t.Fatal(err)
		}
	}
	session := entity.ScriptRunSession{ScriptID: script.ID, UserID: user.ID, AccessTokenID: 1, TokenHash: strings.Repeat("3", 64), IdentityHash: strings.Repeat("4", 64), ActivatedAt: now, ExpiresAt: now.Add(time.Minute)}
	syntheticRollback := errors.New("synthetic outer transaction rollback")
	if err := db.Transaction(func(tx *gorm.DB) error {
		candidate := session
		if err := tx.Create(&candidate).Error; err != nil {
			return err
		}
		if _, err := ConsumeScriptActivationEntitlementRunInTx(tx, user.ID, script.ID, firstTicket.ID, candidate.ID); err != nil {
			return err
		}
		return syntheticRollback
	}); !errors.Is(err, syntheticRollback) {
		t.Fatalf("synthetic rollback failed: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		replayID, err := ResolveScriptActivationEntitlementRunInTx(tx, user.ID, script.ID, firstTicket.ID)
		if err != nil || replayID != 0 {
			return fmt.Errorf("rolled back activation consumed grant: %v", err)
		}
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		usedID, err := ConsumeScriptActivationEntitlementRunInTx(tx, user.ID, script.ID, firstTicket.ID, session.ID)
		if err != nil || usedID != session.ID {
			return fmt.Errorf("activation did not bind new session: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		replayID, err := ResolveScriptActivationEntitlementRunInTx(tx, user.ID, script.ID, firstTicket.ID)
		if err != nil || replayID != session.ID {
			return fmt.Errorf("exact retry lost original session: %v", err)
		}
		_, err = ResolveScriptActivationEntitlementRunInTx(tx, user.ID, script.ID, secondTicket.ID)
		if !errors.Is(err, ErrScriptActivationRunConsumed) {
			return fmt.Errorf("new ticket could start second run: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertActivationWallet(t, db, user.ID, 47.5, 1)
}

func TestScriptActivationPurchaseRollsBackIfLedgerWriteFails(t *testing.T) {
	db, user := activationEntitlementFixture(t)
	setSyntheticActivationPrice(t, db, "12")
	syntheticFailure := errors.New("synthetic ledger failure")
	if err := db.Callback().Create().Before("gorm:create").Register("synthetic_activation_ledger_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameBillingRecord {
			_ = tx.AddError(syntheticFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := PurchaseScriptActivationCodeWithDB(db, user.ID, "retry-after-failure", syntheticActivationSigningKey); !errors.Is(err, syntheticFailure) {
		t.Fatalf("expected synthetic ledger failure: %v", err)
	}
	assertActivationWallet(t, db, user.ID, 50, 0)
	var count int64
	if err := db.Model(&entity.ScriptActivationEntitlement{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rolled back purchase left entitlement: count=%d err=%v", count, err)
	}
}
