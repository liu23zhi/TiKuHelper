package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEffectiveLedgerHTTPRejectsForeignSourceAndDemotedAdmin(t *testing.T) {
	db := newUserControllerTestDB(t)
	if err := db.AutoMigrate(&entity.EffectiveAdjustment{}, &entity.RechargeOrder{}, &entity.TicketRefund{}); err != nil {
		t.Fatal(err)
	}
	admin := entity.User{ID: 1, Username: "admin", Role: "admin", Status: "enabled"}
	owner := entity.User{ID: 2, Username: "owner", Role: "user", Status: "enabled"}
	other := entity.User{ID: 3, Username: "other", Role: "user", Status: "enabled"}
	if err := db.Create(&[]entity.User{admin, owner, other}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]entity.BillingRecord{{ID: 10, UserID: 2, Type: "normal_search", Amount: 3, BalanceBefore: 3, BalanceAfter: 0}, {ID: 11, UserID: 3, Type: "normal_search", Amount: 3, BalanceBefore: 3, BalanceAfter: 0}}).Error; err != nil {
		t.Fatal(err)
	}
	request := func(method, body string, actor *entity.User) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(method, "/api/admin/users/2/effective-ledger", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "id", Value: "2"}}
		if actor != nil {
			c.Set("user", actor)
		}
		EffectiveLedger(c)
		return w.Code
	}
	const valid = `{"sourceKind":"bill","sourceId":10,"expected":"3","new":"2","revision":0,"reason":"纠正消费"}`
	for _, tc := range []struct {
		method, body string
		actor        *entity.User
		status       int
	}{{"GET", "", nil, 401}, {"GET", "", &owner, 403}, {"GET", "", &admin, 200}, {"POST", strings.Replace(valid, ":10", ":11", 1), &admin, 404}, {"POST", valid, &admin, 200}, {"POST", valid, &admin, 409}} {
		if actual := request(tc.method, tc.body, tc.actor); actual != tc.status {
			t.Fatalf("%s status %d want %d", tc.method, actual, tc.status)
		}
	}
	if err := db.Model(&entity.User{}).Where("id = ?", admin.ID).Update("role", "user").Error; err != nil {
		t.Fatal(err)
	}
	if actual := request("GET", "", &admin); actual != 403 {
		t.Fatalf("stale role authorized: %d", actual)
	}
	var n int64
	if err := db.Model(&entity.EffectiveAdjustment{}).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("audit count %d %v", n, err)
	}
}
