package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		flightKey := fmt.Sprintf("%p:%s:%s", db, urlHash, imageHash)
		if text, cached, err, leader := joinAIOCRFlight(ctx, flightKey); !leader {
			return text, cached, err
		} else {
			text, cached, err := lookupOrRecognizeAIOCR(ctx, db, provider, urlHash, imageHash, sourceURL, imageName, contentType, image)
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

func hashAIOCRURL(sourceURL string) string {
	sum := sha256.Sum256([]byte(aiOCRCacheProfile + "\x00" + sourceURL))
	return hex.EncodeToString(sum[:])
}

func hashAIOCRImage(image []byte) string {
	sum := sha256.Sum256(image)
	return hex.EncodeToString(sum[:])
}
