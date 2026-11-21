package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestSensitiveImageImportStoresOnlySanitizedImage(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	if err := db.AutoMigrate(&entity.TicketAttachmentBlob{}); err != nil {
		t.Fatal(err)
	}
	buffer := new(bytes.Buffer)
	if err := png.Encode(buffer, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	safe := append([]byte(nil), buffer.Bytes()...)
	raw := append(buffer.Bytes(), []byte("private metadata must not survive import")...)
	rawSum := sha256.Sum256(raw)
	safeSum := sha256.Sum256(safe)
	content, _ := json.Marshal([]map[string]string{{"image_sha256": hex.EncodeToString(rawSum[:]), "status": "clean", "image_base64": base64.StdEncoding.EncodeToString(raw)}})
	result, err := ImportDataWithDB(db, ImportDataOptions{Resource: ImportResourceSensitiveImages, Filename: "images.json", Content: content})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("import=%+v %v", result, err)
	}
	wantSHA := hex.EncodeToString(safeSum[:])
	if result.Details[0].Key != wantSHA {
		t.Fatalf("stored source digest instead of normalized digest: %+v", result)
	}
	_, stored, err := ReadTicketAttachmentBlobWithDB(db, wantSHA)
	if err != nil || !bytes.Equal(stored, safe) {
		t.Fatalf("stored original payload: size=%d err=%v", len(stored), err)
	}
}

func TestSensitiveImageLegacyPendingAndQuotaDoNotHideVerdict(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	for _, row := range []entity.TicketAttachment{
		{ID: 1, SHA256: "history", ScanState: entity.TicketAttachmentScanSensitive, ScanMarkedBy: "upstream"},
		{ID: 2, SHA256: "history", ScanState: entity.TicketAttachmentScanPending},
		{ID: 3, SHA256: "history", ScanState: entity.TicketAttachmentScanSensitive, ScanMarkedBy: "quota"},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	var rows []SensitiveImageHistoryEntry
	if err := SensitiveImageHistoryQuery(db).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "sensitive" || rows[0].ImageSHA256 != "history" {
		t.Fatalf("actual verdict hidden: %+v", rows)
	}
}

func TestTicketImageWorkerSharedVerdictFailureRollsBackState(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	if err := db.AutoMigrate(&entity.TicketAttachmentBlob{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sha := strings.Repeat("b", 64)
	attachment := entity.TicketAttachment{SHA256: sha, ScanState: entity.TicketAttachmentScanScanning, ScanMarkedBy: "upstream", MediaType: "image/png", ByteSize: 5, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&attachment).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.TicketAttachmentBlob{SHA256: sha, ScanState: entity.TicketAttachmentScanPending}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER reject_shared_verdict BEFORE INSERT ON sensitive_image_cache_entries BEGIN SELECT RAISE(ABORT, 'injected shared verdict failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := persistTicketAttachmentScanResult(db, attachment, TicketAttachmentScanResult{State: entity.TicketAttachmentScanSensitive}, now); err == nil {
		t.Fatal("cache write failure ignored")
	}
	var got entity.TicketAttachment
	if err := db.First(&got, attachment.ID).Error; err != nil || got.ScanState != entity.TicketAttachmentScanScanning || got.AttemptCount != 0 {
		t.Fatalf("attachment partially updated: %+v %v", got, err)
	}
	var blob entity.TicketAttachmentBlob
	if err := db.First(&blob, "sha256 = ?", sha).Error; err != nil || blob.ScanState != entity.TicketAttachmentScanPending {
		t.Fatalf("blob partially updated: %+v %v", blob, err)
	}
}

func TestLogRetentionCanonicalSizeValidationAndAtomicUpdate(t *testing.T) {
	t.Run("canonical cross field validation", func(t *testing.T) {
		db := newLogRetentionTestDB(t)
		if err := db.Create(&entity.SystemSetting{Key: MaxLogSizeBytesSettingKey, Value: "5368709120"}).Error; err != nil {
			t.Fatal(err)
		}
		file := int64(3072)
		if _, err := UpdateLogRetentionSettingsWithDB(db, UpdateLogRetentionSettingsInput{MaxFileMiB: &file}); err != ErrLogRetentionTotalBelowTwoFiles {
			t.Fatalf("accepted 6 GiB of files under actual 5 GiB cap: %v", err)
		}
	})
	t.Run("canonical write failure rolls back all keys", func(t *testing.T) {
		db := newLogRetentionTestDB(t)
		if err := db.Exec("CREATE TRIGGER reject_canonical_log_cap BEFORE INSERT ON system_settings WHEN NEW.key = 'max_log_size_bytes' BEGIN SELECT RAISE(ABORT, 'injected write failure'); END").Error; err != nil {
			t.Fatal(err)
		}
		age, total := 42, int64(7)
		if _, err := UpdateLogRetentionSettingsWithDB(db, UpdateLogRetentionSettingsInput{MaxAgeDays: &age, MaxTotalGiB: &total}); err == nil {
			t.Fatal("injected failure was ignored")
		}
		var count int64
		if err := db.Model(&entity.SystemSetting{}).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("partially saved policy after failed canonical write: count=%d err=%v", count, err)
		}
	})
}

func TestLogRetentionConfigKeepsExactSharedByteLimit(t *testing.T) {
	const exact = int64(5<<30 + 123)
	if got := (LogRetentionConfig{MaxTotalBytes: exact}).Settings().Config().MaxTotalBytes; got != exact {
		t.Fatalf("byte cap rounded during config round trip: got %d want %d", got, exact)
	}
}

func TestTicketImageRetentionModesAndRememberedQrProtection(t *testing.T) {
	for _, mode := range []string{"time", "size", "both"} {
		t.Run(mode, func(t *testing.T) {
			db := newTicketAttachmentRetentionTestDB(t)
			if err := db.AutoMigrate(&entity.RefundPayoutQrMemory{}); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			ticket, _ := seedTicketAttachmentRetentionTicket(t, db, 1)
			remembered := seedRetentionAttachment(t, db, retentionAttachmentSeed{TicketID: ticket.ID, UserID: 1, ByteSize: 2 << 30, CreatedAt: now.Add(-300 * 24 * time.Hour)})
			if err := db.Create(&entity.RefundPayoutQrMemory{UserID: 1, AttachmentID: remembered.ID, Version: "remembered"}).Error; err != nil {
				t.Fatal(err)
			}
			old := seedRetentionAttachment(t, db, retentionAttachmentSeed{TicketID: ticket.ID, UserID: 1, ByteSize: 100, CreatedAt: now.Add(-200 * 24 * time.Hour)})
			recent := seedRetentionAttachment(t, db, retentionAttachmentSeed{TicketID: ticket.ID, UserID: 1, ByteSize: 100, CreatedAt: now.Add(-time.Hour)})
			if _, err := UpdateTicketImageRetentionSettingsWithDB(db, TicketAttachmentRetentionSettings{Mode: mode, MaxAgeDays: 30, MaxTotalGiB: 1, CleanupIntervalMinutes: 5}); err != nil {
				t.Fatal(err)
			}
			result, err := RunTicketAttachmentRetentionWithDB(db, now)
			if err != nil {
				t.Fatal(err)
			}
			if got := reloadRetentionAttachment(t, db, remembered.ID); got.PurgedAt != nil || len(got.Ciphertext) == 0 {
				t.Fatal("remembered payout QR was purged")
			}
			if reloadRetentionAttachment(t, db, old.ID).PurgedAt == nil {
				t.Fatal("expired or over-budget attachment remained")
			}
			if mode == "time" {
				if result.AgePurged != 1 || result.SizePurged != 0 || reloadRetentionAttachment(t, db, recent.ID).PurgedAt != nil {
					t.Fatalf("time-only policy ran size cleanup: %+v", result)
				}
			} else if mode == "size" && (result.AgePurged != 0 || result.SizePurged != 2) {
				t.Fatalf("size-only policy ran age cleanup: %+v", result)
			} else if mode == "both" && (result.AgePurged != 1 || result.SizePurged != 1) {
				t.Fatalf("combined policy did not apply both constraints: %+v", result)
			}
		})
	}
}

func TestSensitiveImageImportBudgetBeforeAnyRowWrite(t *testing.T) {
	// Reuse the encoded string so exercising the full-job budget doesn't itself
	// allocate 64 MiB of fixtures or require decoding untrusted image content.
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 100<<10)))
	rows := make([][]string, 700)
	for i := range rows {
		rows[i] = []string{encoded}
	}
	if err := validateSensitiveImageTransferBudget(map[string]int{"image_base64": 0}, rows); err == nil {
		t.Fatal("unbounded aggregate image input accepted")
	}
	if err := validateSensitiveImageTransferBudget(map[string]int{"image_base64": 0}, rows[:10]); err != nil {
		t.Fatalf("small batch rejected: %v", err)
	}
}
