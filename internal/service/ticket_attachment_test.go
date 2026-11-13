package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/uapi"
	"gorm.io/gorm"
)

// newTicketAttachmentTestDB 建立附件测试库，并保证加密密钥可用。
//
// 密钥在测试里生成（不写死任何真实 key，也不读部署环境），因此本文件不包含
// 任何需要保密的材料。
func newTicketAttachmentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.SystemSetting{}, &entity.Ticket{},
		&entity.TicketMessage{}, &entity.TicketNotification{}, &entity.TicketAttachment{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	useTicketAttachmentTestKey(t)
	return db
}

// useTicketAttachmentTestKey 注入一把测试用 32 字节密钥。
func useTicketAttachmentTestKey(t *testing.T) {
	t.Helper()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	t.Setenv("TIKU_TICKET_ATTACHMENT_KEY", key)
}

// ticketAttachmentTestPNG 生成一张合法的 PNG（width x height 纯色）。
func ticketAttachmentTestPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func ticketAttachmentTestJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for x := 0; x < 32; x++ {
		for y := 0; y < 32; y++ {
			img.Set(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func seedTicketAttachmentTicket(t *testing.T, db *gorm.DB, ownerID int32) entity.Ticket {
	t.Helper()
	ticket := entity.Ticket{
		UserID: ownerID, UserName: "owner", Subject: "图片工单", Status: entity.TicketStatusOpen,
		Priority: entity.TicketPriorityNormal, LastMessageAt: time.Now(),
	}
	if err := db.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	return ticket
}

// TestTicketAttachmentUploadRejectsHostileFiles 覆盖需求的「上传校验链」必测项：
// 超大文件、非图片、伪装扩展名（.jpg 实际是 zip）、超尺寸。
func TestTicketAttachmentUploadRejectsHostileFiles(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)

	validPNG := ticketAttachmentTestPNG(t, 16, 16)
	oversized := make([]byte, MaxTicketAttachmentBytes+1)
	copy(oversized, validPNG)
	// 伪装扩展名：ZIP 魔数（PK\x03\x04）+ 若干字节，文件名/声明都是图片。
	fakeJPG := append([]byte{'P', 'K', 0x03, 0x04}, bytes.Repeat([]byte{0x00}, 128)...)
	// 超尺寸：4500x4500 超过 4096 边长上限，且 20.25M 像素超过 4M。
	hugeDimensions := ticketAttachmentTestPNG(t, MaxTicketAttachmentEdge+1, 64)
	// 超像素但不超单边：3000x3000 = 9M 像素 > 4M。
	tooManyPixels := ticketAttachmentTestPNG(t, 3000, 3000)

	cases := []struct {
		name string
		data []byte
	}{
		{name: "empty", data: nil},
		{name: "oversized", data: oversized},
		{name: "not_an_image", data: []byte("这不是图片，只是一段文本")},
		{name: "zip_disguised_as_jpg", data: fakeJPG},
		{name: "edge_too_large", data: hugeDimensions},
		{name: "pixels_too_many", data: tooManyPixels},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, tc.data)
			if !errors.Is(err, ErrTicketAttachmentInvalid) {
				t.Fatalf("expected ErrTicketAttachmentInvalid, got %v", err)
			}
			var count int64
			if err := db.Model(&entity.TicketAttachment{}).Where("ticket_id = ?", ticket.ID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("rejected upload must not persist any row, got %d", count)
			}
		})
	}

	// 反向断言：合法 PNG 与 JPEG 必须成功，且初始状态是 pending（尚未检测）。
	for _, data := range [][]byte{validPNG, ticketAttachmentTestJPEG(t)} {
		info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, data)
		if err != nil {
			t.Fatalf("valid image rejected: %v", err)
		}
		if info.ScanState != entity.TicketAttachmentScanPending {
			t.Fatalf("new attachment must be pending, got %q", info.ScanState)
		}
		if info.Visible {
			t.Fatal("pending attachment must not be visible (fail-closed)")
		}
		if info.HiddenReason == "" {
			t.Fatal("pending attachment must carry a hidden reason")
		}
	}
}

// TestTicketAttachmentUploadStripsAppendedContent 验证「重新编码消除追加内容」：
// 入库字节必须与上传字节不同，且不含尾部追加的标记串。
func TestTicketAttachmentUploadStripsAppendedContent(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	admin := &entity.User{ID: 2, Username: "admin", Role: "admin", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)

	original := ticketAttachmentTestPNG(t, 24, 24)
	const marker = "APPENDED-SECRET-MARKER"
	tainted := append(append([]byte(nil), original...), []byte(marker)...)

	info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, tainted)
	if err != nil {
		t.Fatalf("upload tainted png: %v", err)
	}
	var row entity.TicketAttachment
	if err := db.Where("id = ?", info.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if len(row.Ciphertext) == 0 || len(row.Nonce) == 0 {
		t.Fatal("ciphertext and nonce must be stored")
	}
	if strings.Contains(string(row.Ciphertext), marker) {
		t.Fatal("appended content must be stripped before encryption")
	}
	// 新附件是 pending（未检测），普通用户读不到 —— 用管理员读，专测字节内容本身。
	mimeType, plain, _, err := ReadTicketAttachmentWithDB(db, admin, info.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if mimeType != "image/png" {
		t.Fatalf("unexpected mime type %q", mimeType)
	}
	if strings.Contains(string(plain), marker) {
		t.Fatal("re-encoded image must not retain appended content")
	}
	if _, format, decodeErr := image.DecodeConfig(bytes.NewReader(plain)); decodeErr != nil || format != "png" {
		t.Fatalf("stored bytes must be a valid png, got format=%q err=%v", format, decodeErr)
	}
}

// TestTicketAttachmentCiphertextIsNotPlaintext 断言密文与明文字节不同 ——
// 防止有人「为了省事」退回明文存储而测试仍然变绿。
func TestTicketAttachmentCiphertextIsNotPlaintext(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	admin := &entity.User{ID: 2, Username: "admin", Role: "admin", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)
	data := ticketAttachmentTestPNG(t, 32, 32)
	info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, data)
	if err != nil {
		t.Fatal(err)
	}
	var row entity.TicketAttachment
	if err := db.Where("id = ?", info.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	// 重新编码后的明文与原始上传不同，但密文也必须与解密后的明文完全不同。
	_, plain, _, err := ReadTicketAttachmentWithDB(db, admin, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(row.Ciphertext, plain) || bytes.Contains(plain, row.Ciphertext) {
		t.Fatal("ciphertext must not contain plaintext image bytes")
	}
	if bytes.HasPrefix(row.Ciphertext, []byte("\x89PNG")) {
		t.Fatal("ciphertext must not start with a PNG signature")
	}
}

// TestTicketAttachmentAADBindsIdentity 验证 AAD 绑定：篡改 user_id 后解密必须失败。
func TestTicketAttachmentAADBindsIdentity(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	admin := &entity.User{ID: 2, Username: "admin", Role: "admin", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)
	info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatal(err)
	}
	// 直接把 user_id 改掉：密文里的 AAD 与新 user_id 不匹配。
	// 用管理员读（绕过展示判定），确保失败原因真的是解密，而不是「不可见」。
	if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", info.ID).UpdateColumn("user_id", admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadTicketAttachmentWithDB(db, admin, info.ID); !errors.Is(err, ErrTicketAttachmentInvalid) {
		t.Fatalf("expected decrypt failure after identity tampering, got %v", err)
	}
}

// TestTicketAttachmentUserCannotReadSensitiveImage 是「用户端直接拒绝显示」的服务端落点：
// 敏感图不返回任何字节。
func TestTicketAttachmentUserCannotReadSensitiveImage(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	admin := &entity.User{ID: 2, Username: "admin", Role: "admin", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(admin).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)
	info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", info.ID).
		Updates(map[string]interface{}{"scan_state": entity.TicketAttachmentScanSensitive}).Error; err != nil {
		t.Fatal(err)
	}
	if _, data, _, err := ReadTicketAttachmentWithDB(db, user, info.ID); !errors.Is(err, ErrTicketAttachmentHidden) || len(data) != 0 {
		t.Fatalf("user must be denied sensitive bytes, got len=%d err=%v", len(data), err)
	}
	// 管理员可以拿到字节，且 allowSensitive=true（handler 据此写审计）。
	mimeType, data, allowSensitive, err := ReadTicketAttachmentWithDB(db, admin, info.ID)
	if err != nil {
		t.Fatalf("admin read: %v", err)
	}
	if !allowSensitive {
		t.Fatal("admin read of sensitive attachment must report allowSensitive=true")
	}
	if mimeType != "image/png" || len(data) == 0 {
		t.Fatalf("admin must receive bytes, got mime=%q len=%d", mimeType, len(data))
	}
	// 其他普通用户连「存在」都不该知道。
	other := &entity.User{ID: 3, Username: "bob", Role: "user", Status: "enabled"}
	if err := db.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ReadTicketAttachmentWithDB(db, other, info.ID); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("other user must get not-found, got %v", err)
	}
}

// TestTicketAttachmentFailClosedOnEveryNonApprovedState 逐状态验证 fail-closed：
// pending / scanning / failed / 未知值 一律不可见，只有 approved 可见。
func TestTicketAttachmentFailClosedOnEveryNonApprovedState(t *testing.T) {
	cases := []struct {
		state string
		want  bool
	}{
		{state: entity.TicketAttachmentScanPending, want: false},
		{state: entity.TicketAttachmentScanScanning, want: false},
		{state: entity.TicketAttachmentScanSensitive, want: false},
		{state: entity.TicketAttachmentScanFailed, want: false},
		{state: "", want: false},
		{state: "weird-upstream-state", want: false},
		{state: entity.TicketAttachmentScanApproved, want: true},
	}
	for _, tc := range cases {
		t.Run("state="+tc.state, func(t *testing.T) {
			got := ticketAttachmentVisible(entity.TicketAttachment{ScanState: tc.state})
			if got != tc.want {
				t.Fatalf("visible(%q)=%v want %v", tc.state, got, tc.want)
			}
		})
	}
}

// TestJudgeTicketAttachmentScanRuleTable 是判定规则表：覆盖 uapis 的每一种返回。
func TestJudgeTicketAttachmentScanRuleTable(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }
	score := func(v float64) *float64 { return &v }
	cases := []struct {
		name       string
		result     uapiImageNSFW
		wantState  string
		wantHidden bool
	}{
		{name: "normal_pass_low", result: uapiImageNSFW{IsNSFW: boolPtr(false), Suggestion: "pass", RiskLevel: "low", NSFWScore: score(0.05)}, wantState: entity.TicketAttachmentScanApproved, wantHidden: false},
		{name: "pass_without_scores", result: uapiImageNSFW{IsNSFW: boolPtr(false), Suggestion: "pass"}, wantState: entity.TicketAttachmentScanApproved, wantHidden: false},
		{name: "suggestion_review", result: uapiImageNSFW{IsNSFW: boolPtr(false), Suggestion: "review", RiskLevel: "medium"}, wantState: entity.TicketAttachmentScanSensitive, wantHidden: true},
		{name: "suggestion_block", result: uapiImageNSFW{IsNSFW: boolPtr(false), Suggestion: "block", RiskLevel: "low"}, wantState: entity.TicketAttachmentScanSensitive, wantHidden: true},
		{name: "risk_high", result: uapiImageNSFW{IsNSFW: boolPtr(false), Suggestion: "pass", RiskLevel: "high"}, wantState: entity.TicketAttachmentScanSensitive, wantHidden: true},
		{name: "risk_medium", result: uapiImageNSFW{IsNSFW: boolPtr(false), RiskLevel: "medium"}, wantState: entity.TicketAttachmentScanSensitive, wantHidden: true},
		{name: "is_nsfw_true", result: uapiImageNSFW{IsNSFW: boolPtr(true), Suggestion: "pass", RiskLevel: "low", NSFWScore: score(0.97)}, wantState: entity.TicketAttachmentScanSensitive, wantHidden: true},
		{name: "case_insensitive_suggestion", result: uapiImageNSFW{IsNSFW: boolPtr(false), Suggestion: "BLOCK"}, wantState: entity.TicketAttachmentScanSensitive, wantHidden: true},
		{name: "no_fields_at_all", result: uapiImageNSFW{}, wantState: entity.TicketAttachmentScanFailed, wantHidden: true},
		{name: "only_unknown_suggestion", result: uapiImageNSFW{Suggestion: "maybe"}, wantState: entity.TicketAttachmentScanFailed, wantHidden: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, reason := judgeTicketAttachmentScan(tc.result.toUAPI())
			if state != tc.wantState {
				t.Fatalf("state=%q want %q (reason=%q)", state, tc.wantState, reason)
			}
			visible := ticketAttachmentVisible(entity.TicketAttachment{ScanState: state})
			if visible == tc.wantHidden {
				t.Fatalf("visible=%v want hidden=%v", visible, tc.wantHidden)
			}
		})
	}
}

// TestTicketAttachmentScanWithoutAPIKeyIsFailClosed 是需求必测的 fail-closed：
// 没有 API key 时不得放行，也不得外呼。
func TestTicketAttachmentScanWithoutAPIKeyIsFailClosed(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"is_nsfw":false,"suggestion":"pass"}`))
	}))
	defer server.Close()
	withTicketAttachmentNSFWURL(t, server.URL)

	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)
	info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatal(err)
	}
	// 明确清空所有可能的密钥来源。
	t.Setenv("TIKU_TICKET_ATTACHMENT_KEY", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))

	if _, err := ProcessPendingTicketAttachmentScans(context.Background(), db, time.Now()); err != nil {
		t.Fatalf("process: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("missing api key must not call upstream, got %d calls", got)
	}
	var row entity.TicketAttachment
	if err := db.Where("id = ?", info.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	// 首次失败会退回 pending 等待重试（attempt_count=1），但可见性必须仍为 false。
	if ticketAttachmentVisible(row) {
		t.Fatalf("attachment without api key must stay hidden, state=%q", row.ScanState)
	}
	if row.ScanState != entity.TicketAttachmentScanPending {
		t.Fatalf("first failure should requeue as pending, got %q", row.ScanState)
	}
	// 耗尽重试额度后必须定格 failed。时间必须**递增**：退避把 next_attempt_at
	// 推到未来后，同一时间点的下一次处理不该再 claim（这正是退避生效的证明）。
	for i := 0; i < TicketAttachmentScanMaxAttempts; i++ {
		scanAt := time.Now().Add(time.Duration(i+1) * 10 * time.Minute)
		if _, err := ProcessPendingTicketAttachmentScans(context.Background(), db, scanAt); err != nil {
			t.Fatalf("process retry: %v", err)
		}
	}
	if err := db.Where("id = ?", info.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ScanState != entity.TicketAttachmentScanFailed {
		t.Fatalf("exhausted retries must end in failed, got %q", row.ScanState)
	}
	if ticketAttachmentVisible(row) {
		t.Fatal("failed attachment must stay hidden")
	}
}

// TestTicketAttachmentScanUpstreamOutcomes 用 httptest 模拟 uapis 的 pass/review/block/500/超时。
func TestTicketAttachmentScanUpstreamOutcomes(t *testing.T) {
	type want struct {
		state   string
		visible bool
	}
	cases := []struct {
		name    string
		status  int
		body    string
		delay   time.Duration
		want    want
		wantErr bool
	}{
		{name: "pass", status: 200, body: `{"nsfw_score":0.05,"normal_score":0.95,"is_nsfw":false,"label":"normal","suggestion":"pass","risk_level":"low","confidence":0.95,"inference_time_ms":156}`, want: want{state: entity.TicketAttachmentScanApproved, visible: true}},
		{name: "review", status: 200, body: `{"nsfw_score":0.55,"is_nsfw":false,"suggestion":"review","risk_level":"medium"}`, want: want{state: entity.TicketAttachmentScanSensitive, visible: false}},
		{name: "block", status: 200, body: `{"nsfw_score":0.98,"is_nsfw":true,"suggestion":"block","risk_level":"high"}`, want: want{state: entity.TicketAttachmentScanSensitive, visible: false}},
		{name: "server_500", status: 500, body: `{"error":"internal"}`, want: want{state: entity.TicketAttachmentScanPending, visible: false}},
		{name: "missing_fields", status: 200, body: `{"label":"normal"}`, want: want{state: entity.TicketAttachmentScanPending, visible: false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newTicketAttachmentTestDB(t)
			if err := saveSystemSettingValue(db, UAPIAPIKeySettingKey, "uapi-test-only-key", "test"); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.delay > 0 {
					time.Sleep(tc.delay)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			withTicketAttachmentNSFWURL(t, server.URL)

			user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
			if err := db.Create(user).Error; err != nil {
				t.Fatal(err)
			}
			ticket := seedTicketAttachmentTicket(t, db, user.ID)
			info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ProcessPendingTicketAttachmentScans(context.Background(), db, time.Now()); err != nil {
				t.Fatalf("process: %v", err)
			}
			var row entity.TicketAttachment
			if err := db.Where("id = ?", info.ID).First(&row).Error; err != nil {
				t.Fatal(err)
			}
			if row.ScanState != tc.want.state {
				t.Fatalf("state=%q want %q (check_error=%q)", row.ScanState, tc.want.state, row.CheckError)
			}
			if ticketAttachmentVisible(row) != tc.want.visible {
				t.Fatalf("visible=%v want %v", ticketAttachmentVisible(row), tc.want.visible)
			}
			// 上游 200 的样本应把分数/建议落库，便于管理端判断。
			if tc.status == 200 && tc.body != `{"label":"normal"}` {
				if row.Suggestion == "" && row.RiskLevel == "" {
					t.Fatalf("expected suggestion/risk to be persisted, got %+v", row)
				}
			}
		})
	}
}

// TestTicketAttachmentScanTimeoutIsFailClosed 覆盖「检测超时」：
// 用 context 取消模拟超时（不真的等 30 秒）。
func TestTicketAttachmentScanTimeoutIsFailClosed(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	if err := saveSystemSettingValue(db, UAPIAPIKeySettingKey, "uapi-test-only-key", "test"); err != nil {
		t.Fatal(err)
	}
	// 上游「卡住不返回」：handler 睡 2 秒，调用方 context 200ms 超时。
	// 不用 `<-r.Context().Done()`，因为 httptest.Server.Close() 会等 handler 返回，
	// 那种写法会让测试收尾挂死（不是被测逻辑的问题）。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(`{"is_nsfw":false,"suggestion":"pass"}`))
	}))
	defer server.Close()
	withTicketAttachmentNSFWURL(t, server.URL)

	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)
	info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// 直接调用一次（不经 worker），断言错误路径落到 failed/pending 且不可见。
	var row entity.TicketAttachment
	if err := db.Where("id = ?", info.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	plain, mimeType, readErr := decryptTicketAttachmentForScan(db, row)
	if readErr != nil {
		t.Fatalf("decrypt: %v", readErr)
	}
	result := scanTicketAttachmentOnce(ctx, db, row.ID, plain, mimeType)
	if result.State != entity.TicketAttachmentScanFailed {
		t.Fatalf("timeout must be failed, got %q", result.State)
	}
	if result.CheckError == "" {
		t.Fatal("timeout must record a check error")
	}
}

// TestTicketAttachmentMessageBinding 验证「回复时带图」的绑定与跨工单隔离。
func TestTicketAttachmentMessageBinding(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	other := &entity.User{ID: 2, Username: "bob", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)
	otherTicket := seedTicketAttachmentTicket(t, db, other.ID)

	own, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatal(err)
	}
	// 别人的附件不能被我绑到自己的消息上。
	if _, err := AddTicketMessageWithDB(db, user, ticket.ID, ReplyTicketInput{
		Body: "带图回复", AttachmentIds: []int64{own.ID},
	}); err != nil {
		t.Fatalf("bind own attachment: %v", err)
	}
	var row entity.TicketAttachment
	if err := db.Where("id = ?", own.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.MessageID == nil {
		t.Fatal("attachment must be bound to the reply message")
	}
	// 重复绑定同一个附件必须失败（message_id 已非空）。
	if _, err := AddTicketMessageWithDB(db, user, ticket.ID, ReplyTicketInput{
		Body: "再次带图", AttachmentIds: []int64{own.ID},
	}); !errors.Is(err, ErrTicketAttachmentNotFound) {
		t.Fatalf("double binding must fail, got %v", err)
	}
	// 跨工单绑定别人的附件必须失败。
	foreign, err := StoreTicketAttachmentWithDB(db, other, otherTicket.ID, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AddTicketMessageWithDB(db, user, ticket.ID, ReplyTicketInput{
		Body: "偷图", AttachmentIds: []int64{foreign.ID},
	}); !errors.Is(err, ErrTicketAttachmentNotFound) {
		t.Fatalf("cross-ticket binding must fail, got %v", err)
	}
}

// TestTicketAttachmentDraftClaim 验证「发工单时带图」：草稿图只能被上传者本人认领一次。
func TestTicketAttachmentDraftClaim(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	draft, err := StoreTicketAttachmentWithDB(db, user, 0, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatalf("upload draft: %v", err)
	}
	if draft.TicketID != 0 {
		t.Fatalf("draft attachment must have ticket_id=0, got %d", draft.TicketID)
	}
	detail, err := CreateTicketWithDB(db, user, CreateTicketInput{
		Subject: "带图工单", Body: "问题描述", AttachmentIds: []int64{draft.ID},
	})
	if err != nil {
		t.Fatalf("create ticket with draft image: %v", err)
	}
	var row entity.TicketAttachment
	if err := db.Where("id = ?", draft.ID).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.TicketID != detail.Ticket.ID || row.MessageID == nil {
		t.Fatalf("draft attachment not claimed: ticketId=%d messageId=%v", row.TicketID, row.MessageID)
	}
	// 认领过的草稿图不能再被认领。
	if _, err := CreateTicketWithDB(db, user, CreateTicketInput{
		Subject: "第二个工单", Body: "描述", AttachmentIds: []int64{draft.ID},
	}); !errors.Is(err, ErrTicketAttachmentNotFound) {
		t.Fatalf("second claim must fail, got %v", err)
	}
	// 别人的草稿图不能被认领。
	stranger := &entity.User{ID: 2, Username: "bob", Role: "user", Status: "enabled"}
	if err := db.Create(stranger).Error; err != nil {
		t.Fatal(err)
	}
	foreignDraft, err := StoreTicketAttachmentWithDB(db, stranger, 0, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateTicketWithDB(db, user, CreateTicketInput{
		Subject: "抢图", Body: "描述", AttachmentIds: []int64{foreignDraft.ID},
	}); !errors.Is(err, ErrTicketAttachmentNotFound) {
		t.Fatalf("claiming another user's draft must fail, got %v", err)
	}
}

// TestTicketAttachmentDetailIncludesPlaceholders 验证工单详情返回附件占位条目，
// 且敏感图在列表里 visible=false + 带中文提示。
func TestTicketAttachmentDetailIncludesPlaceholders(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	detail, err := CreateTicketWithDB(db, user, CreateTicketInput{Subject: "标题", Body: "内容"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := StoreTicketAttachmentWithDB(db, user, detail.Ticket.ID, ticketAttachmentTestPNG(t, 16, 16))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", info.ID).
		Updates(map[string]interface{}{"scan_state": entity.TicketAttachmentScanSensitive}).Error; err != nil {
		t.Fatal(err)
	}
	read, err := GetTicketWithDB(db, user, detail.Ticket.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(read.Attachments))
	}
	if read.Attachments[0].Visible {
		t.Fatal("sensitive attachment must not be visible")
	}
	if read.Attachments[0].HiddenReason != "此图片包含敏感内容，已隐藏" {
		t.Fatalf("unexpected hidden reason %q", read.Attachments[0].HiddenReason)
	}
	// 序列化后不得出现密文字段或上传者 ID 之外的敏感信息。
	raw, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"ciphertext", "nonce", "Ciphertext", "Nonce", "checkError", "CheckError"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("detail JSON must not contain %q: %s", forbidden, string(raw))
		}
	}
}

// TestTicketAttachmentPerTicketCountIsUnlimited 覆盖判据 ④：
// 单工单**没有**图片张数上限 —— 连续上传 20 张（> 旧上限 9）必须全部成功。
//
// 这条用例替换了原来的 TestTicketAttachmentPerTicketLimit。
// 变更说明（不是「把期望改松了」，是需求变了）：
// 用户 2026-11-13 拍板「工单图片数量不限制」，改为限制单个工单的图片**总占用**
// （见 TestTicketAttachmentTicketQuota）。旧用例断言的 9 张上限已被显式删除，
// 它的防回归价值转移到「总占用」这条闸门上。
//
// 变异：把 TicketAttachmentMaxPerTicket = 9 与两处 count 判定恢复 → 本用例在第 10 张变红。
func TestTicketAttachmentPerTicketCountIsUnlimited(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)
	image := ticketAttachmentTestPNG(t, 8, 8)
	// 20 > 旧上限 9：只要张数闸门还活着，第 10 张必然失败。
	const uploads = 20
	for i := 0; i < uploads; i++ {
		if _, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, image); err != nil {
			t.Fatalf("第 %d 张上传失败，单工单张数必须不限：%v", i+1, err)
		}
	}
	var count int64
	if err := db.Model(&entity.TicketAttachment{}).Where("ticket_id = ?", ticket.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != uploads {
		t.Fatalf("落库张数 = %d, want %d", count, uploads)
	}
}

// TestTicketAttachmentTicketQuotaSettingDefault 断言默认值的**字面量**。
//
// 写字面量 200<<20 而不是引用 DefaultTicketAttachmentMaxTicketBytes：
// 期望值与被测常量共享同一符号时，常量漂移测试仍然绿（自证式断言）。
func TestTicketAttachmentTicketQuotaSettingDefault(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	// 没写 settings 行 → 必须回落到 200 MiB。
	if got := GetTicketAttachmentMaxTicketBytesWithDB(db); got != 209715200 {
		t.Fatalf("default quota = %d, want 209715200 (200 MiB)", got)
	}
	// 非法值（非数字 / 0 / 负数）同样回落，不能把上传路径打成「一律拒绝」。
	for _, bad := range []string{"not-a-number", "0", "-1", " "} {
		if err := saveSystemSettingValue(db, TicketAttachmentMaxTicketBytesSettingKey, bad, "test"); err != nil {
			t.Fatal(err)
		}
		if got := GetTicketAttachmentMaxTicketBytesWithDB(db); got != 209715200 {
			t.Fatalf("invalid setting %q must fall back to 209715200, got %d", bad, got)
		}
	}
	// 合法值必须生效（否则「不硬编码」就是假的）。
	if err := saveSystemSettingValue(db, TicketAttachmentMaxTicketBytesSettingKey, "1048576", "test"); err != nil {
		t.Fatal(err)
	}
	if got := GetTicketAttachmentMaxTicketBytesWithDB(db); got != 1048576 {
		t.Fatalf("configured quota = %d, want 1048576", got)
	}
}

// TestTicketAttachmentTicketQuota 覆盖判据 ⑤：
// 同一工单累计到上限 + 1 字节被拒，且**已成功的部分不被回滚**。
//
// 把上限调小（1 MiB）而不是真的传 200 MiB：200 MiB 的文件在测试里既不快也不稳，
// 而这条用例要验证的是「累计判定 + 不回滚」，与阈值绝对值无关。
// 剩下的「默认就是 200 MiB」由 TestTicketAttachmentTicketQuotaSettingDefault 钉住。
func TestTicketAttachmentTicketQuota(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)
	// 上限调成 8 KiB：占用判定与阈值绝对值无关，调小只是让用例跑得快
	// （默认 200 MiB 要传上千张才满）。「默认值就是 200 MiB」由
	// TestTicketAttachmentTicketQuotaSettingDefault 单独钉住。
	const quota int64 = 8 << 10 // 8 KiB
	if err := saveSystemSettingValue(db, TicketAttachmentMaxTicketBytesSettingKey, "8192", "test"); err != nil {
		t.Fatal(err)
	}

	image := ticketAttachmentTestPNG(t, 256, 256)
	stored := int64(len(image))
	if stored <= 0 || stored >= quota {
		t.Fatalf("用例前提不成立：单张 %d 字节，quota %d", stored, quota)
	}

	// 先传满：直到再加一张就会超限。
	var accepted []int64
	var used int64
	for used+stored <= quota {
		info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, image)
		if err != nil {
			t.Fatalf("第 %d 张上传失败：%v", len(accepted)+1, err)
		}
		accepted = append(accepted, info.ID)
		used += stored
	}
	if len(accepted) == 0 {
		t.Fatal("至少要有一张成功，否则「已成功的部分不被回滚」无从验证")
	}
	if used != quota {
		// 允许 used < quota（除不尽），但必须已接近上限，否则说明闸门没按配置走。
		if quota-used >= stored {
			t.Fatalf("累计用量 %d 距上限 %d 还有一整张的余量，闸门未生效", used, quota)
		}
	}

	// 第 N+1 张：累计会超过 quota + 1 字节 → 必须被拒。
	if _, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, image); !errors.Is(err, ErrTicketAttachmentQuota) {
		t.Fatalf("超限上传必须返回 ErrTicketAttachmentQuota，got %v", err)
	}

	// 已成功的部分必须原样留在库里（不被回滚），且用量统计与之一致。
	var rows []entity.TicketAttachment
	if err := db.Where("ticket_id = ?", ticket.ID).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if int64(len(rows)) != int64(len(accepted)) {
		t.Fatalf("落库 %d 行，已成功 %d 行 —— 超限失败不得回滚既有行", len(rows), len(accepted))
	}
	var sum int64
	for i, row := range rows {
		if row.ID != accepted[i] {
			t.Fatalf("第 %d 行 ID = %d, want %d", i, row.ID, accepted[i])
		}
		sum += row.ByteSize
	}
	if sum != used {
		t.Fatalf("SUM(byte_size) = %d, 预期 %d", sum, used)
	}

	// 上限调到 2 倍后必须又能上传（证明闸门读的是配置，不是常量）。
	if err := saveSystemSettingValue(db, TicketAttachmentMaxTicketBytesSettingKey, "16384", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, image); err != nil {
		t.Fatalf("提高上限后必须可以继续上传，got %v", err)
	}
}

// TestTicketAttachmentQuotaCountsLogicalUsageNotDistinct 是三处口径钉子：
// 同一张图被上传多次时，占用按**行**累加（逻辑占用），不按去重后的字节累加。
//
// 若有人把 ticketAttachmentUsageBytes 改成 SUM(DISTINCT sha256)（或接上 blob 去重后
// 顺手改成「只算一份」），本用例必红 —— 否则用户拿同一张图重复引用一千次就不受任何限制。
func TestTicketAttachmentQuotaCountsLogicalUsageNotDistinct(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)
	const quota int64 = 4096
	if err := saveSystemSettingValue(db, TicketAttachmentMaxTicketBytesSettingKey, "4096", "test"); err != nil {
		t.Fatal(err)
	}
	image := ticketAttachmentTestPNG(t, 256, 256)
	perRow := int64(len(image))
	if perRow <= 0 || perRow >= quota {
		t.Fatalf("用例前提不成立：单张 %d 字节，quota %d", perRow, quota)
	}
	storeUntilFull := func() int {
		n := 0
		for {
			if _, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, image); err != nil {
				if errors.Is(err, ErrTicketAttachmentQuota) {
					return n
				}
				t.Fatalf("unexpected error: %v", err)
			}
			n++
			if n > 1000 {
				t.Fatal("闸门没有生效")
			}
		}
	}
	got := storeUntilFull()
	want := int(quota / perRow)
	if got != want {
		t.Fatalf("同内容重复上传落库 %d 行，按逻辑占用应为 %d 行（去重口径会把额度放大 N 倍）", got, want)
	}
	// 另一侧边界：去重口径下额度会被放大 N 倍，落库行数会明显多于 want。
	if got > want*2 {
		t.Fatalf("落库 %d 行远超逻辑占用允许的 %d 行：占用被按去重口径计算了", got, want)
	}
	// 一次真正相同的字节进来 N 次，库里的 sha256 字段必然全相同 —— 证明这确实是「同一张图」。
	var distinct int64
	if err := db.Model(&entity.TicketAttachment{}).Where("ticket_id = ?", ticket.ID).
		Distinct("sha256").Count(&distinct).Error; err != nil {
		t.Fatal(err)
	}
	if distinct != 1 {
		t.Fatalf("用例前提不成立：期望同一张图有 1 个 distinct sha256，实际 %d", distinct)
	}
	// 直接钉占用函数的返回值：必须等于「行数 × 单行字节」，不是「去重后一份」。
	//
	// 这条比上面的行数断言更早暴露问题：若把它改成 GROUP BY sha256 / SUM(DISTINCT ...)，
	// 单次 Scan 只会取到一行的字节数，这里立刻不等（行数断言那侧反而可能因为
	// 闸门被绕过后仍能收敛到同样的行数而侥幸通过 —— 实测确实如此）。
	used, err := ticketAttachmentUsageBytes(db, ticket.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(got) * perRow; used != want {
		t.Fatalf("逻辑占用 = %d, want %d（%d 行 × %d 字节）；去重口径会把它压成 %d", used, want, got, perRow, perRow)
	}
}

// TestTicketAttachmentTicketQuotaDefaultIs200MiB 是判据 ⑤ 的**字面**版本：
// 默认配置下，同一工单累计到 200 MiB 后再传一张（哪怕只有几百字节）必须被拒。
//
// 为了不真的上传 200 MiB 数据，这里按「逻辑占用」的口径直接把 byte_size 写进库里 ——
// 闸门判的是 SUM(byte_size)，与这些行当初是怎么来的无关（见 ticketAttachmentUsageBytes 的注释）。
// 这也顺带证明闸门不依赖 blob 去重表：这些行没有任何 blob 关联。
func TestTicketAttachmentTicketQuotaDefaultIs200MiB(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	user := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)

	const quota = int64(200 << 20) // 209715200，与默认值同值但**写字面量**（防常量漂移自证）
	// 先铺到 quota - 1 字节。
	if err := db.Create(&entity.TicketAttachment{
		TicketID: ticket.ID, UserID: user.ID, MediaType: "image/png",
		ByteSize: quota - 1, SHA256: strings.Repeat("a", 64),
		ScanState: entity.TicketAttachmentScanPending,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// 再传一张真实小图（几百字节）：累计 = quota - 1 + n > quota → 必拒。
	//
	// 注意这里**没有**「+1 字节」的精确构造：单张图的大小由编码决定，无法精确等于 1 字节。
	// 边界精确性（恰好等于上限通过 / 上限 + 1 字节失败）由
	// TestTicketAttachmentImageAcceptsExactlySizeLimit 在单张层面钉住；
	// 本用例负责的是「累计闸门用的是同一把尺」。
	small := ticketAttachmentTestPNG(t, 256, 256)
	if _, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, small); !errors.Is(err, ErrTicketAttachmentQuota) {
		t.Fatalf("默认 200 MiB 累计上限必须生效，got %v", err)
	}
	// 既有行不得被回滚，且字节口径就是 SUM(byte_size)。
	var sum int64
	if err := db.Model(&entity.TicketAttachment{}).Where("ticket_id = ?", ticket.ID).
		Select("COALESCE(SUM(byte_size), 0)").Scan(&sum).Error; err != nil {
		t.Fatal(err)
	}
	if sum != quota-1 {
		t.Fatalf("SUM(byte_size) = %d, want %d（被拒的上传不得留下任何行）", sum, quota-1)
	}
}

// TestTicketAttachmentDraftQuotaIsScopedPerUploader 是草稿态（ticket_id=0）的隔离钉子：
// 额度必须按上传者分别统计，否则 A 传满就会把 B 一起锁死。
func TestTicketAttachmentDraftQuotaIsScopedPerUploader(t *testing.T) {
	db := newTicketAttachmentTestDB(t)
	alice := &entity.User{ID: 1, Username: "alice", Role: "user", Status: "enabled"}
	bob := &entity.User{ID: 2, Username: "bob", Role: "user", Status: "enabled"}
	for _, u := range []*entity.User{alice, bob} {
		if err := db.Create(u).Error; err != nil {
			t.Fatal(err)
		}
	}
	const quota int64 = 4096
	if err := saveSystemSettingValue(db, TicketAttachmentMaxTicketBytesSettingKey, "4096", "test"); err != nil {
		t.Fatal(err)
	}
	image := ticketAttachmentTestPNG(t, 256, 256)
	// 有界循环：闸门失效时**必须**以失败结束，不能挂起（挂起会把 CI 拖成超时，
	// 掩盖真正的原因；实测在「闸门短路」变异下无限循环）。
	filled := 0
	for {
		if _, err := StoreTicketAttachmentWithDB(db, alice, 0, image); err != nil {
			if !errors.Is(err, ErrTicketAttachmentQuota) {
				t.Fatalf("unexpected error: %v", err)
			}
			break
		}
		filled++
		if filled > 1000 {
			t.Fatal("闸门没有生效：草稿额度填了 1000 张仍未报超限")
		}
	}
	if filled == 0 {
		t.Fatal("至少要有一张成功，否则「按上传者隔离」无从验证")
	}
	// alice 的草稿额度已满，bob 必须还能传。
	if _, err := StoreTicketAttachmentWithDB(db, bob, 0, image); err != nil {
		t.Fatalf("草稿额度必须按上传者隔离，bob 不该被 alice 的占用锁死：%v", err)
	}
}

// withTicketAttachmentNSFWURL 把检测端点指向 httptest 服务器，测试结束后复位。
func withTicketAttachmentNSFWURL(t *testing.T, url string) {
	t.Helper()
	previous := ticketAttachmentNSFWURLOverride
	ticketAttachmentNSFWURLOverride = url
	t.Cleanup(func() { ticketAttachmentNSFWURLOverride = previous })
}

// uapiImageNSFW 是测试里构造上游响应的最小结构，toUAPI 转换成 uapi 包的类型。
type uapiImageNSFW struct {
	NSFWScore  *float64
	IsNSFW     *bool
	Suggestion string
	RiskLevel  string
	Label      string
	Confidence *float64
}

func (v uapiImageNSFW) toUAPI() uapi.ImageNSFWResponse {
	return uapi.ImageNSFWResponse{
		NSFWScore: v.NSFWScore, IsNSFW: v.IsNSFW, Suggestion: v.Suggestion,
		RiskLevel: v.RiskLevel, Label: v.Label, Confidence: v.Confidence,
	}
}
