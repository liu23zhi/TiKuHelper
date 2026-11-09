package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// newTicketAttachmentControllerTestDB 建立附件控制器测试库并注入测试密钥。
func newTicketAttachmentControllerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newTicketControllerTestDB(t)
	if err := db.AutoMigrate(&entity.TicketAttachment{}, &entity.User{}, &entity.Log{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TIKU_TICKET_ATTACHMENT_KEY",
		base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	return db
}

func ticketAttachmentControllerPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for x := 0; x < 16; x++ {
		for y := 0; y < 16; y++ {
			img.Set(x, y, color.RGBA{R: 10, G: uint8(x * 8), B: uint8(y * 8), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// performUploadRequest 构造一个 multipart 上传请求。
func performUploadRequest(t *testing.T, user *entity.User, target, field, filename string, data []byte, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", user)
	ctx.Request = httptest.NewRequest(http.MethodPost, target, &body)
	ctx.Request.Header.Set("Content-Type", writer.FormDataContentType())
	handler(ctx)
	return recorder
}

func seedAttachmentTicket(t *testing.T, db *gorm.DB, ownerID int32) entity.Ticket {
	t.Helper()
	ticket := entity.Ticket{
		UserID: ownerID, UserName: "owner", Subject: "图片工单", Status: entity.TicketStatusOpen,
		Priority: entity.TicketPriorityNormal, LastMessageAt: time.Now(),
	}
	if err := db.Create(&ticket).Error; err != nil {
		t.Fatal(err)
	}
	return ticket
}

// TestUploadTicketAttachmentValidationChain 覆盖需求的「上传：超大文件、非图片、伪装扩展名」。
func TestUploadTicketAttachmentValidationChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketAttachmentControllerTestDB(t)
	withTicketControllerDB(t, db)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedAttachmentTicket(t, db, user.ID)
	target := "/ticket-attachments?ticketId=" + strconv.FormatInt(ticket.ID, 10)

	valid := ticketAttachmentControllerPNG(t)
	oversized := make([]byte, service.MaxTicketAttachmentBytes+1)
	copy(oversized, valid)
	zipDisguised := append([]byte{'P', 'K', 0x03, 0x04}, bytes.Repeat([]byte{0x41}, 64)...)

	cases := []struct {
		name     string
		field    string
		filename string
		data     []byte
		want     int
	}{
		{name: "valid_png", field: "file", filename: "shot.png", data: valid, want: http.StatusCreated},
		{name: "oversized", field: "file", filename: "big.png", data: oversized, want: http.StatusBadRequest},
		{name: "not_image", field: "file", filename: "note.txt", data: []byte("hello"), want: http.StatusBadRequest},
		{name: "zip_disguised_as_jpg", field: "file", filename: "evil.jpg", data: zipDisguised, want: http.StatusBadRequest},
		{name: "wrong_field_name", field: "upload", filename: "shot.png", data: valid, want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := performUploadRequest(t, user, target, tc.field, tc.filename, tc.data, UploadTicketAttachment)
			if recorder.Code != tc.want {
				t.Fatalf("status=%d want %d body=%s", recorder.Code, tc.want, recorder.Body.String())
			}
			if tc.want == http.StatusCreated {
				var payload struct {
					Data service.TicketAttachmentInfo `json:"data"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
					t.Fatalf("decode: %v body=%s", err, recorder.Body.String())
				}
				if payload.Data.ID <= 0 || payload.Data.ScanState != entity.TicketAttachmentScanPending {
					t.Fatalf("unexpected created payload: %+v", payload.Data)
				}
				if payload.Data.Visible {
					t.Fatal("fresh upload must be marked not visible (fail-closed)")
				}
			}
		})
	}
}

// TestUploadTicketAttachmentRequiresTicketOwnership 验证鉴权：别人的工单不能上传。
func TestUploadTicketAttachmentRequiresTicketOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketAttachmentControllerTestDB(t)
	withTicketControllerDB(t, db)
	owner := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	other := &entity.User{ID: 2, Username: "bob", Role: "user", Status: "enabled"}
	if err := db.Create(owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedAttachmentTicket(t, db, owner.ID)
	target := "/ticket-attachments?ticketId=" + strconv.FormatInt(ticket.ID, 10)
	recorder := performUploadRequest(t, other, target, "file", "shot.png", ticketAttachmentControllerPNG(t), UploadTicketAttachment)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("other user upload status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestDownloadTicketAttachmentMarksSensitiveAsHidden 是需求的核心：
// 普通用户拉敏感图必须拿不到字节。
func TestDownloadTicketAttachmentMarksSensitiveAsHidden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketAttachmentControllerTestDB(t)
	withTicketControllerDB(t, db)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	admin := &entity.User{ID: 2, Username: "admin", Role: "admin", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedAttachmentTicket(t, db, user.ID)
	info, err := service.StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentControllerPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	params := gin.Params{{Key: "attachmentId", Value: strconv.FormatInt(info.ID, 10)}}

	// 1) pending：用户拿不到字节（检测未完成也按敏感处理）。
	recorder := performTicketControllerRequestWithParams(t, user, http.MethodGet, "/ticket-attachments/id", params, DownloadTicketAttachment)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "TICKET_ATTACHMENT_HIDDEN") {
		t.Fatalf("pending download status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte("\x89PNG")) {
		t.Fatal("hidden response must not contain image bytes")
	}

	// 2) sensitive：同样拒绝。
	if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", info.ID).
		Updates(map[string]interface{}{"scan_state": entity.TicketAttachmentScanSensitive}).Error; err != nil {
		t.Fatal(err)
	}
	recorder = performTicketControllerRequestWithParams(t, user, http.MethodGet, "/ticket-attachments/id", params, DownloadTicketAttachment)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("sensitive download status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// 3) approved：用户拿到字节。
	if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", info.ID).
		Updates(map[string]interface{}{"scan_state": entity.TicketAttachmentScanApproved}).Error; err != nil {
		t.Fatal(err)
	}
	recorder = performTicketControllerRequestWithParams(t, user, http.MethodGet, "/ticket-attachments/id", params, DownloadTicketAttachment)
	if recorder.Code != http.StatusOK {
		t.Fatalf("approved download status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !bytes.HasPrefix(recorder.Body.Bytes(), []byte("\x89PNG")) {
		t.Fatal("approved download must return png bytes")
	}
	if got := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(got, "sandbox") {
		t.Fatalf("image response must be sandboxed, got %q", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("image response must be nosniff, got %q", got)
	}
}

// TestAdminDownloadSensitiveAttachmentWritesAudit 验证：管理员能看到敏感图，且必有审计记录。
func TestAdminDownloadSensitiveAttachmentWritesAudit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketAttachmentControllerTestDB(t)
	withTicketControllerDB(t, db)
	// recordAccountAudit 走 dao.User，测试里把它指向同一个库。
	withControllerDAO(t, db)

	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	admin := &entity.User{ID: 2, Username: "admin", Role: "admin", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedAttachmentTicket(t, db, user.ID)
	info, err := service.StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentControllerPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", info.ID).
		Updates(map[string]interface{}{"scan_state": entity.TicketAttachmentScanSensitive}).Error; err != nil {
		t.Fatal(err)
	}
	params := gin.Params{{Key: "attachmentId", Value: strconv.FormatInt(info.ID, 10)}}

	// 普通用户走管理端接口：403，且没有任何审计。
	recorder := performTicketControllerRequestWithParams(t, user, http.MethodGet, "/ticket-attachments/id/admin", params, AdminDownloadTicketAttachment)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin must be forbidden, status=%d", recorder.Code)
	}
	var userAudits int64
	if err := db.Model(&entity.Log{}).Where("module = ?", "ticket").Count(&userAudits).Error; err != nil {
		t.Fatal(err)
	}
	if userAudits != 0 {
		t.Fatalf("denied request must not write audit, got %d", userAudits)
	}

	// 管理员：200 + PNG 字节 + 一条审计。
	recorder = performTicketControllerRequestWithParams(t, admin, http.MethodGet, "/ticket-attachments/id/admin", params, AdminDownloadTicketAttachment)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin download status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !bytes.HasPrefix(recorder.Body.Bytes(), []byte("\x89PNG")) {
		t.Fatal("admin must receive sensitive image bytes")
	}
	var audit entity.Log
	if err := db.Where("module = ?", "ticket").Order("id DESC").First(&audit).Error; err != nil {
		t.Fatalf("expected audit log: %v", err)
	}
	if audit.UserID != admin.ID || audit.Username != admin.Username {
		t.Fatalf("audit must record the admin identity, got %+v", audit)
	}
	if audit.Description != "管理员查看工单图片" {
		t.Fatalf("unexpected audit description %q", audit.Description)
	}
	if !strings.Contains(audit.NewAnswer, "sensitive=true") {
		t.Fatalf("audit must record that the image was sensitive, got %s", audit.NewAnswer)
	}
	if !strings.Contains(audit.NewAnswer, "attachmentId="+strconv.FormatInt(info.ID, 10)) {
		t.Fatalf("audit must record the attachment id, got %s", audit.NewAnswer)
	}
}

// TestListTicketAttachmentsKeepsPlaceholders 验证列表接口返回占位条目而不是字节。
func TestListTicketAttachmentsKeepsPlaceholders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTicketAttachmentControllerTestDB(t)
	withTicketControllerDB(t, db)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	other := &entity.User{ID: 2, Username: "bob", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedAttachmentTicket(t, db, user.ID)
	info, err := service.StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentControllerPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", info.ID).
		Updates(map[string]interface{}{"scan_state": entity.TicketAttachmentScanSensitive}).Error; err != nil {
		t.Fatal(err)
	}
	params := gin.Params{{Key: "id", Value: strconv.FormatInt(ticket.ID, 10)}}
	recorder := performTicketControllerRequestWithParams(t, user, http.MethodGet, "/tickets/id/attachments", params, ListTicketAttachments)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data []service.TicketAttachmentInfo `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, recorder.Body.String())
	}
	if len(payload.Data) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(payload.Data))
	}
	if payload.Data[0].Visible || payload.Data[0].HiddenReason == "" {
		t.Fatalf("sensitive attachment must be a placeholder with a reason: %+v", payload.Data[0])
	}
	if strings.Contains(recorder.Body.String(), "ciphertext") {
		t.Fatal("list response must never include ciphertext")
	}
	// 非本人：404（不泄露是否存在）。
	recorder = performTicketControllerRequestWithParams(t, other, http.MethodGet, "/tickets/id/attachments", params, ListTicketAttachments)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("other user list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// performTicketControllerRequestWithParams 是带路径参数的请求辅助。
func performTicketControllerRequestWithParams(t *testing.T, user *entity.User, method, target string, params gin.Params, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("user", user)
	ctx.Params = params
	ctx.Request = httptest.NewRequest(method, target, nil)
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler(ctx)
	return recorder
}

// withControllerDAO 让 recordAccountAudit（走 dao.User/dao.Log）指向测试库。
//
// 与本包既有 b01/D03 测试的做法一致：只安装、不复位。
// dao.SetDefault(nil) 会在 gen 的 UseDB 里空指针 panic，因此不能用作 cleanup。
func withControllerDAO(t *testing.T, db *gorm.DB) {
	t.Helper()
	dao.SetDefault(db)
}
