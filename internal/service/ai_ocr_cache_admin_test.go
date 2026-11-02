package service

import (
	"context"
	"errors"
	"fmt"
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

func TestListAIOCRCacheAppliesTextTypeDateFiltersAndPagination(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	entries := []entity.AIOCRCache{
		{URLHash: "url-one", ImageSHA256: "image-one", ContentType: "image/png", OCRText: "Alpha answer", UpdatedAt: base.Add(-2 * time.Hour)},
		{URLHash: "url-two", ImageSHA256: "image-two", ContentType: "image/jpeg", OCRText: "Beta answer", UpdatedAt: base.Add(-time.Hour)},
		{URLHash: "url-three", ImageSHA256: "image-three", ContentType: "image/png", OCRText: "Alpha second", UpdatedAt: base},
	}
	if err := db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	result, err := ListAIOCRCacheWithDB(db, AIOCRCacheListQuery{
		Keyword: "alpha", ContentType: "image/png",
		UpdatedFrom: base.Add(-3 * time.Hour).Format(time.RFC3339), UpdatedTo: base.Add(time.Minute).Format(time.RFC3339),
	}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Data) != 1 || result.Page != 1 || result.PageSize != 1 || result.Data[0].OCRText != "Alpha second" {
		t.Fatalf("unexpected filtered OCR cache result: %+v", result)
	}
	if result.Summary.Matched != 2 {
		t.Fatalf("unexpected filtered OCR cache summary: %+v", result.Summary)
	}
	deleted, err := ClearAIOCRCacheWithDB(db, AIOCRCacheListQuery{Keyword: "alpha"})
	if err != nil || deleted != 2 {
		t.Fatalf("filtered OCR cache clear: deleted=%d err=%v", deleted, err)
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

func TestClearAIOCRCacheEmptyFiltersClearAll(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	if err := db.Create(&entity.AIOCRCache{URLHash: "one", ImageSHA256: "image", OCRText: "answer"}).Error; err != nil {
		t.Fatal(err)
	}
	deleted, err := ClearAIOCRCacheWithDB(db, AIOCRCacheListQuery{Keyword: "  ", ContentType: "  "})
	if err != nil || deleted != 1 {
		t.Fatalf("empty filters should clear all entries: deleted=%d err=%v", deleted, err)
	}
}

func TestAIOCRCacheInvalidFiltersDoNotChangeEpoch(t *testing.T) {
	for _, filters := range []AIOCRCacheListQuery{
		{UpdatedFrom: "not-a-date"},
		{UpdatedTo: "2026-02-30"},
		{UpdatedFrom: "2026-11-03", UpdatedTo: "2026-11-01"},
	} {
		t.Run(filters.UpdatedFrom+filters.UpdatedTo, func(t *testing.T) {
			db := newAIOCRCacheTestDB(t)
			epoch := currentAIOCRCacheEpoch(db)
			if _, err := ClearAIOCRCacheWithDB(db, filters); !errors.Is(err, ErrAIOCRCacheFilterInvalid) {
				t.Fatalf("invalid cache filter must return a validation error: %v", err)
			}
			if got := currentAIOCRCacheEpoch(db); got != epoch {
				t.Fatalf("invalid filter changed cache epoch: before=%d after=%d", epoch, got)
			}
			if err := db.Migrator().DropTable(&entity.AIOCRCache{}); err != nil {
				t.Fatal(err)
			}
			if _, err := ListAIOCRCacheWithDB(db, filters, 1, 20); err == nil {
				t.Fatal("invalid list filter must fail even when the cache table is missing")
			}
			if _, err := ClearAIOCRCacheWithDB(db, filters); err == nil {
				t.Fatal("invalid clear filter must fail even when the cache table is missing")
			}
		})
	}
}

func TestAIOCRCacheInvalidFiltersAreValidatedBeforeDatabaseUse(t *testing.T) {
	filters := AIOCRCacheListQuery{UpdatedFrom: "not-a-date"}
	if _, err := ListAIOCRCacheWithDB(nil, filters, 1, 20); !errors.Is(err, ErrAIOCRCacheFilterInvalid) {
		t.Fatalf("invalid list filter should be reported before unavailable database: %v", err)
	}
	if _, err := ClearAIOCRCacheWithDB(nil, filters); !errors.Is(err, ErrAIOCRCacheFilterInvalid) {
		t.Fatalf("invalid clear filter should be reported before unavailable database: %v", err)
	}
}

func TestListAIOCRCacheLiteralSearchAndPaginationStayConsistent(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	updatedAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	entries := []entity.AIOCRCache{
		{URLHash: "one", ImageSHA256: "first", ContentType: "image/png", OCRText: "Alpha score_100%", UpdatedAt: updatedAt},
		{URLHash: "two", ImageSHA256: "second", ContentType: "image/png", OCRText: "Alpha score100", UpdatedAt: updatedAt},
		{URLHash: "three", ImageSHA256: "third", ContentType: "image/jpeg", OCRText: "Alpha score", UpdatedAt: updatedAt},
		{URLHash: "four", ImageSHA256: "fourth", ContentType: "image/png", OCRText: "Alpha next day", UpdatedAt: updatedAt.Add(12 * time.Hour)},
	}
	if err := db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	for _, keyword := range []string{"_", "%", "FIRST"} {
		result, err := ListAIOCRCacheWithDB(db, AIOCRCacheListQuery{Keyword: keyword}, 1, 20)
		if err != nil || result.Total != 1 || result.Summary.Matched != 1 || len(result.Data) != 1 || result.Data[0].URLHash != "one" {
			t.Fatalf("literal keyword %q: result=%+v err=%v", keyword, result, err)
		}
	}
	filters := AIOCRCacheListQuery{Keyword: "aLpHa", ContentType: "image/png", UpdatedFrom: "2026-11-01", UpdatedTo: "2026-11-01"}
	for page, want := range []string{"one", "two", ""} {
		result, err := ListAIOCRCacheWithDB(db, filters, page+1, 1)
		if err != nil || result.Total != 2 || result.Summary.Matched != 2 || result.Page != page+1 || result.PageSize != 1 {
			t.Fatalf("filtered page %d: result=%+v err=%v", page+1, result, err)
		}
		if want == "" {
			if result.Data == nil || len(result.Data) != 0 {
				t.Fatalf("out-of-range page must be an empty array: %+v", result)
			}
		} else if len(result.Data) != 1 || result.Data[0].URLHash != want {
			t.Fatalf("unstable page %d: %+v", page+1, result)
		}
	}
	deleted, err := ClearAIOCRCacheWithDB(db, filters)
	if err != nil || deleted != 2 {
		t.Fatalf("filtered delete must share the list scope: deleted=%d err=%v", deleted, err)
	}
}

func TestListAIOCRCacheMissingTableAndDatabaseErrors(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	if err := db.Migrator().DropTable(&entity.AIOCRCache{}); err != nil {
		t.Fatal(err)
	}
	result, err := ListAIOCRCacheWithDB(db, AIOCRCacheListQuery{}, 1, 20)
	if err != nil || result.Data == nil || len(result.Data) != 0 || result.Total != 0 || result.Page != 1 || result.PageSize != 20 {
		t.Fatalf("missing cache table must yield a bounded empty list: %+v err=%v", result, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ListAIOCRCacheWithDB(db, AIOCRCacheListQuery{}, 1, 20); err == nil {
		t.Fatal("closed list database must return an error")
	}
	if _, err := ListAIOCRCacheWithDB(nil, AIOCRCacheListQuery{}, 1, 20); !errors.Is(err, ErrAIOCRCacheUnavailable) {
		t.Fatalf("nil list database: %v", err)
	}
}

func TestAIOCRCacheProviderPanicReleasesFlight(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	provider := cacheAIImageOCRProviderWithDB(db, func(context.Context, string, string, string, []byte) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			panic("provider panic")
		}
		return "recovered OCR", nil
	})
	const sourceURL = "https://images.example/panic.png"
	image := []byte("image")
	key := fmt.Sprintf("%s:%s:%s:%d", aiOCRCacheIdentity(db), hashAIOCRURL(sourceURL), hashAIOCRImage(image), currentAIOCRCacheEpoch(db))
	t.Cleanup(func() { finishAIOCRFlight(key, "", false, errors.New("test cleanup")) })
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		_, _, _ = provider(context.Background(), sourceURL, "panic.png", "image/png", image)
	}()
	<-started
	aiOCRFlights.Lock()
	flight := aiOCRFlights.items[key]
	aiOCRFlights.Unlock()
	close(release)
	if value := <-panicked; value != "provider panic" {
		t.Fatalf("leader panic should propagate unchanged: %v", value)
	}
	select {
	case <-flight.done:
		if flight.err == nil || flight.shared || flight.text != "" {
			t.Fatalf("panic must release followers with an error: %+v", flight)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("provider panic left its OCR flight blocked")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	text, cached, err := provider(ctx, sourceURL, "panic.png", "image/png", image)
	if err != nil || cached || text != "recovered OCR" || calls.Load() != 2 {
		t.Fatalf("request after a panic must retry OCR: text=%q cached=%v calls=%d err=%v", text, cached, calls.Load(), err)
	}
}

func TestClearAIOCRCacheFailedDeleteDoesNotChangeEpoch(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	if err := db.Create(&entity.AIOCRCache{URLHash: "one", ImageSHA256: "image", OCRText: "answer"}).Error; err != nil {
		t.Fatal(err)
	}
	databaseErr := errors.New("cache delete rejected")
	if err := db.Callback().Delete().Before("gorm:delete").Register("reject_ocr_cache_delete", func(tx *gorm.DB) {
		tx.AddError(databaseErr)
	}); err != nil {
		t.Fatal(err)
	}
	epoch := currentAIOCRCacheEpoch(db)
	if deleted, err := ClearAIOCRCacheWithDB(db); !errors.Is(err, databaseErr) || deleted != 0 {
		t.Fatalf("failed clear must return the database error: deleted=%d err=%v", deleted, err)
	}
	if got := currentAIOCRCacheEpoch(db); got != epoch {
		t.Fatalf("failed clear changed cache epoch: before=%d after=%d", epoch, got)
	}
	var count int64
	if err := db.Model(&entity.AIOCRCache{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("failed clear changed cache contents: count=%d err=%v", count, err)
	}
}
