package registry

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestEnsureLocationCacheEntriesSchemaCopiesLegacyRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&entity.LegacyLocationCache{}, &entity.SystemSetting{}, &entity.LocationCache{}); err != nil {
		t.Fatal(err)
	}
	fetchedAt := time.Now().Add(-time.Hour).Round(0)
	updatedAt := time.Now().Add(-time.Minute).Round(0)
	legacy := entity.LegacyLocationCache{
		Kind: entity.LocationKindIP, LookupKey: "8.8.8.8", Address: "美国",
		RawJSON: `{"region":"美国","extra":{"source":"legacy"}}`, FetchedAt: fetchedAt, UpdatedAt: updatedAt,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	phone := entity.LegacyLocationCache{Kind: entity.LocationKindPhone, LookupKey: "13800138000", Address: "云南 移动", RawJSON: `{}`, FetchedAt: fetchedAt, UpdatedAt: updatedAt}
	if err := db.Create(&phone).Error; err != nil {
		t.Fatal(err)
	}
	current := entity.LocationCache{Kind: entity.LocationKindIP, LookupKey: legacy.LookupKey, Address: "已确认默认查询", RawJSON: `{}`, FetchedAt: fetchedAt, UpdatedAt: updatedAt}
	if err := db.Create(&current).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureLocationCacheEntriesSchema(db); err != nil {
		t.Fatal(err)
	}
	var copied entity.LocationCache
	if err := db.Where("kind = ? AND lookup_key = ? AND source = ?", entity.LocationKindIP, "8.8.8.8", "legacy").First(&copied).Error; err != nil {
		t.Fatal(err)
	}
	if copied.Address != legacy.Address || copied.RawJSON != legacy.RawJSON || !copied.FetchedAt.Equal(legacy.FetchedAt) || !copied.UpdatedAt.Equal(legacy.UpdatedAt) {
		t.Fatalf("legacy row was not copied exactly: legacy=%+v copied=%+v", legacy, copied)
	}
	var copiedPhone entity.LocationCache
	if err := db.Where("kind = ? AND lookup_key = ? AND source = ?", entity.LocationKindPhone, phone.LookupKey, "").First(&copiedPhone).Error; err != nil || copiedPhone.Address != phone.Address {
		t.Fatalf("phone cache should retain its only source: %+v err=%v", copiedPhone, err)
	}
	var retainedCurrent entity.LocationCache
	if err := db.Where("kind = ? AND lookup_key = ? AND source = ?", current.Kind, current.LookupKey, "").First(&retainedCurrent).Error; err != nil || retainedCurrent.Address != current.Address {
		t.Fatalf("migration overwrote an identified source: %+v err=%v", retainedCurrent, err)
	}
	if !systemSettingFlagIsSet(db, locationCacheEntriesMigrationKey) {
		t.Fatal("expected migration marker")
	}
	if err := ensureLocationCacheEntriesSchema(db); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&entity.LocationCache{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("migration should be idempotent, got %d rows", count)
	}
}
