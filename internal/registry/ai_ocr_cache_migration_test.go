package registry

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type legacyAIOCRCacheWithoutID struct {
	URLHash     string    `gorm:"column:url_hash;type:char(64);primaryKey"`
	URL         string    `gorm:"column:url;type:text"`
	ImageSHA256 string    `gorm:"column:image_sha256;type:char(64);not null"`
	ContentType string    `gorm:"column:content_type;type:varchar(128);not null;default:''"`
	OCRText     string    `gorm:"column:ocr_text;type:text;not null"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (*legacyAIOCRCacheWithoutID) TableName() string { return entity.TableNameAIOCRCache }

func TestEnsureAIOCRCacheIDsPreservesRowsAndOrdersNewestFirst(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&legacyAIOCRCacheWithoutID{}, &entity.AIOCRCacheSequence{}, &entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	legacyRows := []legacyAIOCRCacheWithoutID{
		{URLHash: "older", ImageSHA256: "image-older", OCRText: "旧缓存", UpdatedAt: base},
		{URLHash: "newer", ImageSHA256: "image-newer", OCRText: "新缓存", UpdatedAt: base.Add(time.Hour)},
	}
	if err := db.Create(&legacyRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.AIOCRCache{}); err != nil {
		t.Fatal(err)
	}
	if err := ensureAIOCRCacheIDs(db); err != nil {
		t.Fatal(err)
	}
	var rows []entity.AIOCRCache
	if err := db.Order("id DESC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].URLHash != "newer" || rows[0].ID == nil || rows[1].URLHash != "older" || rows[1].ID == nil || *rows[0].ID <= *rows[1].ID {
		t.Fatalf("legacy records should retain data and receive IDs in chronological order: %+v", rows)
	}
	if rows[0].OCRText != "新缓存" || rows[1].OCRText != "旧缓存" {
		t.Fatalf("migration changed OCR data: %+v", rows)
	}
	var sequenceCount int64
	if err := db.Model(&entity.AIOCRCacheSequence{}).Count(&sequenceCount).Error; err != nil || sequenceCount != 2 {
		t.Fatalf("expected two allocated IDs, count=%d err=%v", sequenceCount, err)
	}
	if !systemSettingFlagIsSet(db, aiOCRCacheIDsMigrationKey) {
		t.Fatal("expected migration marker")
	}
	if err := ensureAIOCRCacheIDs(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entity.AIOCRCacheSequence{}).Count(&sequenceCount).Error; err != nil || sequenceCount != 2 {
		t.Fatalf("rerunning migration must not allocate IDs again, count=%d err=%v", sequenceCount, err)
	}
}
