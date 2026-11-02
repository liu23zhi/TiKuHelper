package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"gorm.io/gorm"
)

func newAIOCRCacheTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:ai-ocr-cache-%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open OCR cache test DB: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get OCR cache test SQL DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&entity.AIOCRCache{}); err != nil {
		t.Fatalf("migrate OCR cache: %v", err)
	}
	return db
}

func TestAIOCRCacheReusesOnlySameURLAndImage(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	var calls atomic.Int32
	base := internalsearch.AIImageOCRProvider(func(_ context.Context, _, _, _ string, image []byte) (string, error) {
		call := calls.Add(1)
		return fmt.Sprintf("识别结构-%d-%s", call, image), nil
	})
	provider := cacheAIImageOCRProviderWithDB(db, base)
	ctx := context.Background()
	const signedURL = "https://images.example/question.png?token=private-token"

	first, firstCached, err := provider(ctx, signedURL, "question.png", "image/png", []byte("image-a"))
	if err != nil || firstCached {
		t.Fatalf("first OCR result should be uncached: cached=%v err=%v", firstCached, err)
	}
	second, secondCached, err := provider(ctx, signedURL, "question.png", "image/png", []byte("image-a"))
	if err != nil || !secondCached || second != first {
		t.Fatalf("same URL and image should reuse the first result: text=%q cached=%v err=%v", second, secondCached, err)
	}
	providerAfterRebuild := cacheAIImageOCRProviderWithDB(db, base)
	persisted, persistedCached, err := providerAfterRebuild(ctx, signedURL, "question.png", "image/png", []byte("image-a"))
	if err != nil || !persistedCached || persisted != first {
		t.Fatalf("rebuilding the provider should still reuse the persisted result: text=%q cached=%v err=%v", persisted, persistedCached, err)
	}
	changed, changedCached, err := provider(ctx, signedURL, "question.png", "image/png", []byte("image-b"))
	if err != nil || changedCached || changed == first {
		t.Fatalf("changed image at same URL must run OCR again: text=%q cached=%v err=%v", changed, changedCached, err)
	}
	otherURL, otherURLCached, err := provider(ctx, "https://images.example/other.png", "question.png", "image/png", []byte("image-b"))
	if err != nil || otherURLCached || otherURL == changed {
		t.Fatalf("different URL must not reuse an OCR result: text=%q cached=%v err=%v", otherURL, otherURLCached, err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected OCR to run for first, changed image, and different URL only; got %d calls", got)
	}

	var count int64
	if err := db.Model(&entity.AIOCRCache{}).Count(&count).Error; err != nil {
		t.Fatalf("count OCR cache rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected one current row per URL, got %d", count)
	}
	var stored entity.AIOCRCache
	if err := db.Where("url_hash = ?", hashAIOCRURL(signedURL)).First(&stored).Error; err != nil {
		t.Fatalf("find OCR cache entry: %v", err)
	}
	if stored.ImageSHA256 != hashAIOCRImage([]byte("image-b")) || stored.OCRText != changed {
		t.Fatalf("same-URL cache entry was not refreshed: %+v", stored)
	}
	if stored.URLHash == signedURL || stored.OCRText == "" {
		t.Fatalf("OCR cache did not keep the URL hash key or OCR result: %+v", stored)
	}
}

func TestAIOCRCacheDoesNotStoreFailedOrEmptyResults(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	var calls atomic.Int32
	provider := cacheAIImageOCRProviderWithDB(db, func(_ context.Context, _, _, _ string, _ []byte) (string, error) {
		if calls.Add(1) == 1 {
			return "", errors.New("temporary OCR failure")
		}
		return "  ", nil
	})
	for range 2 {
		_, cached, err := provider(context.Background(), "https://images.example/empty.png", "empty.png", "image/png", []byte("image"))
		if cached {
			t.Fatal("failed or empty OCR result must not be reported as cached")
		}
		if calls.Load() == 1 && err == nil || calls.Load() == 2 && err != nil {
			t.Fatalf("unexpected OCR result on attempt %d: %v", calls.Load(), err)
		}
	}
	var count int64
	if err := db.Model(&entity.AIOCRCache{}).Count(&count).Error; err != nil {
		t.Fatalf("count OCR cache rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("failed and empty OCR results must not be cached, got %d rows", count)
	}
}

func TestAIOCRCachePreservesTheFullOCRStructure(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	fullText := strings.Repeat("识", 12001)
	provider := cacheAIImageOCRProviderWithDB(db, func(_ context.Context, _, _, _ string, _ []byte) (string, error) {
		return fullText, nil
	})

	first, firstCached, err := provider(context.Background(), "https://images.example/long.png", "long.png", "image/png", []byte("image"))
	if err != nil || firstCached || first != fullText {
		t.Fatalf("first OCR result was changed: cached=%v chars=%d err=%v", firstCached, len([]rune(first)), err)
	}
	secondProvider := cacheAIImageOCRProviderWithDB(db, func(context.Context, string, string, string, []byte) (string, error) {
		t.Fatal("cached result should not invoke the OCR provider")
		return "", nil
	})
	second, secondCached, err := secondProvider(context.Background(), "https://images.example/long.png", "long.png", "image/png", []byte("image"))
	if err != nil || !secondCached || second != fullText {
		t.Fatalf("cache must preserve the complete OCR structure: cached=%v chars=%d err=%v", secondCached, len([]rune(second)), err)
	}
}

func TestAIOCRCacheCoalescesConcurrentRequestsWithoutDoubleBilling(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	provider := cacheAIImageOCRProviderWithDB(db, func(_ context.Context, _, _, _ string, _ []byte) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "并发识别结构", nil
	})

	const requestCount = 8
	type result struct {
		cached bool
		err    error
		text   string
	}
	results := make(chan result, requestCount)
	var ready sync.WaitGroup
	ready.Add(requestCount)
	start := make(chan struct{})
	for range requestCount {
		go func() {
			ready.Done()
			<-start
			text, cached, err := provider(context.Background(), "https://images.example/concurrent.png", "concurrent.png", "image/png", []byte("same image"))
			results <- result{text: text, cached: cached, err: err}
		}()
	}
	ready.Wait()
	close(start)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("OCR provider was not called")
	}
	time.Sleep(25 * time.Millisecond)
	close(release)

	cachedCount := 0
	for range requestCount {
		item := <-results
		if item.err != nil || item.text != "并发识别结构" {
			t.Fatalf("concurrent OCR result mismatch: %+v", item)
		}
		if item.cached {
			cachedCount++
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("same concurrent request should call OCR once, got %d calls", got)
	}
	if cachedCount != requestCount-1 {
		t.Fatalf("only the OCR leader should be chargeable, got %d cached responses", cachedCount)
	}
}

func TestAIOCRCacheDoesNotMarkConcurrentEmptyResultAsCached(t *testing.T) {
	db := newAIOCRCacheTestDB(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	provider := cacheAIImageOCRProviderWithDB(db, func(_ context.Context, _, _, _ string, _ []byte) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "", nil
	})

	results := make(chan bool, 2)
	go func() {
		_, cached, _ := provider(context.Background(), "https://images.example/empty-concurrent.png", "empty.png", "image/png", []byte("same image"))
		results <- cached
	}()
	<-started
	go func() {
		_, cached, _ := provider(context.Background(), "https://images.example/empty-concurrent.png", "empty.png", "image/png", []byte("same image"))
		results <- cached
	}()
	time.Sleep(25 * time.Millisecond)
	close(release)
	if <-results || <-results {
		t.Fatal("empty concurrent OCR result must not be marked as cached")
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one OCR call for concurrent empty result, got %d", calls.Load())
	}
}
