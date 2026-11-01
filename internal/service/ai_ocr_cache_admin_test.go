package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestAIOCRCacheStatusAndClear(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	if err := saveSystemSettingValue(db, UAPIAPIKeySettingKey, "uapi-test-only", "test"); err != nil {
		t.Fatal(err)
	}
	updatedAt := time.Now().UTC().Truncate(time.Second)
	entries := []entity.AIOCRCache{
		{URLHash: "one", ImageSHA256: "image-one", OCRText: "first", UpdatedAt: updatedAt.Add(-time.Hour)},
		{URLHash: "two", ImageSHA256: "image-two", OCRText: "second", UpdatedAt: updatedAt},
	}
	if err := db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	status, err := GetAIOCRCacheStatusWithDB(db)
	if err != nil || !status.Configured || status.Entries != 2 || status.LatestUpdatedAt == nil || !status.LatestUpdatedAt.Equal(updatedAt) {
		t.Fatalf("unexpected OCR cache status: %+v err=%v", status, err)
	}
	deleted, err := ClearAIOCRCacheWithDB(db)
	if err != nil || deleted != 2 {
		t.Fatalf("clear OCR cache: deleted=%d err=%v", deleted, err)
	}
	status, err = GetAIOCRCacheStatusWithDB(db)
	if err != nil || !status.Configured || status.Entries != 0 || status.LatestUpdatedAt != nil {
		t.Fatalf("unexpected empty OCR cache status: %+v err=%v", status, err)
	}
	if key := GetUAPIAPIKeyWithDB(db); key != "uapi-test-only" {
		t.Fatal("clearing OCR cache must not change the configured key")
	}
}

func TestAIOCRCacheStatusWithoutConfiguredProvider(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "wrong-prefix", "uapi-"} {
		if err := saveSystemSettingValue(db, UAPIAPIKeySettingKey, key, "test"); err != nil {
			t.Fatal(err)
		}
		status, err := GetAIOCRCacheStatusWithDB(db)
		if err != nil || status.Configured || status.Entries != 0 {
			t.Fatalf("invalid provider key should report unavailable: %+v err=%v", status, err)
		}
	}
}

func TestAIOCRCacheAdminUnavailableDatabaseAndMissingTable(t *testing.T) {
	if _, err := GetAIOCRCacheStatusWithDB(nil); !errors.Is(err, ErrAIOCRCacheUnavailable) {
		t.Fatalf("nil status database should return ErrAIOCRCacheUnavailable, got %v", err)
	}
	if _, err := ClearAIOCRCacheWithDB(nil); !errors.Is(err, ErrAIOCRCacheUnavailable) {
		t.Fatalf("nil clear database should return ErrAIOCRCacheUnavailable, got %v", err)
	}

	db := newAIOCRCacheTestDB(t)
	if err := db.Migrator().DropTable(&entity.AIOCRCache{}); err != nil {
		t.Fatal(err)
	}
	status, err := GetAIOCRCacheStatusWithDB(db)
	if err != nil || status.Entries != 0 || status.LatestUpdatedAt != nil {
		t.Fatalf("missing OCR cache table should be treated as empty: %+v err=%v", status, err)
	}
	deleted, err := ClearAIOCRCacheWithDB(db)
	if err != nil || deleted != 0 {
		t.Fatalf("clearing a missing OCR cache table should be a no-op: deleted=%d err=%v", deleted, err)
	}
}

func TestAIOCRCacheAdminReturnsDatabaseErrors(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := GetAIOCRCacheStatusWithDB(db); err == nil {
		t.Fatal("closed database status should return an error")
	}
	if _, err := ClearAIOCRCacheWithDB(db); err == nil {
		t.Fatal("closed database clear should return an error")
	}
}

func TestClearAIOCRCacheSeparatesNewRequestsFromOldFlight(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	provider := cacheAIImageOCRProviderWithDB(db.WithContext(context.Background()), func(context.Context, string, string, string, []byte) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return "old result", nil
		}
		return "new result", nil
	})
	oldDone := make(chan error, 1)
	go func() {
		_, _, err := provider(context.Background(), "https://images.example/clear.png", "clear.png", "image/png", []byte("image"))
		oldDone <- err
	}()
	<-started
	if _, err := ClearAIOCRCacheWithDB(db.Session(&gorm.Session{NewDB: true})); err != nil {
		t.Fatal(err)
	}
	newDone := make(chan string, 1)
	go func() {
		text, _, _ := provider(context.Background(), "https://images.example/clear.png", "clear.png", "image/png", []byte("image"))
		newDone <- text
	}()
	select {
	case text := <-newDone:
		if text != "new result" {
			t.Fatalf("new request reused a pre-clear flight: %q", text)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("new request joined a pre-clear flight")
	}
	close(release)
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	var entry entity.AIOCRCache
	if err := db.First(&entry).Error; err != nil || entry.OCRText != "new result" {
		t.Fatalf("old flight repopulated the cleared cache: %+v err=%v", entry, err)
	}
}

func TestAIOCRCacheClearSerializesWithInFlightCacheWrite(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	db = db.Session(&gorm.Session{SkipDefaultTransaction: true})
	createStarted := make(chan struct{})
	releaseCreate := make(chan struct{})
	deleteStarted := make(chan struct{})
	var blockCreate atomic.Bool
	blockCreate.Store(true)
	if err := db.Callback().Create().Before("gorm:create").Register("test_ai_ocr_cache_block_create", func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != entity.TableNameAIOCRCache || !blockCreate.CompareAndSwap(true, false) {
			return
		}
		close(createStarted)
		<-releaseCreate
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Delete().Before("gorm:delete").Register("test_ai_ocr_cache_delete_started", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == entity.TableNameAIOCRCache {
			select {
			case <-deleteStarted:
			default:
				close(deleteStarted)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}

	provider := cacheAIImageOCRProviderWithDB(db, func(context.Context, string, string, string, []byte) (string, error) {
		return "in-flight result", nil
	})
	requestDone := make(chan error, 1)
	go func() {
		_, _, err := provider(context.Background(), "https://images.example/serialized.png", "serialized.png", "image/png", []byte("image"))
		requestDone <- err
	}()
	select {
	case <-createStarted:
	case <-time.After(time.Second):
		t.Fatal("OCR cache write did not reach the deterministic blocking point")
	}

	clearDone := make(chan error, 1)
	go func() {
		_, err := ClearAIOCRCacheWithDB(db.Session(&gorm.Session{NewDB: true}))
		clearDone <- err
	}()
	select {
	case <-deleteStarted:
		close(releaseCreate)
		t.Fatal("cache clear entered DELETE before the in-flight cache write completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseCreate)
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-clearDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cache clear did not finish after the in-flight write released")
	}

	var count int64
	if err := db.Model(&entity.AIOCRCache{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("cache clear must remove a write that completed before its DELETE: got %d entries", count)
	}
}
