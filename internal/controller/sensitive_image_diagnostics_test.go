package controller

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

func TestT12SensitiveImageAdminReceipts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newSensitiveImageControllerTestDB(t)
	installSensitiveImageAdminTestDB(t, db)
	admin := sensitiveImageSeedActor(t, db, 9210, "admin")
	router := sensitiveImageAdminRouter(&admin)
	dir := t.TempDir()
	old := *logger.LogDir
	*logger.LogDir = dir
	if err := logger.SetupGinLog(); err != nil {
		*logger.LogDir = old
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close(); *logger.LogDir = old })
	t.Setenv("TIKU_TICKET_ATTACHMENT_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	badSHA := strings.Repeat("b", 64)
	if err := db.Create(&entity.TicketAttachment{
		TicketID: 9001, UserID: admin.ID, SHA256: badSHA, MediaType: "image/png",
		ByteSize: 43, ScanState: entity.TicketAttachmentScanSensitive,
		Nonce: []byte("corrupt"), Ciphertext: []byte("synthetic no ciphertext"),
	}).Error; err != nil {
		t.Fatal(err)
	}
	original, err := service.StoreTicketAttachmentWithDB(db, &admin, 0, ticketAttachmentControllerPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	var available entity.TicketAttachment
	if err := db.First(&available, original.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.SensitiveImageCacheEntry{
		ImageSHA256: badSHA, Status: entity.SensitiveImageCacheStatusError,
		Reason: "/private/fault-path SYNTHETIC_LEGACY_RAW_ERROR",
	}).Error; err != nil {
		t.Fatal(err)
	}
	history := sensitiveImageRequest(router, http.MethodGet, "/api/sensitive-image/entries?status=error", "")
	if history.Code != http.StatusOK || strings.Contains(history.Body.String(), "SYNTHETIC_LEGACY_RAW_ERROR") {
		t.Errorf("history exposes an untrusted old reason: %d %s", history.Code, history.Body)
	}
	for _, c := range []struct {
		sha    string
		status int
		code   string
	}{
		{strings.Repeat("a", 64), http.StatusNotFound, "SENSITIVE_IMAGE_BYTES_UNAVAILABLE"},
		{badSHA, http.StatusInternalServerError, "SENSITIVE_IMAGE_READ_FAILED"},
		{available.SHA256, http.StatusBadGateway, "SENSITIVE_IMAGE_DETECTOR_UNCONFIGURED"},
	} {
		res := sensitiveImageRequest(router, http.MethodPost, "/api/sensitive-image/detect", `{"imageSha256":"`+c.sha+`"}`)
		var body struct {
			Code string `json:"code"`
			ID   string `json:"diagnosticId"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if res.Code != c.status || body.Code != c.code || !regexp.MustCompile(`^IMG-[0-9a-f]{32}$`).MatchString(body.ID) {
			t.Errorf("expected %d %q and local receipt, got %d %s", c.status, c.code, res.Code, res.Body)
		}
		log, err := os.ReadFile(filepath.Join(dir, "Tiku-sensitive-image.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if body.ID == "" || !strings.Contains(string(log), "diagnostic_id="+body.ID) || strings.Contains(string(log), "synthetic no ciphertext") {
			t.Error("admin receipt does not safely resolve to the dedicated log")
		}
	}
	res := sensitiveImageRequest(router, http.MethodPost, "/api/sensitive-image/detect", `{"ticketId":9001}`)
	var batch struct {
		Detected int `json:"detected"`
		Skipped  int `json:"skipped"`
		Failures []struct {
			Code string `json:"code"`
			ID   string `json:"diagnosticId"`
		} `json:"failures"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &batch); err != nil {
		t.Fatal(err)
	}
	if res.Code != 200 || batch.Detected != 0 || batch.Skipped != 1 || len(batch.Failures) != 1 ||
		batch.Failures[0].Code != "SENSITIVE_IMAGE_READ_FAILED" || !regexp.MustCompile(`^IMG-[0-9a-f]{32}$`).MatchString(batch.Failures[0].ID) {
		t.Errorf("batch hides failed-image receipt: %d %s", res.Code, res.Body)
	}
}
