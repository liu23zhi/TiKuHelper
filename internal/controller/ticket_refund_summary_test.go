package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestTicketRefundSummaryHTTPUsesCurrentAuthenticatedWallet(t *testing.T) {
	db := newSearchControllerTestDB(t)
	owner := seedControllerUser(t, db, 0, "refund-summary-owner", "user")
	other := seedControllerUser(t, db, 0, "refund-summary-other", "user")
	actor := owner
	actor.Balance = 9999
	if err := db.Model(&owner).Updates(map[string]any{"balance": 25.000001, "gift_balance": 1000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&other).Update("balance", 99999).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range []entity.BillingRecord{
		{UserID: owner.ID, Type: "normal_search", GiftCharged: 15, Amount: 15, RequestID: "summary-owner-gift-consumption"},
		{UserID: other.ID, Type: "normal_search", GiftCharged: 100, Amount: 100, RequestID: "summary-other-gift-consumption"},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	previous := ticketRefundDB
	ticketRefundDB = func() *gorm.DB { return db }
	t.Cleanup(func() { ticketRefundDB = previous })
	r := gin.New()
	authenticated := true
	r.GET("/api/ticket-refunds/summary", func(c *gin.Context) {
		if authenticated {
			c.Set("user", &actor)
		}
	}, GetTicketRefundSummary)
	send := func(target string) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		r.ServeHTTP(out, httptest.NewRequest(http.MethodGet, target, nil))
		return out
	}
	out := send("/api/ticket-refunds/summary?userId=" + strconv.FormatInt(int64(other.ID), 10) + "&balance=9999")
	if out.Code != 200 || out.Body.String() != `{"refundableCents":1001}` {
		t.Fatalf("summary status=%d body=%s", out.Code, out.Body)
	}
	if out.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("wallet summary may be cached")
	}
	if err := db.Model(&owner).Update("balance", -1).Error; err != nil {
		t.Fatal(err)
	}
	out = send("/api/ticket-refunds/summary")
	if out.Code != 200 || out.Body.String() != `{"refundableCents":0}` {
		t.Fatalf("debt summary: %d %s", out.Code, out.Body)
	}
	if err := db.Model(&owner).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if out := send("/api/ticket-refunds/summary"); out.Code != 403 {
		t.Fatalf("cached enabled account accepted: %d %s", out.Code, out.Body)
	}
	authenticated = false
	if out := send("/api/ticket-refunds/summary"); out.Code != 401 {
		t.Fatalf("anonymous summary: %d %s", out.Code, out.Body)
	}
	authenticated = true
	if err := db.Model(&owner).Update("status", "enabled").Error; err != nil {
		t.Fatal(err)
	}
	failure := errors.New("synthetic summary query failure")
	if err := db.Callback().Query().Before("gorm:query").Register("summary_http_db_failure", func(tx *gorm.DB) { tx.AddError(failure) }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("summary_http_db_failure") })
	out = send("/api/ticket-refunds/summary")
	var response map[string]any
	if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil || out.Code != 500 || response["code"] != "REFUND_ERROR" || response["refundableCents"] != nil {
		t.Fatalf("failed summary returned a balance: %d %s", out.Code, out.Body)
	}
}
