package controller

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestLegacyUserBalanceUpdateAlwaysWritesLedgerAndAudit(t *testing.T) {
	for _, mode := range []string{"increase", "decrease", "override"} {
		for _, show := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%t", mode, show), func(t *testing.T) {
				gin.SetMode(gin.TestMode)
				db := newUserControllerTestDB(t)
				admin := entity.User{ID: 1, Username: "balance_admin", Role: "admin", Status: "enabled"}
				target := entity.User{ID: 2, Username: "balance_target", Role: "user", Status: "enabled", Balance: 10}
				if err := db.Create(&[]entity.User{admin, target}).Error; err != nil {
					t.Fatal(err)
				}
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Request = httptest.NewRequest(http.MethodPut, "/api/user/2", bytes.NewBufferString(fmt.Sprintf(`{"nickname":"test","role":"user","status":"enabled","balance":2.123456,"balanceMode":%q,"showInStatement":%t}`, mode, show)))
				ctx.Request.Header.Set("Content-Type", "application/json")
				ctx.Request.RemoteAddr = "198.51.100.5:1234"
				ctx.Params = gin.Params{{Key: "id", Value: "2"}}
				ctx.Set("user", &admin)
				UpdateUser(ctx)
				if recorder.Code != http.StatusOK {
					t.Fatalf("update: %d %s", recorder.Code, recorder.Body.String())
				}
				var records []entity.BillingRecord
				if err := db.Where("user_id = ?", target.ID).Find(&records).Error; err != nil {
					t.Fatal(err)
				}
				if len(records) != 1 {
					t.Fatalf("expected one permanent ledger record, got %d", len(records))
				}
				if records[0].Type != "balance_adjustment" || records[0].BalanceBefore != 10 {
					t.Fatalf("unexpected record: %+v", records[0])
				}
				if records[0].HiddenFromStatement == show || records[0].OperatorID != admin.ID || records[0].OperatorUsername != admin.Username || records[0].AdjustmentMode != mode {
					t.Fatalf("missing visibility/operator metadata: %+v", records[0])
				}
				var logs []entity.Log
				if err := db.Where("user_id = ? AND module = ? AND action = ?", admin.ID, "user", logActionUpdate).Find(&logs).Error; err != nil {
					t.Fatal(err)
				}
				if len(logs) != 1 || logs[0].Username != admin.Username || !strings.Contains(logs[0].Description, target.Username) || logs[0].ClientIP != "198.51.100.5" {
					t.Fatalf("missing operator/target audit: %+v", logs)
				}
				var profileLogs []entity.Log
				if err := db.Where("user_id = ? AND module = ? AND action = ?", admin.ID, "user", 16).Find(&profileLogs).Error; err != nil {
					t.Fatal(err)
				}
				if len(profileLogs) != 1 || !strings.Contains(profileLogs[0].NewAnswer, `"nickname":"test"`) {
					t.Fatalf("profile change must have its own audit, separate from the balance: %+v", profileLogs)
				}
			})
		}
	}
}

func TestBalanceEndpointIdempotencyPreventsDuplicateAdjustment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := entity.User{ID: 1, Username: "balance_admin", Role: "admin", Status: "enabled"}
	target := entity.User{ID: 2, Username: "balance_target", Role: "user", Status: "enabled", Balance: 10}
	if err := db.Create(&[]entity.User{admin, target}).Error; err != nil {
		t.Fatal(err)
	}
	request := func(amount string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/api/user/2/balance", bytes.NewBufferString(`{"mode":"increase","amount":`+amount+`}`))
		ctx.Request.Header.Set("Content-Type", "application/json")
		ctx.Request.Header.Set("Idempotency-Key", "balance-action-1")
		ctx.Params = gin.Params{{Key: "id", Value: "2"}}
		ctx.Set("user", &admin)
		UpdateUserBalance(ctx)
		return recorder
	}
	first, replay := request("2"), request("2")
	if first.Code != 200 || replay.Code != 200 || first.Body.String() != replay.Body.String() {
		t.Fatalf("replay response mismatch: first=%d %s replay=%d %s", first.Code, first.Body.String(), replay.Code, replay.Body.String())
	}
	var stored entity.User
	if err := db.First(&stored, target.ID).Error; err != nil || stored.Balance != 12 {
		t.Fatalf("balance changed twice: %+v err=%v", stored, err)
	}
	var count int64
	db.Model(&entity.BillingRecord{}).Where("type = ?", "balance_adjustment").Count(&count)
	if count != 1 {
		t.Fatalf("expected one ledger record, got %d", count)
	}
	if changed := request("3"); changed.Code != http.StatusConflict {
		t.Fatalf("changed request should conflict: %d %s", changed.Code, changed.Body.String())
	}
}

func TestDedicatedBalanceAndVisibilityEndpointsEnforceAdminAndExplicitValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := entity.User{ID: 1, Username: "endpoint_admin", Role: "admin", Status: "enabled"}
	user := entity.User{ID: 2, Username: "endpoint_user", Role: "user", Status: "enabled", Balance: 10}
	if err := db.Create(&[]entity.User{admin, user}).Error; err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, id string
		operator       *entity.User
		handler        gin.HandlerFunc
		status         int
	}{
		{"nonadmin_balance", `{"mode":"increase","amount":1}`, "2", &user, UpdateUserBalance, 403},
		{"missing_amount", `{"mode":"override"}`, "2", &admin, UpdateUserBalance, 400},
		{"missing_mode", `{"amount":1}`, "2", &admin, UpdateUserBalance, 400},
		{"bad_id", `{"mode":"increase","amount":1}`, "-1", &admin, UpdateUserBalance, 400},
		{"zero_override_hidden", `{"mode":"override","amount":0,"showInStatement":false}`, "2", &admin, UpdateUserBalance, 200},
		{"missing_visibility", `{}`, "1", &admin, UpdateBillingStatementVisibility, 400},
		{"nonadmin_visibility", `{"showInStatement":true}`, "1", &user, UpdateBillingStatementVisibility, 403},
		{"show_record", `{"showInStatement":true}`, "1", &admin, UpdateBillingStatementVisibility, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/api/test", bytes.NewBufferString(tc.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			ctx.Params = gin.Params{{Key: "id", Value: tc.id}}
			ctx.Set("user", tc.operator)
			tc.handler(ctx)
			if recorder.Code != tc.status {
				t.Fatalf("%d != %d: %s", recorder.Code, tc.status, recorder.Body.String())
			}
		})
	}
	var stored entity.User
	db.First(&stored, user.ID)
	if stored.Balance != 0 {
		t.Fatalf("expected zero override, got %v", stored.Balance)
	}
	var record entity.BillingRecord
	db.First(&record)
	if record.HiddenFromStatement {
		t.Fatal("visibility update did not apply")
	}
}

func TestLegacyProfileWithBalanceButNoModeKeepsTheExistingBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newUserControllerTestDB(t)
	admin := entity.User{ID: 1, Username: "legacy_admin", Role: "admin", Status: "enabled"}
	user := entity.User{ID: 2, Username: "legacy_user", Role: "user", Status: "enabled", Balance: 15}
	if err := db.Create(&[]entity.User{admin, user}).Error; err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/user/2", bytes.NewBufferString(`{"nickname":"profile edit","role":"user","status":"enabled","balance":10}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "id", Value: "2"}}
	ctx.Set("user", &admin)
	UpdateUser(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%d: %s", recorder.Code, recorder.Body.String())
	}
	var stored entity.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Balance != 15 || stored.Nickname != "profile edit" {
		t.Fatalf("legacy no-mode behavior changed: %+v", stored)
	}
}

func TestLegacyUserBalanceUpdateRollsBackWhenLedgerOrAuditFails(t *testing.T) {
	for _, table := range []string{entity.TableNameBillingRecord, entity.TableNameLog} {
		t.Run(table, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := newUserControllerTestDB(t)
			admin := entity.User{ID: 1, Username: "rollback_admin", Role: "admin", Status: "enabled"}
			target := entity.User{ID: 2, Username: "rollback_target", Role: "user", Status: "enabled", Balance: 10, Nickname: "original"}
			if err := db.Create(&[]entity.User{admin, target}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Create().Before("gorm:create").Register("balance_write_failure", func(tx *gorm.DB) {
				if tx.Statement.Table == table {
					tx.AddError(errors.New("simulated write failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPut, "/api/user/2", bytes.NewBufferString(`{"nickname":"changed","role":"user","status":"enabled","balance":5,"balanceMode":"increase"}`))
			ctx.Request.Header.Set("Content-Type", "application/json")
			ctx.Params = gin.Params{{Key: "id", Value: "2"}}
			ctx.Set("user", &admin)
			UpdateUser(ctx)
			if recorder.Code == http.StatusOK {
				t.Fatal("write failure must reject and roll back the adjustment")
			}
			var stored entity.User
			if err := db.First(&stored, target.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Balance != 10 || stored.Nickname != "original" {
				t.Fatalf("partial mutation: %+v", stored)
			}
			var count int64
			db.Model(&entity.BillingRecord{}).Count(&count)
			if count != 0 {
				t.Fatalf("partial ledger records: %d", count)
			}
		})
	}
}
