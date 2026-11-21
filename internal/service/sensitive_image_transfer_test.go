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
	"github.com/xuri/excelize/v2"
)

func TestSensitiveImageMetadataBackupRoundTripWithoutInventingImage(t *testing.T) {
	for _, format := range []string{ExportFormatJSON, ExportFormatXlsx} {
		t.Run(format, func(t *testing.T) {
			db := newSensitiveImageTestDB(t)
			if err := db.AutoMigrate(&entity.TicketAttachmentBlob{}); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 11, 20, 3, 4, 5, 123000000, time.UTC)
			original := entity.SensitiveImageCacheEntry{ImageSHA256: strings.Repeat("a", 64), Status: "sensitive", Reason: "历史判定", ContentType: "image/png", FirstSeenAt: now, UpdatedAt: now}
			if err := db.Create(&original).Error; err != nil {
				t.Fatal(err)
			}
			output, err := ExportDataWithDB(db, ExportDataOptions{Resource: ImportResourceSensitiveImages, Format: format})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Where("image_sha256 = ?", original.ImageSHA256).Delete(&entity.SensitiveImageCacheEntry{}).Error; err != nil {
				t.Fatal(err)
			}
			options := ImportDataOptions{Resource: ImportResourceSensitiveImages, Filename: "backup." + format, Content: output.Content, DryRun: true}
			preview, err := ImportDataWithDB(db, options)
			if err != nil || preview.Succeeded != 1 || preview.Failed != 0 {
				t.Fatalf("metadata preview=%+v err=%v", preview, err)
			}
			var count int64
			db.Model(&entity.SensitiveImageCacheEntry{}).Count(&count)
			if count != 0 {
				t.Fatal("preview wrote metadata")
			}
			options.DryRun = false
			result, err := ImportDataWithDB(db, options)
			if err != nil || result.Succeeded != 1 || result.Failed != 0 {
				t.Fatalf("metadata restore=%+v err=%v", result, err)
			}
			var restored entity.SensitiveImageCacheEntry
			if err := db.Where("image_sha256 = ?", original.ImageSHA256).Take(&restored).Error; err != nil {
				t.Fatal(err)
			}
			if restored.Status != original.Status || restored.Reason != original.Reason || restored.ContentType != original.ContentType || !restored.UpdatedAt.Equal(now) {
				t.Fatalf("metadata changed=%+v", restored)
			}
			db.Model(&entity.TicketAttachmentBlob{}).Count(&count)
			if count != 0 {
				t.Fatal("metadata restore invented blob")
			}
			if _, _, err := LoadSensitiveImageBytesWithDB(db, original.ImageSHA256); err == nil {
				t.Fatal("missing image reported available")
			}
		})
	}
}

func TestSensitiveImageMetadataRestorePreservesNewerVerdictsBytesAndPendingScans(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	if err := db.AutoMigrate(&entity.TicketAttachmentBlob{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 3, 4, 5, 0, time.UTC)
	sha := strings.Repeat("b", 64)
	entry := entity.SensitiveImageCacheEntry{ImageSHA256: sha, Status: "sensitive", Reason: "较新真实判定", FileSize: 123, ContentType: "image/jpeg", HitCount: 7, FirstSeenAt: now.Add(-time.Hour), UpdatedAt: now}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	blob := entity.TicketAttachmentBlob{SHA256: sha, MediaType: "image/jpeg", ByteSize: 123, Ciphertext: []byte("original-ciphertext"), Nonce: []byte("original-nonce"), ScanState: "sensitive", RefCount: 2}
	if err := db.Create(&blob).Error; err != nil {
		t.Fatal(err)
	}
	content := func(hash, status string, at time.Time) []byte {
		data, _ := json.Marshal([]map[string]string{{"image_sha256": hash, "status": status, "reason": "备份判定", "content_type": "image/png", "image_base64": "", "updated_at": at.Format(time.RFC3339Nano)}})
		return data
	}
	for _, at := range []time.Time{now.Add(-time.Hour), now} {
		result, err := ImportDataWithDB(db, ImportDataOptions{Resource: ImportResourceSensitiveImages, Format: ImportFormatJSON, Content: content(sha, "clean", at)})
		if err != nil || result.Skipped != 1 || result.Failed != 0 {
			t.Fatalf("older/equal backup=%+v %v", result, err)
		}
	}
	var current entity.SensitiveImageCacheEntry
	db.Where("image_sha256 = ?", sha).Take(&current)
	if current.Status != entry.Status || current.Reason != entry.Reason || !current.UpdatedAt.Equal(now) {
		t.Fatalf("overwrote newer verdict=%+v", current)
	}
	result, err := ImportDataWithDB(db, ImportDataOptions{Resource: ImportResourceSensitiveImages, Format: ImportFormatJSON, Content: content(sha, "error", now.Add(time.Hour))})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("newer metadata=%+v %v", result, err)
	}
	db.Where("image_sha256 = ?", sha).Take(&current)
	if current.FileSize != 123 || current.ContentType != "image/jpeg" || current.HitCount != 7 || !current.FirstSeenAt.Equal(entry.FirstSeenAt) {
		t.Fatalf("lost physical metadata=%+v", current)
	}
	var physical entity.TicketAttachmentBlob
	db.Where("sha256 = ?", sha).Take(&physical)
	if !bytes.Equal(physical.Ciphertext, blob.Ciphertext) || !bytes.Equal(physical.Nonce, blob.Nonce) || physical.RefCount != blob.RefCount || physical.ScanState != blob.ScanState {
		t.Fatalf("modified real blob=%+v", physical)
	}
	for i, target := range []string{"cache", "attachment", "blob"} {
		hash := strings.Repeat(string(rune('c'+i)), 64)
		switch target {
		case "cache":
			db.Create(&entity.SensitiveImageCacheEntry{ImageSHA256: hash, Status: "pending", UpdatedAt: now})
		case "attachment":
			db.Create(&entity.TicketAttachment{SHA256: hash, ScanState: "scanning", UpdatedAt: now})
		case "blob":
			db.Create(&entity.TicketAttachmentBlob{SHA256: hash, ScanState: "pending"})
		}
		result, err := ImportDataWithDB(db, ImportDataOptions{Resource: ImportResourceSensitiveImages, Format: ImportFormatJSON, Content: content(hash, "clean", now.Add(time.Hour))})
		if err != nil || result.Skipped != 1 || result.Failed != 0 {
			t.Fatalf("pending %s=%+v %v", target, result, err)
		}
		if target == "cache" {
			var row entity.SensitiveImageCacheEntry
			db.Where("image_sha256 = ?", hash).Take(&row)
			if row.Status != "pending" {
				t.Fatal("metadata approved pending cache")
			}
		} else {
			var count int64
			db.Model(&entity.SensitiveImageCacheEntry{}).Where("image_sha256 = ?", hash).Count(&count)
			if count != 0 {
				t.Fatal("metadata bypassed pending detection")
			}
		}
	}
	legacyHash := strings.Repeat("f", 64)
	legacy := entity.TicketAttachment{SHA256: legacyHash, ScanState: entity.TicketAttachmentScanSensitive, ScanMarkedBy: "upstream", CreatedAt: now, UpdatedAt: now, CheckedAt: &now}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	result, err = ImportDataWithDB(db, ImportDataOptions{Resource: ImportResourceSensitiveImages, Format: ImportFormatJSON, Content: content(legacyHash, "clean", now.Add(-time.Hour))})
	if err != nil || result.Skipped != 1 {
		t.Fatalf("overwrote newer legacy history: %+v %v", result, err)
	}
	var legacyCount int64
	db.Model(&entity.SensitiveImageCacheEntry{}).Where("image_sha256 = ?", legacyHash).Count(&legacyCount)
	if legacyCount != 0 {
		t.Fatal("old backup shadowed newer legacy verdict")
	}
}

func TestSensitiveImageMetadataRestoreRejectsMissingIdentityOrUntrustedVerdict(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	valid := map[string]string{"image_sha256": strings.Repeat("a", 64), "status": "clean", "image_base64": "", "updated_at": "2026-11-20T03:04:05Z"}
	for _, bad := range []map[string]string{{"image_sha256": ""}, {"image_sha256": "../invalid"}, {"status": "pending"}, {"status": "sensitive_by_quota"}, {"updated_at": ""}, {"updated_at": "not-time"}, {"content_type": "text/html"}} {
		row := map[string]string{}
		for key, value := range valid {
			row[key] = value
		}
		for key, value := range bad {
			row[key] = value
		}
		content, _ := json.Marshal([]map[string]string{row})
		result, err := ImportDataWithDB(db, ImportDataOptions{Resource: ImportResourceSensitiveImages, Format: ImportFormatJSON, Content: content})
		if err != nil || result.Failed != 1 || result.Succeeded != 0 {
			t.Fatalf("invalid metadata %v = %+v %v", bad, result, err)
		}
	}
	var count int64
	db.Model(&entity.SensitiveImageCacheEntry{}).Count(&count)
	if count != 0 {
		t.Fatal("invalid metadata was stored")
	}
}

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
