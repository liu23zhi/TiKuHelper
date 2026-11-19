package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestTicketRefundOrderChoicesHTTPListsDisabledOrdersWithoutLeakingTradeMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TIKU_REFUND_EPAY_PROFILE", "")
	db := newSearchControllerTestDB(t)
	if err := db.AutoMigrate(&entity.RechargeOrder{}, &entity.TicketRefund{}); err != nil {
		t.Fatal(err)
	}
	owner := seedControllerUser(t, db, 0, "choice-owner", "user")
	other := seedControllerUser(t, db, 0, "choice-other", "user")
	now := time.Now()
	old := now.Add(-61 * 24 * time.Hour)
	for i, row := range []entity.RechargeOrder{
		{UserID: owner.ID, Status: "paid", PaidAt: &now, CreatedAt: now, ThirdPartyOrderNo: ""},
		{UserID: owner.ID, Status: "paid", PaidAt: &old, CreatedAt: old, ThirdPartyOrderNo: "private-trade-number"},
		{UserID: owner.ID, Status: "refunded", CreatedAt: now},
		{UserID: owner.ID, Status: "pending", CreatedAt: now},
		{UserID: other.ID, Status: "paid", PaidAt: &now, CreatedAt: now, ThirdPartyOrderNo: "private-other-trade"},
	} {
		row.OrderNo = "choice-order-" + string(rune('a'+i))
		row.PaymentMethodNameSnapshot, row.PaymentProvider, row.Amount = "测试", "epay", 7.5
		row.CallbackPayload, row.RefundChannelFingerprint = "private-callback", "private-fingerprint"
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	previous := ticketRefundDB
	ticketRefundDB = func() *gorm.DB { return db }
	t.Cleanup(func() { ticketRefundDB = previous })
	router := gin.New()
	router.GET("/orders", func(c *gin.Context) { c.Set("user", &owner) }, EligibleTicketRefundOrders)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/orders", nil))
	var items []service.TicketRefundOrder
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil || recorder.Code != http.StatusOK || len(items) != 3 {
		t.Fatalf("disabled nonpending orders must remain in the HTTP array: %d %s, %v", recorder.Code, recorder.Body, err)
	}
	for _, item := range items {
		if item.OriginalSupported || item.OriginalUnsupportedReason == "" || item.Status == "pending" || item.CreatedAt.IsZero() {
			t.Fatalf("incomplete disabled-order response: %+v", item)
		}
	}
	for _, private := range []string{"private-trade-number", "private-other-trade", "private-callback", "private-fingerprint"} {
		if strings.Contains(recorder.Body.String(), private) {
			t.Fatal("private gateway metadata exposed in order choices")
		}
	}
}
