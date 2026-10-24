package service

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newBalanceAdjustmentTestDB(t *testing.T) (*gorm.DB, entity.User, entity.User) {
	t.Helper()
	db := newBillingRecordTestDB(t)
	if err := db.AutoMigrate(&entity.Log{}); err != nil {
		t.Fatal(err)
	}
	admin := seedBillingRecordUser(t, db, 1, "admin")
	user := seedBillingRecordUser(t, db, 2, "user")
	user.Balance = 10
	if err := db.Model(&user).Update("balance", 10).Error; err != nil {
		t.Fatal(err)
	}
	return db, admin, user
}

func TestBalanceAdjustmentIdempotencyReplaysWithoutSecondLedger(t *testing.T) {
	db, admin, user := newBalanceAdjustmentTestDB(t)
	if err := db.AutoMigrate(&entity.IdempotencyRecord{}); err != nil {
		t.Fatal(err)
	}
	input := UserBalanceAdjustmentInput{Mode: "increase", Amount: 2.5}
	first, err := AdjustUserBalanceIdempotentWithDB(db, admin, user.ID, input, "balance-action-1")
	if err != nil || first.Balance != 12.5 {
		t.Fatalf("first adjustment: user=%+v err=%v", first, err)
	}
	replayed, err := AdjustUserBalanceIdempotentWithDB(db, admin, user.ID, input, "balance-action-1")
	if err != nil || replayed.Balance != 12.5 {
		t.Fatalf("replayed adjustment: user=%+v err=%v", replayed, err)
	}
	var stored entity.User
	if err := db.First(&stored, user.ID).Error; err != nil || stored.Balance != 12.5 {
		t.Fatalf("stored balance: %+v err=%v", stored, err)
	}
	var bills, logs int64
	db.Model(&entity.BillingRecord{}).Where("type = ?", balanceAdjustmentBillingRecordType).Count(&bills)
	db.Model(&entity.Log{}).Count(&logs)
	if bills != 1 || logs != 1 {
		t.Fatalf("replay caused extra side effect: bills=%d logs=%d", bills, logs)
	}
	if _, err := AdjustUserBalanceIdempotentWithDB(db, admin, user.ID, UserBalanceAdjustmentInput{Mode: "increase", Amount: 3}, "balance-action-1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed request must conflict: %v", err)
	}
}

func TestBalanceAdjustmentsPersistActorAndVisibilityWithoutCountingAsConsumption(t *testing.T) {
	for _, mode := range []string{"increase", "decrease", "override"} {
		for _, show := range []bool{false, true} {
			name := mode + "/hidden"
			if show {
				name = mode + "/shown"
			}
			t.Run(name, func(t *testing.T) {
				db, admin, user := newBalanceAdjustmentTestDB(t)
				updated, err := AdjustUserBalanceWithDB(db, admin, user.ID, UserBalanceAdjustmentInput{Mode: mode, Amount: 2.123456, ShowInStatement: &show, ClientIP: "198.51.100.2"})
				if err != nil {
					t.Fatal(err)
				}
				expected := map[string]float64{"increase": 12.123456, "decrease": 7.876544, "override": 2.123456}[mode]
				if updated.Balance != expected {
					t.Fatalf("balance %v != %v", updated.Balance, expected)
				}
				all, err := ListBillingRecordsWithDB(db, admin, BillingRecordListQuery{}, 1, 10)
				if err != nil {
					t.Fatal(err)
				}
				if all.Total != 1 || len(all.Data) != 1 {
					t.Fatalf("admin must always see record: %+v", all)
				}
				row := all.Data[0]
				if row.ShowInStatement != show || row.OperatorID != admin.ID || row.OperatorUsername != admin.Username || row.AdjustmentMode != mode || row.BalanceBefore != 10 || row.BalanceAfter != expected {
					t.Fatalf("missing metadata: %+v", row)
				}
				if !strings.Contains(row.Subject, "管理员") || !strings.Contains(row.Detail, admin.Username) {
					t.Fatalf("missing explanation: %+v", row)
				}
				if all.Summary.TotalAmount != 0 || all.Summary.CurrentPageAmount != 0 {
					t.Fatalf("adjustments are not consumption: %+v", all.Summary)
				}
				mine, err := ListBillingRecordsWithDB(db, user, BillingRecordListQuery{Keyword: "管理员"}, 1, 10)
				if err != nil {
					t.Fatal(err)
				}
				if (mine.Total == 1) != show || (len(mine.Data) == 1) != show {
					t.Fatalf("hidden row leaked or visible row missing: %+v", mine)
				}
				other := entity.User{ID: 99, Role: "user"}
				foreign, err := ListBillingRecordsWithDB(db, other, BillingRecordListQuery{}, 1, 10)
				if err != nil || foreign.Total != 0 {
					t.Fatalf("foreign user saw records: %+v %v", foreign, err)
				}
			})
		}
	}
}

func TestBalanceAdjustmentDefaultsVisibleAndSupportsZeroAndNegativeBalances(t *testing.T) {
	db, admin, user := newBalanceAdjustmentTestDB(t)
	for _, input := range []UserBalanceAdjustmentInput{{Mode: "override", Amount: 0}, {Mode: "decrease", Amount: 1}, {Mode: "override", Amount: -2}} {
		if _, err := AdjustUserBalanceWithDB(db, admin, user.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	var rows []entity.BillingRecord
	db.Find(&rows)
	if len(rows) != 3 {
		t.Fatalf("records: %+v", rows)
	}
	for _, row := range rows {
		if row.HiddenFromStatement {
			t.Fatal("default must be visible")
		}
	}
}

func TestBalanceAdjustmentRejectsInvalidInputsAndNonAdmins(t *testing.T) {
	db, admin, user := newBalanceAdjustmentTestDB(t)
	for _, input := range []UserBalanceAdjustmentInput{{Mode: "multiply", Amount: 1}, {Mode: "increase", Amount: -1}, {Mode: "decrease", Amount: -1}, {Mode: "override", Amount: 0.0000001}, {Amount: math.NaN()}, {Amount: math.Inf(1)}, {Amount: 1e13}} {
		if _, err := AdjustUserBalanceWithDB(db, admin, user.ID, input); !errors.Is(err, ErrInvalidBalanceAdjustment) {
			t.Fatalf("input %+v: %v", input, err)
		}
	}
	if _, err := AdjustUserBalanceWithDB(db, user, admin.ID, UserBalanceAdjustmentInput{Mode: "increase", Amount: 1}); !errors.Is(err, ErrBalanceAdjustmentForbidden) {
		t.Fatalf("permission: %v", err)
	}
	var count int64
	db.Model(&entity.BillingRecord{}).Count(&count)
	if count != 0 {
		t.Fatalf("invalid requests wrote records: %d", count)
	}
}

func TestStatementVisibilityEditsPreserveMoneyAndAuditActor(t *testing.T) {
	db, admin, user := newBalanceAdjustmentTestDB(t)
	if _, err := AdjustUserBalanceWithDB(db, admin, user.ID, UserBalanceAdjustmentInput{Mode: "increase", Amount: 5}); err != nil {
		t.Fatal(err)
	}
	var before entity.BillingRecord
	db.First(&before)
	if err := SetBillingStatementVisibilityWithDB(db, user, before.ID, false, ""); !errors.Is(err, ErrBalanceAdjustmentForbidden) {
		t.Fatalf("permission: %v", err)
	}
	for _, show := range []bool{false, true} {
		if err := SetBillingStatementVisibilityWithDB(db, admin, before.ID, show, "198.51.100.3"); err != nil {
			t.Fatal(err)
		}
		result, err := ListBillingRecordsWithDB(db, user, BillingRecordListQuery{}, 1, 10)
		if err != nil || (result.Total == 1) != show {
			t.Fatalf("statement: %+v %v", result, err)
		}
	}
	var after entity.BillingRecord
	db.First(&after, before.ID)
	if after.Amount != before.Amount || after.BalanceBefore != before.BalanceBefore || after.BalanceAfter != before.BalanceAfter || !after.CreatedAt.Equal(before.CreatedAt) || after.OperatorID != before.OperatorID {
		t.Fatal("visibility changed historical money/actor/time")
	}
	var count int64
	db.Model(&entity.Log{}).Where("module = ? AND user_id = ?", "billing", admin.ID).Count(&count)
	if count != 2 {
		t.Fatalf("visibility audit count: %d", count)
	}
}

func TestStatementVisibilityGroupsSearchChargesAndRollsBackOnAuditFailure(t *testing.T) {
	db, admin, user := newBalanceAdjustmentTestDB(t)
	rows := []entity.BillingRecord{{UserID: user.ID, Type: "normal_search", Provider: "icodef", Amount: 1, RequestID: "group"}, {UserID: user.ID, Type: "normal_search", Provider: "ai", Amount: 2, RequestID: "group"}}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := SetBillingStatementVisibilityWithDB(db, admin, rows[1].ID, false, ""); err != nil {
		t.Fatal(err)
	}
	mine, err := ListBillingRecordsWithDB(db, user, BillingRecordListQuery{}, 1, 10)
	if err != nil || mine.Total != 0 || mine.Summary.TotalAmount != 0 {
		t.Fatalf("group leaked: %+v %v", mine, err)
	}
	if err := db.Callback().Create().Before("gorm:create").Register("visibility_audit_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameLog {
			tx.AddError(errors.New("audit failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := SetBillingStatementVisibilityWithDB(db, admin, rows[0].ID, true, ""); err == nil {
		t.Fatal("expected rollback")
	}
	var count int64
	db.Model(&entity.BillingRecord{}).Where("hidden_from_statement = ?", true).Count(&count)
	if count != 2 {
		t.Fatalf("partial visibility change: %d", count)
	}
}

func TestProfileBindingsDoNotOverwriteAChangedBalance(t *testing.T) {
	for _, binding := range []string{"phone", "wechat"} {
		t.Run(binding, func(t *testing.T) {
			db, _, user := newBalanceAdjustmentTestDB(t)
			injected := false
			if err := db.Callback().Update().Before("gorm:update").Register("simulate_balance_change", func(tx *gorm.DB) {
				if injected || tx.Statement.Table != entity.TableNameUser {
					return
				}
				injected = true
				tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).Where("id = ?", user.ID).UpdateColumn("balance", 15).Error)
			}); err != nil {
				t.Fatal(err)
			}
			var err error
			if binding == "phone" {
				_, err = UpdateUserPhoneWithDB(db, user.ID, "", false)
			} else {
				_, err = UpdateUserWechatBindingWithDB(db, user.ID, "", false)
			}
			if err != nil {
				t.Fatal(err)
			}
			var stored entity.User
			if err := db.First(&stored, user.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Balance != 15 {
				t.Fatalf("%s binding overwrote a newer balance: %v", binding, stored.Balance)
			}
		})
	}
}
