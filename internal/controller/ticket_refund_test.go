package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
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

func TestTicketRefundHTTPManualProofAccessAndSettlement(t *testing.T) {
	t.Setenv("TIKU_REFUND_EPAY_PROFILE", "")
	t.Setenv("TIKU_REFUND_PROOF_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{13}, 32)))
	db := newSearchControllerTestDB(t)
	if err := db.AutoMigrate(&entity.RechargeOrder{}, &entity.TicketRefund{}, &entity.TicketRefundLedger{}, &entity.TicketRefundProof{}); err != nil {
		t.Fatal(err)
	}
	owner := seedControllerUser(t, db, 0, "refund-proof-owner", "user")
	admin := seedControllerUser(t, db, 0, "refund-proof-admin", "admin")
	other := seedControllerUser(t, db, 0, "refund-proof-other", "user")
	if err := db.Model(&owner).UpdateColumn("balance", 23.123456).Error; err != nil {
		t.Fatal(err)
	}
	previous := ticketRefundDB
	ticketRefundDB = func() *gorm.DB { return db }
	t.Cleanup(func() { ticketRefundDB = previous })
	r := gin.New()
	actor := owner
	auth := func(c *gin.Context) { c.Set("user", &actor) }
	r.GET("/refunds/eligible-orders", auth, EligibleTicketRefundOrders)
	r.POST("/refunds", auth, CreateTicketRefund)
	r.GET("/refunds/:id", auth, TicketRefundDetail)
	r.POST("/refunds/:id/decision", auth, ReviewTicketRefund)
	r.POST("/refunds/:id/reconcile", auth, ReconcileManualTicketRefund)
	r.POST("/refunds/:id/proof", auth, UploadTicketRefundProof)
	r.GET("/refunds/:id/proof", auth, DownloadTicketRefundProof)
	send := func(method, target, kind string, body io.Reader) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, body)
		if kind != "" {
			req.Header.Set("Content-Type", kind)
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	post := func(target, body string) *httptest.ResponseRecorder {
		return send(http.MethodPost, target, "application/json", strings.NewReader(body))
	}
	checkBalance := func(expected float64) {
		t.Helper()
		var fresh entity.User
		if err := db.First(&fresh, owner.ID).Error; err != nil || fresh.Balance != expected {
			t.Fatalf("expected wallet %.6f; got %.6f err=%v", expected, fresh.Balance, err)
		}
	}
	out := send(http.MethodGet, "/refunds/eligible-orders", "", nil)
	if out.Code != 200 || out.Body.String() != "[]" {
		t.Fatalf("unverified channel must not advertise eligibility: %d %s", out.Code, out.Body)
	}
	out = post("/refunds", `{"kind":"manual","requestedCents":600,"reason":"synthetic manual review"}`)
	var refund ticketRefundResponse
	if err := json.Unmarshal(out.Body.Bytes(), &refund); out.Code != 201 || err != nil || refund.ID <= 0 || refund.HasProof {
		t.Fatalf("create manual refund failed: %d %s", out.Code, out.Body)
	}
	base := fmt.Sprintf("/refunds/%d", refund.ID)
	checkBalance(23.123456)
	actor = admin
	out = post(base+"/decision", `{"action":"approve","approvedCents":550}`)
	if out.Code != 409 || !strings.Contains(out.Body.String(), "REFUND_PROOF_REQUIRED") {
		t.Fatalf("manual approval without proof not refused: %d %s", out.Code, out.Body)
	}
	checkBalance(23.123456)

	actor = owner
	var fixture bytes.Buffer
	if err := png.Encode(&fixture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	upload := func(body []byte) *httptest.ResponseRecorder {
		var payload bytes.Buffer
		mp := multipart.NewWriter(&payload)
		part, err := mp.CreateFormFile("file", "synthetic-private-name.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := mp.Close(); err != nil {
			t.Fatal(err)
		}
		return send(http.MethodPost, base+"/proof", mp.FormDataContentType(), &payload)
	}
	if out := upload([]byte("<svg/>")); out.Code != 400 {
		t.Fatalf("non-image proof accepted: %d %s", out.Code, out.Body)
	}
	if out := upload(fixture.Bytes()); out.Code != 201 || !strings.Contains(out.Body.String(), `"hasProof":true`) {
		t.Fatalf("owner proof upload failed: %d %s", out.Code, out.Body)
	}
	if out := upload(fixture.Bytes()); out.Code != 409 {
		t.Fatalf("proof was overwritten: %d %s", out.Code, out.Body)
	}
	var sealed entity.TicketRefundProof
	if err := db.First(&sealed, refund.ID).Error; err != nil || bytes.Equal(sealed.Ciphertext, fixture.Bytes()) ||
		bytes.Contains(sealed.Ciphertext, []byte("synthetic-private-name")) || len(sealed.Nonce) != 12 {
		t.Fatalf("proof storage must be authenticated ciphertext without file metadata: %v", err)
	}
	actor = other
	if out := send(http.MethodGet, base+"/proof", "", nil); out.Code != 403 && out.Code != 404 {
		t.Fatalf("cross-owner proof disclosure: %d", out.Code)
	}
	actor = admin
	out = send(http.MethodGet, base+"/proof", "", nil)
	if out.Code != 200 || out.Header().Get("Cache-Control") != "no-store" ||
		out.Header().Get("Content-Type") != "image/png" ||
		strings.Contains(out.Header().Get("Content-Disposition"), "synthetic-private-name") {
		t.Fatalf("restricted download failed: %d", out.Code)
	}
	if _, err := png.Decode(out.Body); err != nil {
		t.Fatal(err)
	}
	out = post(base+"/decision", `{"action":"approve","approvedCents":550}`)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"status":"frozen"`) {
		t.Fatalf("manual approval failed: %d %s", out.Code, out.Body)
	}
	checkBalance(17.623456)
	actor = owner
	if out := post(base+"/reconcile", `{"outcome":"failed","reference":"fixture-no-payout"}`); out.Code != 403 {
		t.Fatalf("user reconciled their own refund: %d", out.Code)
	}
	actor = admin
	for i := 0; i < 2; i++ {
		out = post(base+"/reconcile", `{"outcome":"failed","reference":"fixture-no-payout"}`)
		if out.Code != 200 || !strings.Contains(out.Body.String(), `"status":"failed"`) ||
			strings.Contains(out.Body.String(), "fixture-no-payout") {
			t.Fatalf("manual reconciliation failed or leaked reference: %d %s", out.Code, out.Body)
		}
		checkBalance(23.123456)
	}
	if out := post(base+"/reconcile", `{"outcome":"succeeded","reference":"fixture-contradiction"}`); out.Code != 409 {
		t.Fatalf("contradictory terminal transition allowed: %d", out.Code)
	}
	if refund.Kind != service.TicketRefundKindManual {
		t.Fatal("manual request kind changed")
	}
}

func TestTicketRefundHTTPDisabledChannelDoesNotDispatchAndUnsentCanCancel(t *testing.T) {
	t.Setenv("TIKU_REFUND_EPAY_PROFILE", "")
	db := newSearchControllerTestDB(t)
	if err := db.AutoMigrate(&entity.RechargeOrder{}, &entity.TicketRefund{}, &entity.TicketRefundLedger{}, &entity.TicketRefundProof{}); err != nil {
		t.Fatal(err)
	}
	owner := seedControllerUser(t, db, 0, "refund-channel-owner", "user")
	admin := seedControllerUser(t, db, 0, "refund-channel-admin", "admin")
	if err := db.Model(&owner).UpdateColumn("balance", 40).Error; err != nil {
		t.Fatal(err)
	}
	paidAt := time.Now().Add(-time.Hour)
	order := entity.RechargeOrder{UserID: owner.ID, OrderNo: "fixture-merchant-order", PaymentProvider: "epay",
		Amount: 20, Status: "paid", PaidAt: &paidAt, ThirdPartyOrderNo: "fixture-gateway-order",
		RefundOriginalTradeNo: "fixture-gateway-order", RefundChannelFingerprint: strings.Repeat("a", 64)}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	verify := func(_ *gorm.DB, original entity.RechargeOrder) (string, error) {
		return original.RefundChannelFingerprint, nil
	}
	refund, err := service.CreateTicketRefundWithDB(db, owner, service.CreateTicketRefundInput{
		Kind: service.TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 600,
	}, verify)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewTicketRefundWithDB(db, admin, refund.ID, true, 500, verify); err != nil {
		t.Fatal(err)
	}
	previous := ticketRefundDB
	ticketRefundDB = func() *gorm.DB { return db }
	t.Cleanup(func() { ticketRefundDB = previous })
	router := gin.New()
	router.POST("/refunds/:id/process", func(c *gin.Context) { c.Set("user", &admin) }, ProcessTicketRefund)
	send := func(action string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/refunds/%d/process", refund.ID), strings.NewReader(fmt.Sprintf(`{"action":%q}`, action)))
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out
	}
	if out := send("submit"); out.Code != http.StatusServiceUnavailable || !strings.Contains(out.Body.String(), "REFUND_CHANNEL_UNAVAILABLE") {
		t.Fatalf("disabled profile allowed dispatch: %d %s", out.Code, out.Body)
	}
	var current entity.TicketRefund
	if err := db.First(&current, refund.ID).Error; err != nil || current.Status != service.TicketRefundStatusFrozen || current.StartedAt != nil {
		t.Fatalf("disabled profile mutated dispatch state: %v", err)
	}
	for i := 0; i < 2; i++ {
		if out := send("cancel_unsent"); out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"status":"failed"`) {
			t.Fatalf("unsent cancellation failed: %d %s", out.Code, out.Body)
		}
	}
	if err := db.First(&owner, owner.ID).Error; err != nil || owner.Balance != 40 {
		t.Fatalf("unsent hold did not restore wallet exactly once: %.6f %v", owner.Balance, err)
	}
	if err := db.First(&order, order.ID).Error; err != nil || order.Status != "paid" {
		t.Fatalf("refund operation rewrote paid order: %v", err)
	}
}
