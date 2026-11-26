package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestT12ImageManualMariaDBTransaction(t *testing.T) {
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
	_, err = ManualDetectSensitiveImageWithDB(ctx, db, plain, "image/png", now)
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
