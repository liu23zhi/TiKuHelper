package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func TestTicketRefundRecoveryRequiresPasswordAndPreviewContainsNoSource(t *testing.T) {
	db := newTicketAttachmentControllerTestDB(t)
	if err := db.AutoMigrate(&entity.TicketRefund{}, &entity.TicketRefundLedger{}, &entity.RechargeOrder{}); err != nil {
		t.Fatal(err)
	}
	owner := seedControllerUser(t, db, 0, "refund-recovery-owner", "user")
	owner.Balance = 20
	owner.Password, _ = util.HashPassword("synthetic password ")
	if err := db.Save(&owner).Error; err != nil {
		t.Fatal(err)
	}
	source := ticketAttachmentControllerPNG(t)
	qr, err := service.StoreTicketAttachmentWithDB(db, &owner, 0, source)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := service.StoreTicketAttachmentWithDB(db, &owner, 0, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.TicketAttachment{}).Where("id IN ?", []int64{qr.ID, signature.ID}).Update("scan_state", entity.TicketAttachmentScanApproved).Error; err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateTicketRefundWithDB(db, owner, service.CreateTicketRefundInput{
		Kind: service.TicketRefundKindManual, RequestedCents: 100, Reason: "fixture", PayoutQrAttachmentID: &qr.ID,
		SignatureAttachmentID: &signature.ID, AgreementText: service.GetRefundAgreementTextWithDB(db),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	old := ticketRefundDB
	ticketRefundDB = func() *gorm.DB { return db }
	t.Cleanup(func() { ticketRefundDB = old })
	actor := owner
	r := gin.New()
	auth := func(c *gin.Context) { c.Set("user", &actor) }
	r.GET("/preview", auth, PreviewTicketRefundPayoutQr)
	r.POST("/recover", auth, RecoverTicketRefundPayoutQr)
	r.GET("/phone", auth, TicketRefundPhoneVerification)
	send := func(method, target, body string) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(out, request)
		return out
	}
	preview := send(http.MethodGet, "/preview", "")
	var thumbnail struct {
		HasPrevious    bool   `json:"hasPrevious"`
		PreviewDataURL string `json:"previewDataUrl"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &thumbnail); err != nil || preview.Code != 200 || !thumbnail.HasPrevious || !strings.HasPrefix(thumbnail.PreviewDataURL, "data:image/png;base64,") {
		t.Fatalf("preview %d %s err=%v", preview.Code, preview.Body, err)
	}
	if strings.Contains(preview.Body.String(), "attachmentId") || strings.Contains(preview.Body.String(), "imageDataUrl") {
		t.Fatal("preview disclosed source reference")
	}
	if preview.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private preview may be cached")
	}
	rejected := send(http.MethodPost, "/recover", `{"password":"incorrect"}`)
	if rejected.Code != 400 || strings.Contains(rejected.Body.String(), "imageDataUrl") {
		t.Fatalf("password bypass: %d %s", rejected.Code, rejected.Body)
	}
	recovered := send(http.MethodPost, "/recover", `{"password":"synthetic password "}`)
	var clear struct {
		AttachmentID int64  `json:"attachmentId"`
		ImageDataURL string `json:"imageDataUrl"`
	}
	if err := json.Unmarshal(recovered.Body.Bytes(), &clear); err != nil || recovered.Code != 200 || clear.AttachmentID != qr.ID || !strings.HasPrefix(clear.ImageDataURL, "data:image/png;base64,") {
		t.Fatalf("recovery %d %s err=%v", recovered.Code, recovered.Body, err)
	}
	if bytes.Equal([]byte(clear.ImageDataURL), []byte(thumbnail.PreviewDataURL)) {
		t.Fatal("preview matches clear image")
	}
	phone := send(http.MethodGet, "/phone", "")
	if phone.Code != 200 || !strings.Contains(phone.Body.String(), `"required":false`) {
		t.Fatalf("phone status: %s", phone.Body)
	}
	actor = seedControllerUser(t, db, 0, "refund-recovery-other", "user")
	other := send(http.MethodGet, "/preview", "")
	if other.Code != 200 || strings.Contains(other.Body.String(), `"hasPrevious":true`) {
		t.Fatalf("other owner sees private preview: %s", other.Body)
	}
}
