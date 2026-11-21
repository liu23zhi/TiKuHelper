package service

import (
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestScriptActivationPurchaseHistoryReturnsOnlyPurchasersWalletReceipts(t *testing.T) {
	db, buyer := activationEntitlementFixture(t)
	recipient := newActivationRecipient(t, db)
	setSyntheticActivationPrice(t, db, "2.5")
	first, err := PurchaseScriptActivationCodeWithDB(db, buyer.ID, "first", syntheticActivationSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := PurchaseScriptActivationCodeWithDB(db, buyer.ID, "shared", syntheticActivationSigningKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.AdminScriptActivationCode{}); err != nil {
		t.Fatal(err)
	}
	adminCodes, err := GenerateAdminScriptActivationCodes(db, buyer.ID, 1, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RedeemScriptActivationCodeWithDB(db, buyer.ID, adminCodes[0].Code); err != nil {
		t.Fatal(err)
	}
	grant, err := RedeemScriptActivationCodeWithDB(db, recipient.ID, shared.Code)
	if err != nil {
		t.Fatal(err)
	}
	now := *grant.RedeemedAt
	clock := func(at time.Time) *gorm.DB {
		return db.Session(&gorm.Session{NowFunc: func() time.Time { return at }})
	}
	history, err := ListScriptActivationCodePurchasesWithDB(clock(now), buyer.ID, syntheticActivationSigningKey, 1, 1)
	if err != nil || history.Total != 2 || len(history.Items) != 1 {
		t.Fatalf("expected paginated wallet purchases, got %d rows, total %d: %v", len(history.Items), history.Total, err)
	}
	item := history.Items[0]
	if item.EntitlementID != shared.EntitlementID || item.Code != shared.Code || item.Price != shared.Price || item.Status != "frozen" || item.FrozenUntil == nil || !item.FrozenUntil.Equal(now.Add(30*time.Minute)) {
		t.Fatal("history omitted the shared code, amount, or current deadline")
	}
	second, err := ListScriptActivationCodePurchasesWithDB(clock(now), buyer.ID, syntheticActivationSigningKey, 2, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0].EntitlementID != first.EntitlementID {
		t.Fatalf("second page did not contain the earlier purchase: %v", err)
	}
	recipientHistory, err := ListScriptActivationCodePurchasesWithDB(clock(now), recipient.ID, syntheticActivationSigningKey, 1, 1)
	if err != nil || recipientHistory.Total != 0 || len(recipientHistory.Items) != 0 {
		t.Fatalf("recipient could access a wallet receipt paid by another user: %v", err)
	}
	atExpiry := clock(now.Add(30 * time.Minute))
	expired, err := ListScriptActivationCodePurchasesWithDB(atExpiry, buyer.ID, []byte("rotated-activation-signing-key-example"), 1, 1)
	if err != nil || len(expired.Items) != 1 {
		t.Fatalf("rotated signing key hid purchase history: %v", err)
	}
	item = expired.Items[0]
	if item.Status != "unused" || item.RedeemedAt != nil || item.FrozenUntil != nil || item.Code != "" || item.ReceiptError == "" {
		t.Fatal("history did not release the reservation or flag the unavailable receipt")
	}
}

func TestAdminActivationCodeFreezesForExactlyThirtyMinutesAndStaysUsedAfterBinding(t *testing.T) {
	db, buyer := activationEntitlementFixture(t)
	recipient := newActivationRecipient(t, db)
	if err := db.AutoMigrate(&entity.AdminScriptActivationCode{}); err != nil {
		t.Fatal(err)
	}
	codes, err := GenerateAdminScriptActivationCodes(db, buyer.ID, 1, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	clock := func(at time.Time) *gorm.DB {
		return db.Session(&gorm.Session{NowFunc: func() time.Time { return at }})
	}
	grant, err := RedeemScriptActivationCodeWithDB(clock(start), buyer.ID, codes[0].Code)
	if err != nil || grant.FrozenUntil == nil || !grant.FrozenUntil.Equal(start.Add(30*time.Minute)) {
		t.Fatalf("initial claim did not start a 30-minute freeze: %v", err)
	}
	retry, err := RedeemScriptActivationCodeWithDB(clock(start.Add(time.Minute)), buyer.ID, codes[0].Code)
	if err != nil || retry.FrozenUntil == nil || !retry.FrozenUntil.Equal(*grant.FrozenUntil) {
		t.Fatalf("idempotent retry changed the deadline: %v", err)
	}
	beforeDeadline := clock(start.Add(29*time.Minute + 59*time.Second))
	if _, err := RedeemScriptActivationCodeWithDB(beforeDeadline, recipient.ID, codes[0].Code); !errors.Is(err, ErrScriptActivationCodeFrozen) {
		t.Fatalf("a second account could claim during the freeze: %v", err)
	}
	frozen, err := ListAdminScriptActivationCodes(beforeDeadline, AdminScriptActivationCodeListQuery{Status: "frozen", UsedBy: buyer.ID})
	if err != nil || frozen.Total != 1 {
		t.Fatalf("admin list did not show the active reservation: %v", err)
	}
	atDeadline := clock(start.Add(30 * time.Minute))
	unused, err := ListAdminScriptActivationCodes(atDeadline, AdminScriptActivationCodeListQuery{Status: "unused"})
	if err != nil || len(unused.Items) != 1 || unused.Items[0].UsedAt != nil || unused.Items[0].UsedBy != nil {
		t.Fatalf("admin list advertised expired reservation use: %v", err)
	}
	reclaimed, err := RedeemScriptActivationCodeWithDB(atDeadline, recipient.ID, codes[0].Code)
	if err != nil || reclaimed.RedeemerID == nil || *reclaimed.RedeemerID != recipient.ID {
		t.Fatalf("second account could not claim at the deadline: %v", err)
	}
	script := entity.UserScript{UserID: recipient.ID, VersionID: 1, ShortCode: "admin-reclaim", Name: "admin-reclaim", Enabled: true}
	if err := db.Create(&script).Error; err != nil {
		t.Fatal(err)
	}
	if err := atDeadline.Transaction(func(tx *gorm.DB) error {
		return BindScriptActivationEntitlementInTx(tx, recipient.ID, reclaimed.ID, script.ID)
	}); err != nil {
		t.Fatal(err)
	}
	afterSecondDeadline := clock(start.Add(61 * time.Minute))
	if _, err := RedeemScriptActivationCodeWithDB(afterSecondDeadline, buyer.ID, codes[0].Code); !errors.Is(err, ErrAdminScriptActivationCodeUsed) {
		t.Fatalf("bound admin code became available again: %v", err)
	}
	stored, err := GetAdminScriptActivationCodeByID(afterSecondDeadline, codes[0].ID)
	if err != nil || stored.UsedScriptID == nil || *stored.UsedScriptID != script.ID {
		t.Fatalf("admin record lost the permanent binding: %v", err)
	}
	usages, err := ListAdminScriptActivationCodeUsages(afterSecondDeadline, stored)
	if err != nil || len(usages) != 2 || usages[0].Kind != "redeem" || usages[1].Kind != "bind" {
		t.Fatalf("admin usage does not accurately show redemption and binding: %v", err)
	}
}

func TestScriptActivationCodeConcurrentClaimsHaveOneWinner(t *testing.T) {
	for _, kind := range []string{"wallet", "admin"} {
		t.Run(kind, func(t *testing.T) {
			db := newActivationCodeTestDB(t)
			seedActivationCodeUser(t, db, 11, "buyer")
			seedActivationCodeUser(t, db, 12, "recipient")
			var code string
			if kind == "wallet" {
				nonce := "wallet-purchase-fixture"
				code = deriveScriptActivationCode(syntheticActivationSigningKey, 11, nonce)
				purchase := entity.ScriptActivationEntitlement{
					UserID: 11, PurchaseKeyHash: hashIdempotencyValue("purchase"), PurchaseNonce: nonce, PurchasePrice: 2.5, CodeHash: hashScriptSecret(code),
				}
				if err := db.Create(&purchase).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				codes, err := GenerateAdminScriptActivationCodes(db, 11, 1, "", nil)
				if err != nil {
					t.Fatal(err)
				}
				code = codes[0].Code
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, id := range []int32{11, 12} {
				go func() {
					<-start
					_, err := RedeemScriptActivationCodeWithDB(db, id, code)
					results <- err
				}()
			}
			close(start)
			wins := 0
			for range 2 {
				if err := <-results; err == nil {
					wins++
				} else if !errors.Is(err, ErrScriptActivationCodeFrozen) {
					t.Errorf("losing claim returned an unexpected error: %v", err)
				}
			}
			if wins != 1 {
				t.Fatalf("expected one successful claim, got %d", wins)
			}
		})
	}
}
