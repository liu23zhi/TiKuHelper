package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func assertAIOCRCacheURL(t *testing.T, db *gorm.DB, hash, want string) {
	t.Helper()
	var row entity.AIOCRCache
	if err := db.Where("url_hash = ?", hash).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["url"] != want {
		t.Fatalf("stored/listed OCR URL was lost or changed: got=%v want %d bytes", fields["url"], len(want))
	}
}

func TestAIOCRCachePersistsFullURLWithoutChangingHashIdentity(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	address := "https://images.example/题图.png?signature=A%2FB%3D&ids=1&ids=2&token=" + strings.Repeat("abc", 1500)
	calls := 0
	provider := cacheAIImageOCRProviderWithDB(db, func(context.Context, string, string, string, []byte) (string, error) {
		calls++
		return "识别内容", nil
	})
	for _, image := range []string{"first-image", "changed-image"} {
		if _, cached, err := provider(context.Background(), address, "题图.png", "image/png", []byte(image)); err != nil || cached {
			t.Fatalf("fresh image: cached=%v err=%v", cached, err)
		}
		assertAIOCRCacheURL(t, db, hashAIOCRURL(address), address)
	}
	// The saved URL is display metadata: only the hashes decide cache reuse.
	if err := db.Model(&entity.AIOCRCache{}).Where("url_hash = ?", hashAIOCRURL(address)).UpdateColumn("url", "https://display.example/metadata").Error; err != nil {
		t.Fatal(err)
	}
	text, cached, err := provider(context.Background(), address, "题图.png", "image/png", []byte("changed-image"))
	if err != nil || !cached || text != "识别内容" || calls != 2 {
		t.Fatalf("raw URL unexpectedly changed cache identity: cached=%v text=%q calls=%d err=%v", cached, text, calls, err)
	}
	var count int64
	if err := db.Model(&entity.AIOCRCache{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("same URL should still have one row: count=%d err=%v", count, err)
	}
}

func TestAIOCRCacheLegacyHitBackfillsURLWithoutOCRCallOrTimestampChange(t *testing.T) {
	for _, emptyURL := range []any{"", nil} {
		t.Run(fmt.Sprintf("url-%v", emptyURL), func(t *testing.T) {
			db := newAIOCRCacheTestDB(t)
			address := "https://images.example/legacy.png?token=full%2Fvalue&x=1"
			updatedAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			entry := entity.AIOCRCache{URLHash: hashAIOCRURL(address), ImageSHA256: hashAIOCRImage([]byte("image")), ContentType: "image/png", OCRText: "历史识别", UpdatedAt: updatedAt}
			if err := db.Create(&entry).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&entry).UpdateColumn("url", emptyURL).Error; err != nil {
				t.Fatal(err)
			}
			provider := cacheAIImageOCRProviderWithDB(db, func(context.Context, string, string, string, []byte) (string, error) {
				t.Fatal("legacy cache hit must not call the OCR provider")
				return "", nil
			})
			if text, cached, err := provider(context.Background(), address, "legacy.png", "image/png", []byte("image")); err != nil || !cached || text != "历史识别" {
				t.Fatalf("legacy cache reuse: text=%q cached=%v err=%v", text, cached, err)
			}
			assertAIOCRCacheURL(t, db, entry.URLHash, address)
			var stored entity.AIOCRCache
			if err := db.First(&stored).Error; err != nil || !stored.UpdatedAt.Equal(updatedAt) {
				t.Fatalf("URL metadata backfill changed the OCR timestamp: %+v err=%v", stored, err)
			}
		})
	}
}

func TestAIOCRCacheURLKeywordSharesListSummaryAndClearScope(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	provider := cacheAIImageOCRProviderWithDB(db, func(context.Context, string, string, string, []byte) (string, error) { return "相同文本", nil })
	for _, address := range []string{"https://images.example/Exam_100%25.png?a=1&b=2", "https://images.example/ExamX100other.png"} {
		if _, _, err := provider(context.Background(), address, "image.png", "image/png", []byte("image")); err != nil {
			t.Fatal(err)
		}
	}
	filters := AIOCRCacheListQuery{Keyword: "exam_100%25"}
	result, err := ListAIOCRCacheWithDB(db, filters, 1, 1)
	if err != nil || result.Total != 1 || result.Summary.Matched != 1 || len(result.Data) != 1 {
		t.Fatalf("URL literal keyword should select exactly one row: %+v err=%v", result, err)
	}
	deleted, err := ClearAIOCRCacheWithDB(db, filters)
	if err != nil || deleted != 1 {
		t.Fatalf("URL clear must use the list scope: deleted=%d err=%v", deleted, err)
	}
	remaining, err := ListAIOCRCacheWithDB(db, AIOCRCacheListQuery{}, 1, 20)
	if err != nil || remaining.Total != 1 || len(remaining.Data) != 1 {
		t.Fatalf("clear removed an unrelated URL: %+v err=%v", remaining, err)
	}
}

func TestAIOCRCacheLegacyURLBackfillCannotWriteAfterClear(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement=%t", replacement), func(t *testing.T) {
			db := newAIOCRCacheTestDB(t)
			address := "https://images.example/clear-legacy.png?x=1&y=2"
			entry := entity.AIOCRCache{URLHash: hashAIOCRURL(address), ImageSHA256: hashAIOCRImage([]byte("image")), OCRText: "旧文本"}
			if err := db.Create(&entry).Error; err != nil {
				t.Fatal(err)
			}
			lookupReady, resume := make(chan struct{}), make(chan struct{})
			var blocked atomic.Bool
			var release sync.Once
			unblock := func() { release.Do(func() { close(resume) }) }
			defer unblock()
			if err := db.Callback().Query().After("gorm:query").Register("test_block_legacy_ocr_hit", func(tx *gorm.DB) {
				if _, ok := tx.Statement.Dest.(*entity.AIOCRCache); ok && blocked.CompareAndSwap(false, true) {
					close(lookupReady)
					<-resume
				}
			}); err != nil {
				t.Fatal(err)
			}
			provider := cacheAIImageOCRProviderWithDB(db, func(context.Context, string, string, string, []byte) (string, error) {
				t.Error("legacy hit should not call OCR")
				return "", nil
			})
			done := make(chan error, 1)
			go func() {
				_, _, err := provider(context.Background(), address, "image.png", "image/png", []byte("image"))
				done <- err
			}()
			select {
			case <-lookupReady:
			case <-time.After(2 * time.Second):
				t.Fatal("legacy lookup did not reach blocking point")
			}
			if deleted, err := ClearAIOCRCacheWithDB(db); err != nil || deleted != 1 {
				t.Fatalf("clear: deleted=%d err=%v", deleted, err)
			}
			if replacement {
				entry.OCRText = "新文本"
				if err := db.Create(&entry).Error; err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("legacy lookup did not finish")
			}
			var rows []entity.AIOCRCache
			if err := db.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if replacement {
				if len(rows) != 1 || rows[0].URL != "" || rows[0].OCRText != "新文本" {
					t.Fatalf("old request modified the new cache generation: %+v", rows)
				}
			} else if len(rows) != 0 {
				t.Fatalf("old request revived cleared cache: %+v", rows)
			}
		})
	}
}
