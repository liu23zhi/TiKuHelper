package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

const imagePrivateFixture = "SYNTHETIC_DO_NOT_PRINT_IMAGE_DIAGNOSTIC"
const imageFixtureKey = "uapi-synthetic-diagnostics-never-print"

func captureImageDiagnosticsLog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	before := *logger.LogDir
	*logger.LogDir = dir
	if err := logger.SetupGinLog(); err != nil {
		*logger.LogDir = before
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close(); *logger.LogDir = before })
	return dir
}

func TestT12ImageRealWorkerFailureAndRecovery(t *testing.T) {
	dir := captureImageDiagnosticsLog(t)
	db := newTicketAttachmentTestDB(t)
	for _, model := range []any{&entity.SensitiveImageCacheEntry{}, &entity.TicketAttachmentBlob{}} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveSystemSettingValue(db, UAPIAPIKeySettingKey, imageFixtureKey, "fixture"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file, _, err := r.FormFile("file")
		if err != nil || r.Method != http.MethodPost || r.FormValue("url") != "" || r.Header.Get("Authorization") != "Bearer "+imageFixtureKey {
			t.Error("expected one authenticated local multipart file, not a URL")
		}
		if file != nil {
			_ = file.Close()
		}
		w.Header().Set("X-Request-ID", imagePrivateFixture)
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			_, _ = fmt.Fprintf(w, `{"code":"RATE_LIMIT_EXCEEDED","message":"%s /private/path"}`, imagePrivateFixture)
			return
		}
		_, _ = fmt.Fprintf(w, `{"is_nsfw":false,"label":"%s","suggestion":"%s","risk_level":"%s","unexpected":"%s"}`,
			imagePrivateFixture, imagePrivateFixture, imagePrivateFixture, imagePrivateFixture)
	}))
	defer server.Close()
	withTicketAttachmentNSFWURL(t, server.URL)
	user := &entity.User{ID: 1, Username: "image-diagnostics-fixture", Role: "user", Status: "enabled"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	ticket := seedTicketAttachmentTicket(t, db, user.ID)
	info, err := StoreTicketAttachmentWithDB(db, user, ticket.ID, ticketAttachmentTestPNG(t, 12, 12))
	if err != nil {
		t.Fatal(err)
	}
	var original entity.TicketAttachment
	if err := db.First(&original, info.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.TicketAttachmentBlob{SHA256: original.SHA256, MediaType: "image/png", ByteSize: info.ByteSize, ScanState: entity.TicketAttachmentScanPending}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Second)
	if count, err := ProcessPendingTicketAttachmentScans(context.Background(), db, now); err != nil || count != 1 {
		t.Fatalf("first worker scan count=%d err=%v", count, err)
	}
	var attachment entity.TicketAttachment
	var cache entity.SensitiveImageCacheEntry
	var blob entity.TicketAttachmentBlob
	checkRows := func() {
		t.Helper()
		if err := db.First(&attachment, info.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Where("image_sha256 = ?", attachment.SHA256).First(&cache).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Where("sha256 = ?", attachment.SHA256).First(&blob).Error; err != nil {
			t.Fatal(err)
		}
	}
	checkRows()
	if attachment.ScanState != "pending" || blob.ScanState != "pending" || cache.Status != "error" || ticketAttachmentVisible(attachment) || attachment.AttemptCount != 1 {
		t.Errorf("first scan state: attachment=%s blob=%s cache=%s attempts=%d", attachment.ScanState, blob.ScanState, cache.Status, attachment.AttemptCount)
	}
	if !strings.Contains(attachment.CheckError, "SENSITIVE_IMAGE_UPSTREAM_RATE_LIMITED") ||
		!strings.Contains(cache.Reason, "SENSITIVE_IMAGE_UPSTREAM_RATE_LIMITED") ||
		strings.Contains(attachment.CheckError, imagePrivateFixture) || strings.Contains(cache.Reason, imagePrivateFixture) {
		t.Errorf("unsafe or unclassified stored errors: %q %q", attachment.CheckError, cache.Reason)
	}
	if count, err := ProcessPendingTicketAttachmentScans(context.Background(), db, now.Add(10*time.Second)); err != nil || count != 0 || calls.Load() != 1 {
		t.Errorf("backoff lost: count=%d calls=%d err=%v", count, calls.Load(), err)
	}
	if count, err := ProcessPendingTicketAttachmentScans(context.Background(), db, now.Add(2*time.Minute)); err != nil || count != 1 || calls.Load() != 2 {
		t.Errorf("retry lost: count=%d calls=%d err=%v", count, calls.Load(), err)
	}
	checkRows()
	if attachment.ScanState != "approved" || blob.ScanState != "approved" || cache.Status != "clean" || attachment.CheckError != "" || attachment.Label != "" || attachment.Suggestion != "" || attachment.RiskLevel != "" {
		t.Errorf("retry state or allowlist: attachment=%s blob=%s cache=%s error=%q label=%q suggestion=%q risk=%q",
			attachment.ScanState, blob.ScanState, cache.Status, attachment.CheckError, attachment.Label, attachment.Suggestion, attachment.RiskLevel)
	}
	data, err := os.ReadFile(filepath.Join(dir, "Tiku-sensitive-image.txt"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	for _, want := range []string{"channel=background", fmt.Sprintf("ticket_id=%d", ticket.ID), fmt.Sprintf("attachment_id=%d", info.ID),
		"image_sha256=" + attachment.SHA256, "mime=image/png", "duration_ms=", "http_status=429", "code=SENSITIVE_IMAGE_UPSTREAM_RATE_LIMITED", "upstream_ref=", "state=approved"} {
		if !strings.Contains(log, want) {
			t.Errorf("dedicated log omitted %q", want)
		}
	}
	for _, deny := range []string{imagePrivateFixture, strings.ToLower(imagePrivateFixture), imageFixtureKey, "/private/path", "nsfw_score=0x"} {
		if strings.Contains(log, deny) {
			t.Errorf("dedicated log contains denied data %q", deny)
		}
	}
	if !regexp.MustCompile(`diagnostic_id=IMG-[0-9a-f]{32}`).MatchString(log) {
		t.Error("missing server-generated diagnostic ID")
	}
}

func TestT12ImageManualAndUploadFailureClasses(t *testing.T) {
	dir := captureImageDiagnosticsLog(t)
	for _, tc := range []struct {
		name       string
		status     int
		body, kind string
	}{
		{"unauthorized", 401, `{"code":"faux"}`, "UPSTREAM_UNAUTHORIZED"},
		{"forbidden", 403, `{"message":"faux"}`, "UPSTREAM_FORBIDDEN"},
		{"rate", 429, `{"code":"RATE_LIMIT_EXCEEDED"}`, "UPSTREAM_RATE_LIMITED"},
		{"quota", 429, `{"code":"VISITOR_MONTHLY_QUOTA_EXHAUSTED"}`, "UPSTREAM_QUOTA_EXHAUSTED"},
		{"quota_typed_alias", 429, `{"code":"fixture:visitor_monthly_quota_exhausted"}`, "UPSTREAM_QUOTA_EXHAUSTED"},
		{"balance", 402, `{}`, "UPSTREAM_QUOTA_EXHAUSTED"},
		{"bad_parameters", 413, `{}`, "UPSTREAM_4XX"},
		{"upstream_timeout_status", 408, `{}`, "UPSTREAM_4XX"},
		{"server_error", 503, `{}`, "UPSTREAM_5XX"},
		{"json", 200, `{invalid`, "RESPONSE_JSON_INVALID"},
		{"empty", 200, `{"label":"faux"}`, "RESPONSE_VERDICT_MISSING"},
		{"unknown_verdict", 200, `{"suggestion":"not-supported","risk_level":"not-supported"}`, "RESPONSE_VERDICT_MISSING"},
		{"transport_network", 0, ``, "UPSTREAM_NETWORK"},
		{"transport_timeout", -1, ``, "UPSTREAM_TIMEOUT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newSensitiveImageTestDB(t)
			if err := saveSystemSettingValue(db, UAPIAPIKeySettingKey, imageFixtureKey, "fixture"); err != nil {
				t.Fatal(err)
			}
			var calls int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				if tc.status == -1 {
					_, _ = io.Copy(io.Discard, r.Body)
					select {
					case <-r.Context().Done():
					case <-time.After(2 * time.Second):
					}
					return
				}
				w.Header().Set("X-Request-ID", imagePrivateFixture)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			if tc.status == 0 {
				server.Close()
			}
			withTicketAttachmentNSFWURL(t, server.URL)
			data := sensitiveImageTestData("case-" + tc.name)
			ctx := context.Background()
			if tc.status == -1 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
				defer cancel()
			}
			_, err := ManualDetectSensitiveImageWithDB(ctx, db, data, "image/png", sensitiveImageTestNow())
			code := "SENSITIVE_IMAGE_" + tc.kind
			if err == nil || !strings.Contains(err.Error(), code) || strings.Contains(err.Error(), imagePrivateFixture) || strings.Contains(err.Error(), server.URL) {
				t.Errorf("manual expected safe %s receipt, got %v", code, err)
			}
			if tc.status <= 0 {
				return
			}
			user := &entity.User{ID: 92, Username: "t12-upload-fixture", Role: "user", Status: "enabled"}
			if err := db.Create(user).Error; err != nil {
				t.Fatal(err)
			}
			useTicketAttachmentTestKey(t)
			enabled := true
			if err := SetTicketImageModerationEnabledWithDB(db, &enabled); err != nil {
				t.Fatal(err)
			}
			_, userErr := StoreTicketAttachmentWithDB(db, user, 0, ticketAttachmentTestPNG(t, 12, 12))
			if !errors.Is(userErr, ErrTicketImageModerationUnavailable) || strings.Contains(userErr.Error(), "SENSITIVE_IMAGE_") || strings.Contains(userErr.Error(), imagePrivateFixture) {
				t.Errorf("upload must keep generic fail-closed error, got %v", userErr)
			}
			var count int64
			if db.Model(&entity.TicketAttachment{}).Count(&count).Error != nil || count != 0 || atomic.LoadInt32(&calls) != 2 {
				t.Errorf("each real path must make one call; upload fail-closed: rows=%d calls=%d", count, calls)
			}
		})
	}
	data, err := os.ReadFile(filepath.Join(dir, "Tiku-sensitive-image.txt"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !regexp.MustCompile(`channel=upload .*code=SENSITIVE_IMAGE_UPSTREAM_UNAUTHORIZED`).MatchString(log) ||
		!regexp.MustCompile(`channel=admin .*code=SENSITIVE_IMAGE_UPSTREAM_TIMEOUT`).MatchString(log) {
		t.Error("expected categorized channel receipts in the dedicated log")
	}
	if strings.Contains(log, imagePrivateFixture) || strings.Contains(log, imageFixtureKey) {
		t.Error("secret fixture leaked to the dedicated log")
	}
}

func TestT12ImageManualAtomicOnAttachmentSyncFailure(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	data := sensitiveImageTestData("manual-atomic")
	sha := HashSensitiveImage(data)
	seedSensitiveImageCache(t, db, sha, entity.SensitiveImageCacheStatusSensitive, "prior verdict")
	row := entity.TicketAttachment{SHA256: sha, MediaType: "image/png", ScanState: entity.TicketAttachmentScanSensitive}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER reject_manual_sync BEFORE UPDATE ON ticket_attachments BEGIN SELECT RAISE(ABORT, 'SYNTHETIC_PRIVATE_DATABASE_FAILURE'); END").Error; err != nil {
		t.Fatal(err)
	}
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{}, nil)
	_, err := ManualDetectSensitiveImageWithDB(context.Background(), db, data, "image/png", now)
	if err == nil || !strings.Contains(err.Error(), "SENSITIVE_IMAGE_STATE_SYNC_FAILED") || strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_DATABASE_FAILURE") {
		t.Errorf("manual sync should fail with an allowlisted error: %v", err)
	}
	entry, exists, readErr := lookupSensitiveImageCacheWithDB(db, sha)
	if readErr != nil || !exists || entry.Status != "sensitive" || *calls != 1 {
		t.Errorf("last verdict must remain until the cache and attachment can update together: %+v exists=%v calls=%d err=%v", entry, exists, *calls, readErr)
	}
}
