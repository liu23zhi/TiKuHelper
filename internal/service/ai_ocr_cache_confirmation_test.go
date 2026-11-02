package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestFindMatchingAIOCRCacheEntryRequiresPersistedFingerprintURLAndText(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	address := "https://images.example/题图.png?token=full%2Fvalue&x=1"
	image := []byte("downloaded image")
	stored := entity.AIOCRCache{
		URLHash: hashAIOCRURL(address), URL: address, ImageSHA256: hashAIOCRImage(image),
		ContentType: "image/png", OCRText: "完整识别结果", UpdatedAt: time.Now().Truncate(time.Second),
	}
	if err := db.Create(&stored).Error; err != nil {
		t.Fatal(err)
	}
	entry, err := FindMatchingAIOCRCacheEntryWithDB(db, address, image, "  完整识别结果\n")
	if err != nil || entry == nil || entry.URL != address || entry.URLHash != stored.URLHash || entry.ImageSHA256 != stored.ImageSHA256 || entry.OCRText != stored.OCRText || !entry.UpdatedAt.Equal(stored.UpdatedAt) {
		t.Fatalf("confirmed OCR cache: entry=%+v err=%v", entry, err)
	}
	for _, test := range []struct {
		name, url, text string
		image           []byte
	}{
		{"different-url", address + "&other=1", stored.OCRText, image},
		{"different-image", address, stored.OCRText, []byte("different image")},
		{"different-text", address, "后来识别出的不同文字", image},
		{"empty-url", "", stored.OCRText, image},
		{"empty-image", address, stored.OCRText, nil},
		{"empty-text", address, " \n", image},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, err := FindMatchingAIOCRCacheEntryWithDB(db, test.url, test.image, test.text)
			if err != nil || entry != nil {
				t.Fatalf("mismatched OCR result was confirmed: entry=%+v err=%v", entry, err)
			}
		})
	}
	if err := db.Model(&stored).UpdateColumn("url", "").Error; err != nil {
		t.Fatal(err)
	}
	if entry, err := FindMatchingAIOCRCacheEntryWithDB(db, address, image, stored.OCRText); err != nil || entry != nil {
		t.Fatalf("legacy metadata was confirmed or modified: entry=%+v err=%v", entry, err)
	}
	var remaining entity.AIOCRCache
	if err := db.First(&remaining).Error; err != nil || remaining.URL != "" || !remaining.UpdatedAt.Equal(stored.UpdatedAt) {
		t.Fatalf("confirmation must never write cache metadata: entry=%+v err=%v", remaining, err)
	}
}

func TestFindMatchingAIOCRCacheEntryReportsUnavailableDatabaseAndClearedRows(t *testing.T) {
	if _, err := FindMatchingAIOCRCacheEntryWithDB(nil, "https://images.example/image.png", []byte("image"), "text"); !errors.Is(err, ErrAIOCRCacheUnavailable) {
		t.Fatalf("nil database should be unavailable, got %v", err)
	}
	db := newAIOCRCacheTestDB(t)
	if entry, err := FindMatchingAIOCRCacheEntryWithDB(db, "https://images.example/image.png", []byte("image"), "text"); err != nil || entry != nil {
		t.Fatalf("missing row: entry=%+v err=%v", entry, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if entry, err := FindMatchingAIOCRCacheEntryWithDB(db, "https://images.example/image.png", []byte("image"), "text"); err == nil || entry != nil {
		t.Fatalf("failed database read must not confirm cache: entry=%+v err=%v", entry, err)
	}
}

func TestFindMatchingAIOCRCacheEntryCannotConfirmWriteFailureOrClearedFlight(t *testing.T) {
	for _, scenario := range []string{"write-failed", "cleared-during-ocr", "cleared-after-ocr"} {
		t.Run(scenario, func(t *testing.T) {
			db := newAIOCRCacheTestDB(t)
			address := "https://images.example/question.png?signature=original"
			image := []byte("downloaded image")
			if scenario == "write-failed" {
				if err := db.Callback().Create().Before("gorm:create").Register("fail_ocr_cache_write", func(tx *gorm.DB) {
					if tx.Statement.Table == entity.TableNameAIOCRCache {
						tx.AddError(errors.New("fixture cache write failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			provider := cacheAIImageOCRProviderWithDB(db, func(context.Context, string, string, string, []byte) (string, error) {
				calls++
				if scenario == "cleared-during-ocr" {
					if _, err := ClearAIOCRCacheWithDB(db); err != nil {
						t.Fatal(err)
					}
				}
				return "识别已完成", nil
			})
			text, cached, err := provider(context.Background(), address, "question.png", "image/png", image)
			if err != nil || text != "识别已完成" || cached || calls != 1 {
				t.Fatalf("cache confirmation must preserve OCR success: text=%q cached=%v calls=%d err=%v", text, cached, calls, err)
			}
			if scenario == "cleared-after-ocr" {
				if _, err := ClearAIOCRCacheWithDB(db); err != nil {
					t.Fatal(err)
				}
			}
			if entry, err := FindMatchingAIOCRCacheEntryWithDB(db, address, image, text); err != nil || entry != nil {
				t.Fatalf("failed or cleared cache was confirmed: entry=%+v err=%v", entry, err)
			}
			var count int64
			if err := db.Model(&entity.AIOCRCache{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("confirmation revived a missing cache row: count=%d err=%v", count, err)
			}
			if calls != 1 {
				t.Fatalf("confirmation repeated the OCR call: calls=%d", calls)
			}
		})
	}
}
