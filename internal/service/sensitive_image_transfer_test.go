package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/xuri/excelize/v2"
)

func TestSensitiveImageTransferRoundTripPicturesAndValidation(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	if err := db.AutoMigrate(&entity.TicketAttachmentBlob{}); err != nil {
		t.Fatal(err)
	}
	buffer := new(bytes.Buffer)
	if err := png.Encode(buffer, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	picture := buffer.Bytes()
	sum := sha256.Sum256(picture)
	sha := hex.EncodeToString(sum[:])
	content, _ := json.Marshal([]map[string]string{{"image_sha256": sha, "status": "sensitive", "image_base64": base64.StdEncoding.EncodeToString(picture)}})
	opts := ImportDataOptions{Resource: ImportResourceSensitiveImages, Filename: "images.json", Content: content, DryRun: true}
	result, err := ImportDataWithDB(db, opts)
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("preview=%+v %v", result, err)
	}
	var count int64
	db.Model(&entity.SensitiveImageCacheEntry{}).Count(&count)
	if count != 0 {
		t.Fatal("preview wrote cache")
	}
	opts.DryRun = false
	result, err = ImportDataWithDB(db, opts)
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("import=%+v %v", result, err)
	}
	for _, format := range []string{ExportFormatJSON, ExportFormatXlsx} {
		output, err := ExportDataWithDB(db, ExportDataOptions{Resource: ImportResourceSensitiveImages, Format: format})
		if err != nil {
			t.Fatal(err)
		}
		if format == ExportFormatJSON {
			if !bytes.Contains(output.Content, []byte(base64.StdEncoding.EncodeToString(picture))) {
				t.Fatal("JSON missing image bytes")
			}
		} else {
			file, e := excelize.OpenReader(bytes.NewReader(output.Content))
			if e != nil {
				t.Fatal(e)
			}
			pictures, e := file.GetPictures(file.GetSheetName(0), "E2")
			if e != nil || len(pictures) != 1 || !bytes.Equal(pictures[0].File, picture) {
				t.Fatalf("xlsx missing image: %d %v", len(pictures), e)
			}
			file.Close()
		}
		result, err = ImportDataWithDB(db, ImportDataOptions{Resource: ImportResourceSensitiveImages, Filename: "images." + format, Content: output.Content})
		if err != nil || result.Succeeded != 1 {
			t.Fatalf("%s round trip=%+v %v", format, result, err)
		}
	}
	invalid := bytes.Replace(content, []byte(sha), []byte("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"), 1)
	result, err = ImportDataWithDB(db, ImportDataOptions{Resource: ImportResourceSensitiveImages, Filename: "wrong.json", Content: invalid})
	if err != nil || result.Failed != 1 {
		t.Fatalf("digest mismatch accepted: %+v %v", result, err)
	}
}

func TestSensitiveImageLegacyHistoryDedupesAndExcludesQuota(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := time.Now()
	for _, row := range []entity.TicketAttachment{
		{ID: 1, SHA256: "legacy", ScanState: entity.TicketAttachmentScanSensitive, ScanMarkedBy: "upstream", CreatedAt: now, UpdatedAt: now},
		{ID: 2, SHA256: "legacy", ScanState: entity.TicketAttachmentScanFailed, ScanMarkedBy: "upstream", CreatedAt: now, UpdatedAt: now},
		{ID: 3, SHA256: "quota", ScanState: entity.TicketAttachmentScanSensitive, ScanMarkedBy: "quota", CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	var rows []SensitiveImageHistoryEntry
	if err := SensitiveImageHistoryQuery(db).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ImageSHA256 != "legacy" || rows[0].Status != "error" {
		t.Fatalf("history=%+v", rows)
	}
	entry := entity.SensitiveImageCacheEntry{ImageSHA256: "legacy", Status: "clean", FirstSeenAt: now, UpdatedAt: now}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	rows = nil
	if err := SensitiveImageHistoryQuery(db).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "clean" {
		t.Fatalf("shared verdict not authoritative: %+v", rows)
	}
}

func TestTicketImageRetentionPolicyModes(t *testing.T) {
	db := newLogRetentionTestDB(t)
	for _, mode := range []string{"time", "size", "both"} {
		settings, err := UpdateTicketImageRetentionSettingsWithDB(db, TicketAttachmentRetentionSettings{Mode: mode, MaxAgeDays: 30, MaxTotalGiB: 2, CleanupIntervalMinutes: 5})
		if err != nil {
			t.Fatal(err)
		}
		config := settings.Config()
		if (config.MaxAgeDays > 0) != (mode != "size") || (config.MaxTotalBytes > 0) != (mode != "time") {
			t.Fatalf("%s wrong config %+v", mode, config)
		}
		if got := resolveTicketImageCacheRetention(db); got.Mode != mode {
			t.Fatalf("cache summary mode=%s", got.Mode)
		}
	}
	if _, err := UpdateTicketImageRetentionSettingsWithDB(db, TicketAttachmentRetentionSettings{Mode: "wrong", MaxAgeDays: 30, MaxTotalGiB: 2, CleanupIntervalMinutes: 5}); err == nil {
		t.Fatal("invalid policy accepted")
	}
}

func TestTicketImageWorkerWritesSharedVerdictAndBlobState(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	if err := db.AutoMigrate(&entity.TicketAttachmentBlob{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	sha := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	attachment := entity.TicketAttachment{SHA256: sha, ScanState: entity.TicketAttachmentScanScanning, ScanMarkedBy: "upstream", MediaType: "image/png", ByteSize: 5, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&attachment).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.TicketAttachmentBlob{SHA256: sha, ScanState: entity.TicketAttachmentScanPending, MediaType: "image/png", ByteSize: 5}).Error; err != nil {
		t.Fatal(err)
	}
	if err := persistTicketAttachmentScanResult(db, attachment, TicketAttachmentScanResult{State: entity.TicketAttachmentScanSensitive}, now); err != nil {
		t.Fatal(err)
	}
	var entry entity.SensitiveImageCacheEntry
	if err := db.First(&entry, "image_sha256 = ?", sha).Error; err != nil || entry.Status != "sensitive" {
		t.Fatalf("cache=%+v %v", entry, err)
	}
	var blob entity.TicketAttachmentBlob
	if err := db.First(&blob, "sha256 = ?", sha).Error; err != nil || blob.ScanState != "sensitive" {
		t.Fatalf("blob=%+v %v", blob, err)
	}
	if _, err := SyncSensitiveImageScanStateWithDB(db, sha, "error"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&blob, "sha256 = ?", sha).Error; err != nil || blob.ScanState != "failed" {
		t.Fatalf("failed state=%+v %v", blob, err)
	}
}

func TestLogRetentionUsesSystemSettingsSizeWithoutOverwritingIt(t *testing.T) {
	db := newLogRetentionTestDB(t)
	const expected int64 = 5 << 30
	if err := db.Create(&entity.SystemSetting{Key: MaxLogSizeBytesSettingKey, Value: "5368709120"}).Error; err != nil {
		t.Fatal(err)
	}
	if cfg := ResolveLogRetentionConfigWithDB(db); cfg.MaxTotalBytes != expected {
		t.Fatalf("background size overrides UI size: %+v", cfg)
	}
	age := 17
	settings, err := UpdateLogRetentionSettingsWithDB(db, UpdateLogRetentionSettingsInput{MaxAgeDays: &age})
	if err != nil || settings.Config().MaxTotalBytes != expected {
		t.Fatalf("partial update changed limit: %+v %v", settings, err)
	}
}
