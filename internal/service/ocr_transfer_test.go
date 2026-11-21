package service

import (
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestOCRCacheTransferRoundTripsRecomputesIdentityAndPreservesIDs(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	url := "https://images.example/a.png?key=private-value"
	input := []byte(`[{"url":"` + url + `","url_hash":"do-not-trust","image_sha256":"` + strings.Repeat("a", 64) + `","content_type":"image/png","ocr_text":"识别\n文字","updated_at":"2026-11-21T00:00:00Z"}]`)
	initialEpoch := currentAIOCRCacheEpoch(db)
	preview, err := ImportDataWithDB(db, ImportDataOptions{Resource: "ocr_cache", Format: "json", Content: input, DryRun: true})
	if err != nil || preview.Succeeded != 1 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	if currentAIOCRCacheEpoch(db) != initialEpoch {
		t.Fatal("preview changed cache epoch")
	}
	for _, format := range []string{"json", "csv"} {
		result, err := ImportDataWithDB(db, ImportDataOptions{Resource: "ocr_cache", Format: "json", Content: input})
		if err != nil || result.Succeeded != 1 {
			t.Fatalf("import=%+v err=%v", result, err)
		}
		var row entity.AIOCRCache
		if err := db.First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.ID == nil || row.URLHash != hashAIOCRURL(url) || row.OCRText != "识别\n文字" {
			t.Fatalf("row=%+v", row)
		}
		plain := false
		output, err := ExportDataWithDB(db, ExportDataOptions{Resource: "ocr_cache", Format: format, Masked: &plain})
		if err != nil || output.Rows != 1 {
			t.Fatalf("export=%+v err=%v", output, err)
		}
		if _, err := ClearAIOCRCacheWithDB(db); err != nil {
			t.Fatal(err)
		}
		result, err = ImportDataWithDB(db, ImportDataOptions{Resource: "ocr_cache", Format: format, Content: output.Content})
		if err != nil || result.Succeeded != 1 {
			t.Fatalf("roundtrip=%+v err=%v", result, err)
		}
		var restored entity.AIOCRCache
		if err := db.First(&restored).Error; err != nil {
			t.Fatal(err)
		}
		if restored.ID == nil || *restored.ID <= *row.ID || restored.URL != url || restored.ImageSHA256 != row.ImageSHA256 || restored.OCRText != row.OCRText || !restored.UpdatedAt.Equal(row.UpdatedAt) {
			t.Fatalf("restored=%+v old=%+v", restored, row)
		}
	}
	if currentAIOCRCacheEpoch(db) <= initialEpoch {
		t.Fatal("real import did not retire in-flight cache writes")
	}
	invalid := []byte(`[{"url":"file:///secret","image_sha256":"bad","ocr_text":"bad"}]`)
	result, err := ImportDataWithDB(db, ImportDataOptions{Resource: "ocr_cache", Format: "json", Content: invalid, DryRun: true})
	if err != nil || result.Failed != 1 {
		t.Fatalf("invalid=%+v err=%v", result, err)
	}
	legacyHash := strings.Repeat("c", 64)
	legacy := []byte(`[{"url":"","url_hash":"` + legacyHash + `","image_sha256":"` + strings.Repeat("a", 64) + `","ocr_text":"历史缓存"}]`)
	result, err = ImportDataWithDB(db, ImportDataOptions{Resource: "ocr_cache", Format: "json", Content: legacy})
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("legacy=%+v err=%v", result, err)
	}
	var restoredLegacy entity.AIOCRCache
	if err := db.Where("url_hash = ?", legacyHash).First(&restoredLegacy).Error; err != nil || restoredLegacy.URL != "" {
		t.Fatalf("historical identity lost: %+v %v", restoredLegacy, err)
	}
}
