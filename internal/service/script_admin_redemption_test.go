package service

import (
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestGeneratedAdminCodeRedeemsIntoExactlyOneUserEntitlement(t *testing.T) {
	db, user := activationEntitlementFixture(t)
	if err := db.AutoMigrate(&entity.AdminScriptActivationCode{}); err != nil {
		t.Fatal(err)
	}
	codes, err := GenerateAdminScriptActivationCodes(db, user.ID, 1, "客服补发", nil)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := RedeemScriptActivationCodeWithDB(db, user.ID, codes[0].Code)
	if err != nil || grant.ID <= 0 || grant.UserID != user.ID || grant.RedeemedAt == nil || grant.PurchasePrice != 0 {
		t.Fatalf("generated code must redeem: %+v %v", grant, err)
	}
	other := entity.User{Username: "other-redemption", Role: "user", Status: "enabled"}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := RedeemScriptActivationCodeWithDB(db, other.ID, codes[0].Code); err == nil {
		t.Fatal("reused code issued second grant")
	}
	var stored entity.AdminScriptActivationCode
	if err := db.First(&stored, codes[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.UsedEntitlementID == nil || *stored.UsedEntitlementID != grant.ID {
		t.Fatal("usage history lost grant linkage")
	}
	if again, err := RedeemScriptActivationCodeWithDB(db, user.ID, codes[0].Code); err != nil || again.ID != grant.ID {
		t.Fatalf("same owner retry lost original grant: %+v %v", again, err)
	}
	var count int64
	db.Model(&entity.ScriptActivationEntitlement{}).Count(&count)
	if count != 1 {
		t.Fatal("duplicate grant on retry")
	}
}

func TestScriptActivationRedemptionRejectsActiveBanWithoutConsumingEitherCode(t *testing.T) {
	for _, source := range []string{"admin-TKA", "purchased-sac"} {
		t.Run(source, func(t *testing.T) {
			db, user := activationEntitlementFixture(t)
			if err := db.AutoMigrate(&entity.AdminScriptActivationCode{}, &entity.UserBan{}); err != nil {
				t.Fatal(err)
			}
			var code string
			var adminCodeID, purchasedEntitlementID int64
			if source == "admin-TKA" {
				codes, err := GenerateAdminScriptActivationCodes(db, user.ID, 1, "封禁兑换回归", nil)
				if err != nil {
					t.Fatal(err)
				}
				code, adminCodeID = codes[0].Code, codes[0].ID
			} else {
				setSyntheticActivationPrice(t, db, "2.5")
				purchase, err := PurchaseScriptActivationCodeWithDB(db, user.ID, "buy-before-ban", syntheticActivationSigningKey)
				if err != nil {
					t.Fatal(err)
				}
				code, purchasedEntitlementID = purchase.Code, purchase.EntitlementID
			}
			var beforeCount int64
			if err := db.Model(&entity.ScriptActivationEntitlement{}).Count(&beforeCount).Error; err != nil {
				t.Fatal(err)
			}
			// Deliberately keep users.status enabled: this catches a regression
			// that checks the status column but ignores the authoritative ban row.
			ban := entity.UserBan{UserID: user.ID, Reason: "生效封禁", BannedAt: time.Now().Add(-time.Minute)}
			if err := db.Create(&ban).Error; err != nil {
				t.Fatal(err)
			}
			var current entity.User
			if err := db.First(&current, user.ID).Error; err != nil || current.Status != "enabled" {
				t.Fatalf("fixture must retain enabled status: %+v %v", current, err)
			}
			if grant, err := RedeemScriptActivationCodeWithDB(db, user.ID, code); !errors.Is(err, ErrScriptActivationCodeInvalid) || grant.ID != 0 {
				t.Fatalf("active ban must reject %s without returning a grant: %+v %v", source, grant, err)
			}
			var afterCount, redeemedCount int64
			if err := db.Model(&entity.ScriptActivationEntitlement{}).Count(&afterCount).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&entity.ScriptActivationEntitlement{}).Where("redeemed_at IS NOT NULL").Count(&redeemedCount).Error; err != nil {
				t.Fatal(err)
			}
			if afterCount != beforeCount || redeemedCount != 0 {
				t.Fatalf("rejection created or redeemed an entitlement: before=%d after=%d redeemed=%d", beforeCount, afterCount, redeemedCount)
			}
			if adminCodeID > 0 {
				var stored entity.AdminScriptActivationCode
				if err := db.First(&stored, adminCodeID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.Status != entity.ScriptActivationCodeStatusUnused || stored.UsedAt != nil || stored.UsedBy != nil || stored.UsedEntitlementID != nil {
					t.Fatalf("rejected admin code was consumed: %+v", stored)
				}
			} else {
				var stored entity.ScriptActivationEntitlement
				if err := db.First(&stored, purchasedEntitlementID).Error; err != nil {
					t.Fatal(err)
				}
				if stored.RedeemedAt != nil {
					t.Fatal("rejected purchased code was consumed")
				}
				assertActivationWallet(t, db, user.ID, 47.5, 1)
			}
			// Prove the rejected code is still valid, rather than manufacturing
			// an invalid fixture that would be rejected even without a ban check.
			if err := Unban(db, UserUnbanRequest{UserID: user.ID, Reason: "回归验证解除"}); err != nil {
				t.Fatal(err)
			}
			if grant, err := RedeemScriptActivationCodeWithDB(db, user.ID, code); err != nil || grant.ID <= 0 || grant.RedeemedAt == nil {
				t.Fatalf("unchanged code must remain redeemable after unban: %+v %v", grant, err)
			}
		})
	}
}
