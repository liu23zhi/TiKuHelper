package service

import (
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func seedUserLevelTriggerFixture(t *testing.T, db *gorm.DB, userID int32, minRecharge, minConsumption float64) (entity.UserLevel, entity.UserLevel) {
	t.Helper()
	if err := db.AutoMigrate(&entity.RechargeOrder{}, &entity.TicketRefund{}, &entity.BillingRecord{}, &entity.UserLevel{}, &entity.UserLevelHistory{}); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "level_id") {
		if err := db.Exec("ALTER TABLE users ADD COLUMN level_id integer NOT NULL DEFAULT 0").Error; err != nil {
			t.Fatal(err)
		}
	}
	basic := mustCreateLevel(t, db, entity.UserLevel{Name: "基础会员", Rank: 1, Enabled: true})
	advanced := mustCreateLevel(t, db, entity.UserLevel{Name: "进阶会员", Rank: 2, Enabled: true, MinRechargeTotal: minRecharge, MinConsumptionTotal: minConsumption})
	if err := db.Table(entity.TableNameUser).Where("id = ?", userID).Update("level_id", basic.ID).Error; err != nil {
		t.Fatal(err)
	}
	return basic, advanced
}

func TestTicketRefundSuccessReevaluatesPersistedUserLevel(t *testing.T) {
	for _, failLevelUpdate := range []bool{false, true} {
		name := "success"
		if failLevelUpdate {
			name = "level-failure-keeps-refund"
		}
		t.Run(name, func(t *testing.T) {
			db, owner, _, admin := newTicketRefundCoreTestDB(t)
			basic, advanced := seedUserLevelTriggerFixture(t, db, owner.ID, 40, 0)
			order := seedTicketRefundOrder(t, db, 1, owner.ID, "paid", time.Now().Add(-time.Hour), 50.25, true)
			if err := db.AutoMigrate(&entity.BillingRecord{}); err != nil {
				t.Fatal(err)
			}
			seedLevelCredit(t, db, order)
			if _, err := ReconcileUserLevelWithDB(db, owner.ID, entity.UserLevelTriggerRecharge); err != nil {
				t.Fatal(err)
			}
			row, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{Kind: TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 3000}, testVerifyTicketRefundChannel)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ReviewTicketRefundWithDB(db, admin, row.ID, true, 3000, testVerifyTicketRefundChannel); err != nil {
				t.Fatal(err)
			}
			if _, err := BeginTicketRefundWithDB(db, admin, row.ID, testVerifyTicketRefundChannel); err != nil {
				t.Fatal(err)
			}
			if saved, err := CurrentUserLevelIDWithDB(db, owner.ID); err != nil || saved != advanced.ID {
				t.Fatalf("nonterminal refund must keep original level: %d %v", saved, err)
			}
			if failLevelUpdate {
				if err := db.Exec(`CREATE TRIGGER reject_level_update BEFORE UPDATE OF level_id ON users
					BEGIN SELECT RAISE(ABORT, 'synthetic level persistence failure'); END`).Error; err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if completed, err := ReconcileTicketRefundWithDB(db, admin, row.ID, TicketRefundStatusSucceeded, "synthetic-level-refund-ref"); err != nil || completed.Status != TicketRefundStatusSucceeded {
					t.Fatalf("refund must complete despite level failure: %+v %v", completed, err)
				}
			}
			wantID, wantHistoryCount := basic.ID, 1
			if failLevelUpdate {
				wantID, wantHistoryCount = advanced.ID, 0
			}
			if saved, err := CurrentUserLevelIDWithDB(db, owner.ID); err != nil || saved != wantID {
				t.Fatalf("unexpected persisted level after refund: %d want %d %v", saved, wantID, err)
			}
			var histories []entity.UserLevelHistory
			if err := db.Where("user_id = ? AND trigger = ?", owner.ID, entity.UserLevelTriggerRefund).Find(&histories).Error; err != nil || len(histories) != wantHistoryCount {
				t.Fatalf("refund replays must not duplicate history: %+v %v", histories, err)
			}
			if len(histories) == 1 && histories[0].RechargeTotal != 20.25 {
				t.Fatalf("successful refund must subtract actual returned principal: %+v", histories[0])
			}
			requireTicketRefundWallet(t, db, owner.ID, 70_123_456)
		})
	}
}

func TestScriptActivationPurchaseReevaluatesPersistedUserLevel(t *testing.T) {
	db, user := activationEntitlementFixture(t)
	_, advanced := seedUserLevelTriggerFixture(t, db, user.ID, 0, 5)
	setSyntheticActivationPrice(t, db, "5")
	for i := 0; i < 2; i++ {
		if _, err := PurchaseScriptActivationCodeWithDB(db, user.ID, "level-up-on-purchase", syntheticActivationSigningKey); err != nil {
			t.Fatal(err)
		}
		if saved, err := CurrentUserLevelIDWithDB(db, user.ID); err != nil || saved != advanced.ID {
			t.Fatalf("activation purchase must persist upgraded level: %d %v", saved, err)
		}
	}
	assertActivationWallet(t, db, user.ID, 45, 1)
	var histories []entity.UserLevelHistory
	if err := db.Where("user_id = ?", user.ID).Find(&histories).Error; err != nil || len(histories) != 1 || histories[0].Trigger != entity.UserLevelTriggerConsumption {
		t.Fatalf("purchase must keep one consumption upgrade history: %+v %v", histories, err)
	}
}

func TestLegacyScriptReservationDoesNotUpgradeUserLevel(t *testing.T) {
	db := newScriptTestDB(t)
	owner := entity.User{Username: "reservation-level-owner", Role: "user", Status: "enabled", Balance: 20}
	bindSyntheticScriptOwner(&owner)
	admin := entity.User{Username: "reservation-level-admin", Role: "admin", Status: "enabled"}
	for _, user := range []*entity.User{&owner, &admin} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	basic, _ := seedUserLevelTriggerFixture(t, db, owner.ID, 0, ScriptReservationFee)
	version, err := UploadScriptVersionWithDB(db, admin, "2026.11", "answer.exe", []byte("MZsynthetic"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateUserScriptWithDB(db, owner, CreateUserScriptInput{Name: "level reservation", VersionID: version.ID, BaseDomain: "a.com"}); err != nil {
		t.Fatal(err)
	}
	if saved, err := CurrentUserLevelIDWithDB(db, owner.ID); err != nil || saved != basic.ID {
		t.Fatalf("refundable reservation must not upgrade the level: %d %v", saved, err)
	}
}
