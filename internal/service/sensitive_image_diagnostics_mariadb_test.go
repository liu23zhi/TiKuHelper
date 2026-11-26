package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func newT12MariaDB(t *testing.T) *gorm.DB {
	t.Helper()
	address := os.Getenv("TIKU_T12_MARIA_FIXTURE_ADDRESS")
	if address == "" {
		t.Skip("requires a disposable T12 fixture on loopback")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		t.Fatal("MariaDB integration fixture must be literal IPv4 loopback")
	}
	dsn := fmt.Sprintf("t12_fixture:t12_fixture_only@tcp(%s)/t12_image_fixture?parseTime=true&loc=UTC&timeout=2s", address)
	db, err := gorm.Open(mysql.New(mysql.Config{DSN: dsn, DisableWithReturning: true}), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return db
}

func TestT12ImageManualMariaDBTransaction(t *testing.T) {
	db := newT12MariaDB(t)
	for _, model := range []any{&entity.TicketAttachment{}, &entity.TicketAttachmentBlob{}, &entity.SensitiveImageCacheEntry{}} {
		if db.Migrator().HasTable(model) {
			t.Fatal("refusing to mutate a preexisting fixture schema; use a fresh disposable database")
		}
		if err := db.AutoMigrate(model); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	now := sensitiveImageTestNow()
	plain := sensitiveImageTestData("manual-maria-atomic")
	sha := HashSensitiveImage(plain)
	seedSensitiveImageCache(t, db, sha, entity.SensitiveImageCacheStatusSensitive, "prior verdict")
	for range 2 {
		if err := db.Create(&entity.TicketAttachment{SHA256: sha, MediaType: "image/png", ScanState: "sensitive", CheckError: "old check failure"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&entity.TicketAttachmentBlob{SHA256: sha, MediaType: "image/png", ScanState: "sensitive"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER reject_t12_blob_sync BEFORE UPDATE ON ticket_attachment_blobs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'SYNTHETIC_MARIA_PRIVATE_FAILURE'").Error; err != nil {
		t.Fatal(err)
	}
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{}, nil)
	_, err := ManualDetectSensitiveImageWithDB(ctx, db, plain, "image/png", now)
	var diag SensitiveImageDiagnostic
	if !errors.As(err, &diag) || diag.Code != imageStateSyncFailed || !localImageDiagnosticID.MatchString(diag.DiagnosticID) {
		t.Fatalf("expected typed transaction failure, got %v", err)
	}
	assertState := func(wantScan, wantCache, wantCheckError string) {
		t.Helper()
		entry, found, err := lookupSensitiveImageCacheWithDB(db, sha)
		if err != nil || !found || entry.Status != wantCache {
			t.Fatalf("cache=%s found=%v err=%v", entry.Status, found, err)
		}
		var rows []entity.TicketAttachment
		if err := db.Where("sha256 = ?", sha).Find(&rows).Error; err != nil || len(rows) != 2 {
			t.Fatalf("attachment count=%d err=%v", len(rows), err)
		}
		for _, row := range rows {
			if row.ScanState != wantScan || row.CheckError != wantCheckError {
				t.Fatalf("attachment=%s check_error=%q", row.ScanState, row.CheckError)
			}
		}
		var blob entity.TicketAttachmentBlob
		if err := db.First(&blob, "sha256 = ?", sha).Error; err != nil || blob.ScanState != wantScan {
			t.Fatalf("blob=%s err=%v", blob.ScanState, err)
		}
	}
	assertState("sensitive", "sensitive", "old check failure")
	if err := db.Exec("DROP TRIGGER reject_t12_blob_sync").Error; err != nil {
		t.Fatal(err)
	}
	result, err := ManualDetectSensitiveImageWithDB(ctx, db, plain, "image/png", now)
	if err != nil || result.AffectedAttachments != 2 || *calls != 2 {
		t.Fatalf("retry result=%+v calls=%d err=%v", result, *calls, err)
	}
	assertState("approved", "clean", "")
	t.Log("real MariaDB: blob failure rolls back cache and both attachments; next one-call attempt updates all three stores")
}

func TestT12ImageWorkerMariaDBSharedClaim(t *testing.T) {
	db := newT12MariaDB(t)
	for _, model := range []any{&entity.SystemSetting{}, &entity.User{}, &entity.TicketAttachment{}, &entity.TicketAttachmentBlob{}, &entity.SensitiveImageCacheEntry{}} {
		if db.Migrator().HasTable(model) {
			t.Fatal("refusing to mutate a preexisting fixture schema; use a fresh disposable database")
		}
		if err := db.AutoMigrate(model); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("ALTER TABLE ticket_attachments ADD COLUMN blob_sha256 char(64) NOT NULL DEFAULT ''").Error; err != nil {
		t.Fatal(err)
	}
	useTicketAttachmentTestKey(t)
	user := wiringNewUser(t, db, 1)
	if err := saveSystemSettingValue(db, UAPIAPIKeySettingKey, imageFixtureKey, "fixture"); err != nil {
		t.Fatal(err)
	}
	data := ticketAttachmentTestPNG(t, 14, 14)
	first, _ := wiringUpload(t, db, user, data)
	second, _ := wiringUpload(t, db, user, data)
	var calls atomic.Int32
	started := make(chan struct{})
	resume := make(chan struct{}, 1)
	defer func() {
		select {
		case resume <- struct{}{}:
		default:
		}
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			select {
			case <-resume:
			case <-time.After(5 * time.Second):
			}
		}
		fmt.Fprintf(w, "{\"is_nsfw\":%t}", n == 1)
	}))
	defer server.Close()
	withTicketAttachmentNSFWURL(t, server.URL)
	now := time.Now().Add(time.Second).Truncate(time.Millisecond)
	done := make(chan error, 1)
	go func() { _, err := ProcessPendingTicketAttachmentScans(context.Background(), db, now); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start a billable call")
	}
	if _, err := ProcessPendingTicketAttachmentScans(context.Background(), db.Session(&gorm.Session{NewDB: true}), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate billable calls before first committed: %d", calls.Load())
	}
	resume <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not finish")
	}
	a, b := t12ReviewRead(t, db, first), t12ReviewRead(t, db, second)
	blob := wiringBlobRow(t, db, a.SHA256)
	entry, ok, err := lookupSensitiveImageCacheWithDB(db, a.SHA256)
	if err != nil || !ok || entry.Status != "sensitive" || entry.Provider != "ticket-worker" ||
		a.ScanState != "sensitive" || b.ScanState != "sensitive" || blob.ScanState != "sensitive" || blob.RefCount != 2 {
		t.Fatalf("shared ruling mismatched: attachments=%s,%s cache=%s provider=%s blob=%s refs=%d found=%t err=%v",
			a.ScanState, b.ScanState, entry.Status, entry.Provider, blob.ScanState, blob.RefCount, ok, err)
	}
	t.Logf("real MariaDB: workers=2 calls=%d refs=%d attachments=%s,%s blob=%s cache=%s", calls.Load(), blob.RefCount, a.ScanState, b.ScanState, blob.ScanState, entry.Status)
}
