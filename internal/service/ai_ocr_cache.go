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
	"unicode"

	"github.com/itihey/tikuAdapter/internal/entity"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const aiOCRCacheProfile = "uapi-image-ocr-plain-v1"

var (
	ErrAIOCRCacheUnavailable   = errors.New("图片 OCR 缓存不可用")
	ErrAIOCRCacheFilterInvalid = errors.New("图片 OCR 缓存筛选参数无效")
	errAIOCRFlightInterrupted  = errors.New("图片 OCR 识别意外中断，请重试")
)

type AIOCRCacheStatus struct {
	Configured      bool       `json:"configured"`
	Entries         int64      `json:"entries"`
	LatestUpdatedAt *time.Time `json:"latestUpdatedAt,omitempty"`
}

// AIOCRCacheListQuery contains the administrator-facing cache filters for the
// full source URL, hashes, content type, OCR text, and update time.
type AIOCRCacheListQuery struct {
	Keyword     string `json:"keyword"`
	ContentType string `json:"contentType"`
	UpdatedFrom string `json:"updatedFrom"`
	UpdatedTo   string `json:"updatedTo"`
}

// AIOCRCacheBatchDeleteRequest describes an explicit selection or all rows in
// the filtered scope, optionally excluding rows deselected after select-all.
type AIOCRCacheBatchDeleteRequest struct {
	AIOCRCacheListQuery
	Items  []string `json:"items"`
	All    bool     `json:"all"`
	Except []string `json:"except"`
}

type AIOCRCacheListSummary struct {
	Matched int64 `json:"matched"`
}

type AIOCRCacheListResult struct {
	Data       []entity.AIOCRCache   `json:"data"`
	Total      int64                 `json:"total"`
	Page       int                   `json:"page"`
	PageSize   int                   `json:"pageSize"`
	Configured bool                  `json:"configured"`
	Summary    AIOCRCacheListSummary `json:"summary"`
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
	return func(ctx context.Context, sourceURL, imageName, contentType string, image []byte) (text string, cached bool, err error) {
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
		}
		completed := false
		defer func() {
			// Keep a leader panic visible to the caller, while releasing followers
			// and allowing a later request to retry the same image.
			if !completed {
				text, cached, err = "", false, errAIOCRFlightInterrupted
			}
			finishAIOCRFlight(flightKey, text, cached, err)
		}()
		text, cached, err = lookupOrRecognizeAIOCR(ctx, db, provider, urlHash, imageHash, sourceURL, imageName, contentType, image, epoch)
		completed = true
		return text, cached, err
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
		if entry.URL == "" {
			backfillAIOCRCacheURL(db, urlHash, imageHash, sourceURL, epoch)
		}
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
		URL:         sourceURL,
		ImageSHA256: imageHash,
		ContentType: strings.TrimSpace(contentType),
		OCRText:     text,
		UpdatedAt:   time.Now(),
	}
	sequence := entity.AIOCRCacheSequence{}
	if err := db.Create(&sequence).Error; err != nil {
		logger.SysLog(fmt.Sprintf("AI OCR cache ID allocation failed; result will not be cached: %v", err))
		return text, false, nil
	}
	entry.ID = &sequence.ID
	if err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "url_hash"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"id", "url", "image_sha256", "content_type", "ocr_text", "updated_at",
		}),
	}).Create(&entry).Error; err != nil {
		logger.SysLog(fmt.Sprintf("AI OCR cache write failed; result will not be cached: %v", err))
		return text, false, nil
	}
	return text, false, nil
}

// Backfill only display metadata on a legacy hit. Never insert a missing row,
// change its OCR timestamp, or repopulate a cache cleared after this request began.
func backfillAIOCRCacheURL(db *gorm.DB, urlHash, imageHash, sourceURL string, epoch uint64) {
	cacheLock := aiOCRCacheLock(db)
	cacheLock.Lock()
	defer cacheLock.Unlock()
	if currentAIOCRCacheEpoch(db) != epoch {
		return
	}
	if err := db.Model(&entity.AIOCRCache{}).
		Where("url_hash = ? AND image_sha256 = ? AND COALESCE(url, '') = ''", urlHash, imageHash).
		UpdateColumn("url", sourceURL).Error; err != nil {
		logger.SysLog(fmt.Sprintf("AI OCR cache URL backfill failed; cached result remains usable: %v", err))
	}
}

// FindMatchingAIOCRCacheEntryWithDB confirms a completed OCR result by reading
// its exact persisted fingerprints. It never calls OCR or writes missing rows;
// a cache write failure or a concurrent clear must not be reported as saved.
func FindMatchingAIOCRCacheEntryWithDB(db *gorm.DB, sourceURL string, image []byte, text string) (*entity.AIOCRCache, error) {
	if db == nil {
		return nil, ErrAIOCRCacheUnavailable
	}
	text = strings.TrimSpace(text)
	if strings.TrimSpace(sourceURL) == "" || len(image) == 0 || text == "" {
		return nil, nil
	}
	var entry entity.AIOCRCache
	result := db.Session(&gorm.Session{NewDB: true}).Where("url_hash = ? AND image_sha256 = ?", hashAIOCRURL(sourceURL), hashAIOCRImage(image)).Limit(1).Find(&entry)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 || entry.URL != sourceURL || strings.TrimSpace(entry.OCRText) != text {
		return nil, nil
	}
	return &entry, nil
}

// IsAIOCRProviderConfiguredWithDB checks the effective stored UAPI key without
// constructing a client, contacting a provider, or exposing the key.
func IsAIOCRProviderConfiguredWithDB(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	key := strings.TrimSpace(GetUAPIAPIKeyWithDB(db))
	return strings.HasPrefix(key, "uapi-") && len(key) > len("uapi-") &&
		!strings.ContainsFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

func GetAIOCRCacheStatusWithDB(db *gorm.DB) (AIOCRCacheStatus, error) {
	status := AIOCRCacheStatus{}
	if db == nil {
		return status, ErrAIOCRCacheUnavailable
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Exec("SELECT 1").Error; err != nil {
		return status, err
	}
	status.Configured = IsAIOCRProviderConfiguredWithDB(db)
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

func parseAIOCRCacheDate(value string, endOfDay bool) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		parsed, err = time.ParseInLocation("2006-01-02", value, time.Local)
		if err == nil && endOfDay {
			parsed = parsed.AddDate(0, 0, 1)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w: 日期格式无效", ErrAIOCRCacheFilterInvalid)
	}
	return &parsed, nil
}

func parseAIOCRCacheFilterRange(filters AIOCRCacheListQuery) (from, to *time.Time, err error) {
	from, err = parseAIOCRCacheDate(filters.UpdatedFrom, false)
	if err != nil {
		return nil, nil, err
	}
	to, err = parseAIOCRCacheDate(filters.UpdatedTo, true)
	if err != nil {
		return nil, nil, err
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, nil, fmt.Errorf("%w: 开始时间必须早于结束时间", ErrAIOCRCacheFilterInvalid)
	}
	return from, to, nil
}

func validateAIOCRCacheFilters(filters AIOCRCacheListQuery) error {
	_, _, err := parseAIOCRCacheFilterRange(filters)
	return err
}

func applyAIOCRCacheFilters(query *gorm.DB, filters AIOCRCacheListQuery) (*gorm.DB, error) {
	if condition, args := util.BuildInstrCondition(filters.Keyword,
		"COALESCE(url, '')", "COALESCE(url_hash, '')", "COALESCE(image_sha256, '')", "COALESCE(content_type, '')", "COALESCE(ocr_text, '')"); condition != "" {
		query = query.Where(condition, args...)
	}
	if contentType := strings.TrimSpace(filters.ContentType); contentType != "" {
		query = query.Where("content_type = ?", contentType)
	}
	from, to, err := parseAIOCRCacheFilterRange(filters)
	if err != nil {
		return nil, err
	}
	if from != nil {
		query = query.Where("updated_at >= ?", *from)
	}
	if to != nil {
		query = query.Where("updated_at < ?", *to)
	}
	return query, nil
}

// ListAIOCRCacheWithDB returns a bounded, filterable administrator view of the
// persistent OCR cache, including its full source URL but never image bytes.
func ListAIOCRCacheWithDB(db *gorm.DB, filters AIOCRCacheListQuery, page, pageSize int) (AIOCRCacheListResult, error) {
	result := AIOCRCacheListResult{Data: make([]entity.AIOCRCache, 0)}
	if err := validateAIOCRCacheFilters(filters); err != nil {
		return result, err
	}
	if db == nil {
		return result, ErrAIOCRCacheUnavailable
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	result.Page, result.PageSize = page, pageSize
	base, err := applyAIOCRCacheFilters(db.Model(&entity.AIOCRCache{}), filters)
	if err != nil {
		return result, err
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Exec("SELECT 1").Error; err != nil {
		return result, err
	}
	result.Configured = IsAIOCRProviderConfiguredWithDB(db)
	if !db.Migrator().HasTable(&entity.AIOCRCache{}) {
		return result, nil
	}
	if err := base.Count(&result.Total).Error; err != nil {
		return result, err
	}
	result.Summary.Matched = result.Total
	if err := base.Order("id DESC, updated_at DESC, url_hash ASC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&result.Data).Error; err != nil {
		return result, err
	}
	return result, nil
}

func DeleteAIOCRCacheBatchWithDB(db *gorm.DB, request AIOCRCacheBatchDeleteRequest) (int64, error) {
	if request.All && len(request.Items) > 0 || !request.All && len(request.Items) == 0 || len(request.Items) > 1000 || len(request.Except) > 10000 {
		return 0, ErrAIOCRCacheFilterInvalid
	}
	items := uniqueAIOCRCacheHashes(request.Items)
	except := uniqueAIOCRCacheHashes(request.Except)
	if !request.All && len(items) == 0 || request.All && len(request.Except) > 0 && len(except) == 0 {
		return 0, ErrAIOCRCacheFilterInvalid
	}
	if err := validateAIOCRCacheFilters(request.AIOCRCacheListQuery); err != nil {
		return 0, err
	}
	if db == nil {
		return 0, ErrAIOCRCacheUnavailable
	}
	var query *gorm.DB
	var err error
	if request.All {
		query, err = applyAIOCRCacheFilters(db.Model(&entity.AIOCRCache{}), request.AIOCRCacheListQuery)
		if err != nil {
			return 0, err
		}
		if len(except) > 0 {
			query = query.Where("url_hash NOT IN ?", except)
		}
	} else {
		query = db.Model(&entity.AIOCRCache{}).Where("url_hash IN ?", items)
	}
	cacheLock := aiOCRCacheLock(db)
	cacheLock.Lock()
	defer cacheLock.Unlock()
	if err := db.Session(&gorm.Session{NewDB: true}).Exec("SELECT 1").Error; err != nil {
		return 0, err
	}
	hasTable := db.Migrator().HasTable(&entity.AIOCRCache{})
	if !hasTable {
		bumpAIOCRCacheEpoch(db)
		return 0, nil
	}
	result := query.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&entity.AIOCRCache{})
	if result.Error != nil {
		return 0, result.Error
	}
	bumpAIOCRCacheEpoch(db)
	return result.RowsAffected, nil
}

func uniqueAIOCRCacheHashes(hashes []string) []string {
	unique := make([]string, 0, len(hashes))
	seen := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		hash = strings.TrimSpace(hash)
		if len(hash) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(hash); err != nil {
			continue
		}
		if _, exists := seen[hash]; exists {
			continue
		}
		seen[hash] = struct{}{}
		unique = append(unique, hash)
	}
	return unique
}

func ClearAIOCRCacheWithDB(db *gorm.DB, queries ...AIOCRCacheListQuery) (int64, error) {
	var filters AIOCRCacheListQuery
	if len(queries) > 0 {
		filters = queries[0]
	}
	return DeleteAIOCRCacheBatchWithDB(db, AIOCRCacheBatchDeleteRequest{AIOCRCacheListQuery: filters, All: true})
}

func hashAIOCRURL(sourceURL string) string {
	sum := sha256.Sum256([]byte(aiOCRCacheProfile + "\x00" + sourceURL))
	return hex.EncodeToString(sum[:])
}

func hashAIOCRImage(image []byte) string {
	sum := sha256.Sum256(image)
	return hex.EncodeToString(sum[:])
}
