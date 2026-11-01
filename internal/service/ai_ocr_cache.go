package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const aiOCRCacheProfile = "uapi-image-ocr-plain-v1"

var ErrAIOCRCacheUnavailable = errors.New("图片 OCR 缓存不可用")

type AIOCRCacheStatus struct {
	Configured      bool       `json:"configured"`
	Entries         int64      `json:"entries"`
	LatestUpdatedAt *time.Time `json:"latestUpdatedAt,omitempty"`
}

type aiOCRFlight struct {
	done   chan struct{}
	text   string
	err    error
	shared bool
}

var aiOCRFlights = struct {
	sync.Mutex
	items map[string]*aiOCRFlight
}{items: make(map[string]*aiOCRFlight)}

var aiOCRCacheEpochs = struct {
	sync.Mutex
	values map[string]uint64
}{values: make(map[string]uint64)}

var aiOCRCacheLocks = struct {
	sync.Mutex
	values map[string]*sync.Mutex
}{values: make(map[string]*sync.Mutex)}

func aiOCRCacheIdentity(db *gorm.DB) string {
	if db == nil {
		return ""
	}
	return fmt.Sprintf("%p", db.ConnPool)
}

func currentAIOCRCacheEpoch(db *gorm.DB) uint64 {
	identity := aiOCRCacheIdentity(db)
	aiOCRCacheEpochs.Lock()
	defer aiOCRCacheEpochs.Unlock()
	return aiOCRCacheEpochs.values[identity]
}

func bumpAIOCRCacheEpoch(db *gorm.DB) {
	identity := aiOCRCacheIdentity(db)
	aiOCRCacheEpochs.Lock()
	aiOCRCacheEpochs.values[identity]++
	aiOCRCacheEpochs.Unlock()
}

func aiOCRCacheLock(db *gorm.DB) *sync.Mutex {
	identity := aiOCRCacheIdentity(db)
	aiOCRCacheLocks.Lock()
	defer aiOCRCacheLocks.Unlock()
	lock := aiOCRCacheLocks.values[identity]
	if lock == nil {
		lock = &sync.Mutex{}
		aiOCRCacheLocks.values[identity] = lock
	}
	return lock
}

func cacheAIImageOCRProviderWithDB(db *gorm.DB, provider internalsearch.AIImageOCRProvider) internalsearch.AIImageOCRCacheProvider {
	if provider == nil {
		return nil
	}
	return func(ctx context.Context, sourceURL, imageName, contentType string, image []byte) (string, bool, error) {
		if db == nil || strings.TrimSpace(sourceURL) == "" || len(image) == 0 {
			text, err := provider(ctx, sourceURL, imageName, contentType, image)
			return text, false, err
		}

		urlHash := hashAIOCRURL(sourceURL)
		imageHash := hashAIOCRImage(image)
		epoch := currentAIOCRCacheEpoch(db)
		flightKey := fmt.Sprintf("%s:%s:%s:%d", aiOCRCacheIdentity(db), urlHash, imageHash, epoch)
		if text, cached, err, leader := joinAIOCRFlight(ctx, flightKey); !leader {
			return text, cached, err
		} else {
			text, cached, err := lookupOrRecognizeAIOCR(ctx, db, provider, urlHash, imageHash, sourceURL, imageName, contentType, image, epoch)
			finishAIOCRFlight(flightKey, text, cached, err)
			return text, cached, err
		}
	}
}

func joinAIOCRFlight(ctx context.Context, key string) (text string, cached bool, err error, leader bool) {
	aiOCRFlights.Lock()
	if existing := aiOCRFlights.items[key]; existing != nil {
		aiOCRFlights.Unlock()
		select {
		case <-existing.done:
			return existing.text, existing.shared, existing.err, false
		case <-ctx.Done():
			return "", false, ctx.Err(), false
		}
	}
	aiOCRFlights.items[key] = &aiOCRFlight{done: make(chan struct{})}
	aiOCRFlights.Unlock()
	return "", false, nil, true
}

func finishAIOCRFlight(key, text string, cached bool, err error) {
	aiOCRFlights.Lock()
	flight := aiOCRFlights.items[key]
	if flight != nil {
		flight.text = text
		flight.err = err
		flight.shared = err == nil && strings.TrimSpace(text) != ""
		close(flight.done)
		delete(aiOCRFlights.items, key)
	}
	aiOCRFlights.Unlock()
}

func lookupOrRecognizeAIOCR(
	ctx context.Context,
	db *gorm.DB,
	provider internalsearch.AIImageOCRProvider,
	urlHash, imageHash, sourceURL, imageName, contentType string,
	image []byte,
	epoch uint64,
) (string, bool, error) {
	var entry entity.AIOCRCache
	lookup := db.Where("url_hash = ? AND image_sha256 = ?", urlHash, imageHash).Find(&entry)
	if lookup.Error == nil && lookup.RowsAffected > 0 && strings.TrimSpace(entry.OCRText) != "" {
		return strings.TrimSpace(entry.OCRText), true, nil
	}
	if lookup.Error != nil {
		logger.SysLog(fmt.Sprintf("AI OCR cache lookup failed; running OCR without cache: %v", lookup.Error))
	}

	text, err := provider(ctx, sourceURL, imageName, contentType, image)
	if err != nil {
		return "", false, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false, nil
	}
	cacheLock := aiOCRCacheLock(db)
	cacheLock.Lock()
	defer cacheLock.Unlock()
	if currentAIOCRCacheEpoch(db) != epoch {
		return text, false, nil
	}

	entry = entity.AIOCRCache{
		URLHash:     urlHash,
		ImageSHA256: imageHash,
		ContentType: strings.TrimSpace(contentType),
		OCRText:     text,
		UpdatedAt:   time.Now(),
	}
	if err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "url_hash"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"image_sha256", "content_type", "ocr_text", "updated_at",
		}),
	}).Create(&entry).Error; err != nil {
		logger.SysLog(fmt.Sprintf("AI OCR cache write failed; result will not be cached: %v", err))
		return text, false, nil
	}
	return text, false, nil
}

func isAIOCRProviderConfigured(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	key := strings.TrimSpace(GetUAPIAPIKeyWithDB(db))
	return strings.HasPrefix(key, "uapi-") && len(key) > len("uapi-")
}

func GetAIOCRCacheStatusWithDB(db *gorm.DB) (AIOCRCacheStatus, error) {
	status := AIOCRCacheStatus{}
	if db == nil {
		return status, ErrAIOCRCacheUnavailable
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Exec("SELECT 1").Error; err != nil {
		return status, err
	}
	status.Configured = isAIOCRProviderConfigured(db)
	hasTable := db.Migrator().HasTable(&entity.AIOCRCache{})
	if !hasTable {
		return status, nil
	}
	if err := db.Model(&entity.AIOCRCache{}).Count(&status.Entries).Error; err != nil {
		return status, err
	}
	var latest entity.AIOCRCache
	if err := db.Order("updated_at DESC").First(&latest).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return status, err
		}
	} else {
		status.LatestUpdatedAt = &latest.UpdatedAt
	}
	return status, nil
}

func ClearAIOCRCacheWithDB(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, ErrAIOCRCacheUnavailable
	}
	cacheLock := aiOCRCacheLock(db)
	cacheLock.Lock()
	defer cacheLock.Unlock()
	if err := db.Session(&gorm.Session{NewDB: true}).Exec("SELECT 1").Error; err != nil {
		return 0, err
	}
	bumpAIOCRCacheEpoch(db)
	hasTable := db.Migrator().HasTable(&entity.AIOCRCache{})
	if !hasTable {
		return 0, nil
	}
	result := db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&entity.AIOCRCache{})
	return result.RowsAffected, result.Error
}

func hashAIOCRURL(sourceURL string) string {
	sum := sha256.Sum256([]byte(aiOCRCacheProfile + "\x00" + sourceURL))
	return hex.EncodeToString(sum[:])
}

func hashAIOCRImage(image []byte) string {
	sum := sha256.Sum256(image)
	return hex.EncodeToString(sum[:])
}
