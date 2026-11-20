package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func seedControllerUserLevels(t *testing.T, db *gorm.DB, userID int32, minRecharge, minConsumption float64) (entity.UserLevel, entity.UserLevel) {
	t.Helper()
	if err := db.AutoMigrate(&entity.UserLevel{}, &entity.UserLevelHistory{}); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(entity.TableNameUser, "level_id") {
		if err := db.Exec("ALTER TABLE users ADD COLUMN level_id integer NOT NULL DEFAULT 0").Error; err != nil {
			t.Fatal(err)
		}
	}
	basic := entity.UserLevel{Name: "基础会员", Rank: 1, Enabled: true}
	advanced := entity.UserLevel{Name: "进阶会员", Rank: 2, Enabled: true, MinRechargeTotal: minRecharge, MinConsumptionTotal: minConsumption}
	for _, level := range []*entity.UserLevel{&basic, &advanced} {
		if err := db.Create(level).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Table(entity.TableNameUser).Where("id = ?", userID).Update("level_id", basic.ID).Error; err != nil {
		t.Fatal(err)
	}
	return basic, advanced
}

func TestSearchBillingReevaluatesPersistedUserLevelAfterConsumption(t *testing.T) {
	db, owner, token := newWalletSplitBillingFixture(t, 10, 0)
	_, advanced := seedControllerUserLevels(t, db, owner.ID, 0, 1)
	for i := 0; i < 2; i++ {
		wallet, _ := chargeOnce(t, db, owner, token, "consumption-level-once", 1)
		if wallet.Balance != 9 {
			t.Fatalf("billing replay must not charge again: %v", wallet.Balance)
		}
		if saved, err := service.CurrentUserLevelIDWithDB(db, owner.ID); err != nil || saved != advanced.ID {
			t.Fatalf("committed search consumption must persist the new level: %d %v", saved, err)
		}
	}
	var histories []entity.UserLevelHistory
	if err := db.Where("user_id = ?", owner.ID).Find(&histories).Error; err != nil || len(histories) != 1 || histories[0].Trigger != entity.UserLevelTriggerConsumption {
		t.Fatalf("billing replay must keep one consumption upgrade: %+v %v", histories, err)
	}
}

func TestSearchBillingLevelFailureDoesNotUndoCommittedCharge(t *testing.T) {
	db, owner, token := newWalletSplitBillingFixture(t, 10, 0)
	basic, _ := seedControllerUserLevels(t, db, owner.ID, 0, 1)
	if err := db.Exec(`CREATE TRIGGER reject_level_update BEFORE UPDATE OF level_id ON users
		BEGIN SELECT RAISE(ABORT, 'synthetic level persistence failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	wallet, bill := chargeOnce(t, db, owner, token, "consumption-level-failure", 1)
	if wallet.Balance != 9 || bill.Amount != 1 {
		t.Fatalf("level failure must preserve committed charge: wallet=%v bill=%+v", wallet.Balance, bill)
	}
	if saved, err := service.CurrentUserLevelIDWithDB(db, owner.ID); err != nil || saved != basic.ID {
		t.Fatalf("failed level update must retain old level: %d %v", saved, err)
	}
	var count int64
	if err := db.Model(&entity.UserLevelHistory{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed level transaction must not leave a history: %d %v", count, err)
	}
}

func TestUserLevelCurrentRepairsStaleSummaryAndReportsPersistenceFailures(t *testing.T) {
	for _, failure := range []string{"success", "reject-update", "missing-history"} {
		t.Run(failure, func(t *testing.T) {
			db := newRegisterLevelTestDB(t)
			if err := db.AutoMigrate(&entity.RechargeOrder{}); err != nil {
				t.Fatal(err)
			}
			owner := entity.User{Username: "stale-level-user", Role: "user", Status: "enabled"}
			if err := db.Create(&owner).Error; err != nil {
				t.Fatal(err)
			}
			basic, advanced := seedControllerUserLevels(t, db, owner.ID, 100, 0)
			if err := db.Create(&entity.RechargeOrder{UserID: owner.ID, OrderNo: "stale-level-296.5", PaymentProvider: "synthetic", Status: "paid", Amount: 296.5}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&entity.BillingRecord{UserID: owner.ID, Type: "normal_search", Amount: 15.158586}).Error; err != nil {
				t.Fatal(err)
			}
			expectedStatus := http.StatusOK
			if failure == "reject-update" {
				expectedStatus = http.StatusInternalServerError
				if err := db.Exec(`CREATE TRIGGER reject_level_update BEFORE UPDATE OF level_id ON users
					BEGIN SELECT RAISE(ABORT, 'synthetic level persistence failure'); END`).Error; err != nil {
					t.Fatal(err)
				}
			} else if failure == "missing-history" {
				expectedStatus = http.StatusServiceUnavailable
				if err := db.Migrator().DropTable(&entity.UserLevelHistory{}); err != nil {
					t.Fatal(err)
				}
			}
			gin.SetMode(gin.TestMode)
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/user/level", nil)
			ctx.Set("user", &owner)
			UserLevelCurrent(ctx)
			if response.Code != expectedStatus {
				t.Fatalf("status = %d want %d: %s", response.Code, expectedStatus, response.Body.String())
			}
			saved, err := service.CurrentUserLevelIDWithDB(db, owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			if failure != "success" {
				if saved != basic.ID {
					t.Fatalf("failure must not pretend an upgraded level was saved: %d", saved)
				}
				return
			}
			var body struct {
				Data service.UserLevelSummary `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if saved != advanced.ID || body.Data.LevelID != saved || body.Data.RechargeTotal != 296.5 || body.Data.ConsumptionTotal != 15.158586 {
				t.Fatalf("response must match updated level and current totals: saved=%d summary=%+v", saved, body.Data)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("current level response must not retain a stale cached summary")
			}
		})
	}
}
