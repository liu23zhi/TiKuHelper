package service

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ExportResourceOCRCache = "ocr_cache"
const ImportResourceOCRCache = ExportResourceOCRCache

func ocrTransferHeaders() []string {
	return []string{"url", "url_hash", "image_sha256", "content_type", "ocr_text", "updated_at"}
}

func validOCRTransferURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == "" && len(value) <= 64<<10
}

func maskOCRTransferURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return parsed.String()
}

func init() {
	exportResourceSpecs[ExportResourceOCRCache] = exportResourceSpec{
		resource: ExportResourceOCRCache,
		columns:  []ExportColumn{exportColumn("id"), exportColumn("url_hash"), maskedColumn("url", maskOCRTransferURL), exportColumn("image_sha256"), exportColumn("content_type"), exportColumn("ocr_text"), exportColumn("updated_at")},
		load: func(db *gorm.DB, limit int) ([][]string, error) {
			var records []entity.AIOCRCache
			if err := exportLimit(db.Session(&gorm.Session{NewDB: true}).Order("id ASC, url_hash ASC"), limit).Find(&records).Error; err != nil {
				return nil, err
			}
			out := make([][]string, 0, len(records))
			for _, row := range records {
				id := ""
				if row.ID != nil {
					id = strconv.FormatUint(*row.ID, 10)
				}
				out = append(out, []string{id, row.URLHash, row.URL, row.ImageSHA256, row.ContentType, row.OCRText, row.UpdatedAt.Format(time.RFC3339Nano)})
			}
			return out, nil
		},
	}
}

func importOCRCacheRows(db *gorm.DB, opts ImportDataOptions, format string) (ImportResult, error) {
	header, rows, err := parseManagementRows(format, opts.Content, ocrTransferHeaders())
	if err != nil {
		return ImportResult{}, err
	}
	if limit := importResolveLimit(ImportResourceOCRCache, opts.Limit); limit > 0 && len(rows) > limit {
		return ImportResult{}, fmt.Errorf("%w：单次最多 %d 行", ErrImportRowLimitExceeded, limit)
	}
	index := importColumnIndex(header)
	if err := requireImportColumns(index, []string{"url", "image_sha256", "ocr_text"}); err != nil {
		return ImportResult{}, err
	}
	if !db.Migrator().HasTable(&entity.AIOCRCache{}) || !db.Migrator().HasTable(&entity.AIOCRCacheSequence{}) {
		return ImportResult{}, ErrAIOCRCacheUnavailable
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	result := ImportResult{Total: len(rows), Details: make([]ImportRowResult, 0, len(rows))}
	seen := make(map[string]bool, len(rows))
	if !opts.DryRun {
		lock := aiOCRCacheLock(db)
		lock.Lock()
		defer lock.Unlock()
	}
	epochAdvanced := false
	for i, row := range rows {
		address := importCell(row, index, "url")
		entry := ImportRowResult{Row: i + 2, Key: address}
		imageHash := strings.ToLower(importCell(row, index, "image_sha256"))
		text, contentType := importCell(row, index, "ocr_text"), importCell(row, index, "content_type")
		var rowErr error
		hash := hashAIOCRURL(address)
		if address == "" {
			hash = strings.ToLower(importCell(row, index, "url_hash"))
			if decoded, err := hex.DecodeString(hash); err != nil || len(decoded) != 32 {
				rowErr = fmt.Errorf("历史记录缺少 url 时必须提供有效 url_hash")
			}
		} else if !validOCRTransferURL(address) {
			rowErr = fmt.Errorf("url 必须是不含用户信息和片段的 HTTP/HTTPS 地址")
		}
		if decoded, err := hex.DecodeString(imageHash); rowErr == nil && (err != nil || len(decoded) != 32) {
			rowErr = fmt.Errorf("image_sha256 必须是 64 位十六进制图片哈希")
		}
		if rowErr == nil && (text == "" || len(text) > 2<<20) {
			rowErr = fmt.Errorf("ocr_text 必填且不超过 2 MiB")
		}
		if rowErr == nil && (utf8.RuneCountInString(contentType) > 128 || (contentType != "" && !strings.HasPrefix(contentType, "image/"))) {
			rowErr = fmt.Errorf("content_type 必须是图片类型")
		}
		updatedAt := now()
		if value := importCell(row, index, "updated_at"); rowErr == nil && value != "" {
			updatedAt, rowErr = time.Parse(time.RFC3339Nano, value)
			if rowErr != nil {
				rowErr = fmt.Errorf("updated_at 必须是 RFC3339 时间")
			}
		}
		if rowErr == nil && seen[hash] {
			entry.Result, entry.Reason = ImportRowSkipped, "本批内重复 URL"
			result.Skipped++
			result.Details = append(result.Details, entry)
			continue
		}
		var count int64
		if rowErr == nil {
			rowErr = db.Session(&gorm.Session{NewDB: true}).Model(&entity.AIOCRCache{}).Where("url_hash = ?", hash).Count(&count).Error
		}
		entry.Result = ImportRowCreated
		if count > 0 {
			entry.Result = ImportRowUpdated
		}
		if rowErr == nil && !opts.DryRun {
			rowErr = db.Transaction(func(tx *gorm.DB) error {
				sequence := entity.AIOCRCacheSequence{}
				if err := tx.Session(&gorm.Session{NewDB: true}).Create(&sequence).Error; err != nil {
					return err
				}
				record := entity.AIOCRCache{ID: &sequence.ID, URLHash: hash, URL: address, ImageSHA256: imageHash, ContentType: contentType, OCRText: text, UpdatedAt: updatedAt}
				return tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "url_hash"}}, DoUpdates: clause.AssignmentColumns([]string{"id", "url", "image_sha256", "content_type", "ocr_text", "updated_at"})}).Create(&record).Error
			})
			if rowErr == nil && !epochAdvanced {
				bumpAIOCRCacheEpoch(db)
				epochAdvanced = true
			}
		}
		if rowErr != nil {
			entry.Result, entry.Reason = ImportRowFailed, rowErr.Error()
			result.Failed++
		} else {
			result.Succeeded++
			seen[hash] = true
		}
		result.Details = append(result.Details, entry)
	}
	result.ErrorReport = buildImportErrorReport(result.Details)
	return result, nil
}
