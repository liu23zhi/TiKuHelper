package controller

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
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

func TestTicketRefundPayoutDeviceHTTPAndCurrentPasswordRecovery(t *testing.T) {
	db := newTicketAttachmentControllerTestDB(t)
	if err := db.AutoMigrate(&entity.TicketRefund{}, &entity.RefundPayoutQrMemory{}, &entity.RefundPayoutQrBinding{}, &entity.RefundPayoutQrChallenge{}); err != nil {
		t.Fatal(err)
	}
	owner := seedControllerUser(t, db, 0, "payout-device-owner", "user")
	owner.Password, _ = util.HashPassword("previous password")
	if err := db.Save(&owner).Error; err != nil {
		t.Fatal(err)
	}
	actor := owner
	currentHash, _ := util.HashPassword("current password ")
	if err := db.Model(&owner).Update("password", currentHash).Error; err != nil {
		t.Fatal(err)
	}
	qr, err := service.StoreTicketAttachmentWithDB(db, &owner, 0, ticketAttachmentControllerPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	previous := ticketRefundDB
	ticketRefundDB = func() *gorm.DB { return db }
	t.Cleanup(func() { ticketRefundDB = previous })
	r := gin.New()
	auth := func(c *gin.Context) { c.Set("user", &actor) }
	r.GET("/qr/status", auth, StatusTicketRefundPayoutQr)
	r.POST("/qr/register", auth, RegisterTicketRefundPayoutQr)
	r.POST("/qr/challenge", auth, ChallengeTicketRefundPayoutQr)
	r.POST("/qr/device-unlock", auth, UnlockTicketRefundPayoutQrDevice)
	r.POST("/qr/recover", auth, RecoverTicketRefundPayoutQr)
	r.DELETE("/qr", auth, DeleteTicketRefundPayoutQr)
	send := func(method, target string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, target, strings.NewReader(string(encoded)))
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if out.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private QR response was cacheable")
		}
		return out
	}
	if out := send(http.MethodGet, "/qr/status", nil); out.Code != 200 || !strings.Contains(out.Body.String(), `"hasPrevious":false`) {
		t.Fatalf("empty status: %d %s", out.Code, out.Body)
	}
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&private.PublicKey)
	out := send(http.MethodPost, "/qr/register", gin.H{"attachmentId": qr.ID, "publicKey": base64.StdEncoding.EncodeToString(der)})
	var registration service.RefundPayoutQrRegistration
	if err := json.Unmarshal(out.Body.Bytes(), &registration); out.Code != 200 || err != nil || registration.KeyID == "" {
		t.Fatalf("registration: %d %s", out.Code, out.Body)
	}
	out = send(http.MethodGet, "/qr/status", nil)
	if out.Code != 200 || !strings.Contains(out.Body.String(), registration.KeyID) || strings.Contains(out.Body.String(), "attachmentId") || strings.Contains(out.Body.String(), "imageDataUrl") {
		t.Fatalf("status leaked source or omitted key: %d %s", out.Code, out.Body)
	}
	out = send(http.MethodPost, "/qr/challenge", gin.H{})
	var challenge service.RefundPayoutQrDeviceChallenge
	if err := json.Unmarshal(out.Body.Bytes(), &challenge); out.Code != 200 || err != nil || challenge.ChallengeID == "" {
		t.Fatalf("challenge: %d %s", out.Code, out.Body)
	}
	nonce, _ := base64.StdEncoding.DecodeString(challenge.Challenge)
	digest := sha256.Sum256(nonce)
	sigR, sigS, err := ecdsa.Sign(rand.Reader, private, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	var raw [64]byte
	sigR.FillBytes(raw[:32])
	sigS.FillBytes(raw[32:])
	unlock := gin.H{"challengeId": challenge.ChallengeID, "signature": base64.StdEncoding.EncodeToString(raw[:])}
	out = send(http.MethodPost, "/qr/device-unlock", unlock)
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"imageDataUrl":"data:image/png;base64,`) {
		t.Fatalf("device unlock: %d %s", out.Code, out.Body)
	}
	if out := send(http.MethodPost, "/qr/device-unlock", unlock); out.Code != 403 || strings.Contains(out.Body.String(), "imageDataUrl") {
		t.Fatalf("replay: %d %s", out.Code, out.Body)
	}
	if out := send(http.MethodPost, "/qr/recover", gin.H{"password": "previous password"}); out.Code != 400 {
		t.Fatalf("stale cached password accepted: %d %s", out.Code, out.Body)
	}
	if out := send(http.MethodPost, "/qr/recover", gin.H{"password": "current password "}); out.Code != 200 || !strings.Contains(out.Body.String(), "imageDataUrl") {
		t.Fatalf("current password recovery: %d %s", out.Code, out.Body)
	}
	var binding entity.RefundPayoutQrBinding
	if err := db.First(&binding, "attachment_id = ?", qr.ID).Error; err != nil || binding.KeyID != registration.KeyID {
		t.Fatal("password recovery rewrote uploading device")
	}
	if out := send(http.MethodDelete, "/qr", nil); out.Code != 200 {
		t.Fatalf("delete: %d %s", out.Code, out.Body)
	}
	if out := send(http.MethodGet, "/qr/status", nil); out.Code != 200 || !strings.Contains(out.Body.String(), `"hasPrevious":false`) {
		t.Fatalf("status after delete: %d %s", out.Code, out.Body)
	}
	if out := send(http.MethodPost, "/qr/recover", gin.H{"password": "current password "}); out.Code != 200 || !strings.Contains(out.Body.String(), `"attachmentId":0`) {
		t.Fatalf("deleted image recovered: %d %s", out.Code, out.Body)
	}
}
