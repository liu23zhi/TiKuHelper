package service

import (
	"database/sql/driver"
	"fmt"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

// SQLite loses datetime column metadata inside UNION/COALESCE. Keep the wire
// timestamp contract while accepting both native driver times and SQL text.
type ImageHistoryTime struct{ time.Time }

func (v *ImageHistoryTime) Scan(value any) error {
	if value == nil {
		v.Time = time.Time{}
		return nil
	}
	if native, ok := value.(time.Time); ok {
		v.Time = native
		return nil
	}
	raw, ok := value.(string)
	if !ok {
		if data, yes := value.([]byte); yes {
			raw = string(data)
		} else {
			return fmt.Errorf("无效图片检测时间")
		}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999-07:00 MST", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			v.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("无效图片检测时间 %q", raw)
}
func (v ImageHistoryTime) Value() (driver.Value, error) { return v.Time, nil }

type SensitiveImageHistoryEntry struct {
	ID          *uint64          `json:"id"`
	ImageSHA256 string           `json:"imageSha256"`
	ContentType string           `json:"contentType"`
	FileSize    int64            `json:"fileSize"`
	Status      string           `json:"status"`
	Reason      string           `json:"reason"`
	Provider    string           `json:"provider"`
	HitCount    int64            `json:"hitCount"`
	FirstSeenAt ImageHistoryTime `json:"firstSeenAt"`
	UpdatedAt   ImageHistoryTime `json:"updatedAt"`
}

// SensitiveImageHistoryQuery includes legacy worker verdicts that predate the
// shared cache. Quota-only marks are not actual detections and stay excluded.
func SensitiveImageHistoryQuery(db *gorm.DB) *gorm.DB {
	base := "SELECT id,image_sha256,content_type,file_size,status,reason,provider,hit_count,first_seen_at,updated_at FROM " + entity.TableNameSensitiveImageCache
	if db.Migrator().HasTable(entity.TableNameTicketAttachment) && db.Migrator().HasColumn(entity.TableNameTicketAttachment, "scan_marked_by") {
		base += ` UNION ALL SELECT NULL AS id,a.sha256 AS image_sha256,a.media_type AS content_type,a.byte_size AS file_size,
CASE a.scan_state WHEN 'approved' THEN 'clean' WHEN 'sensitive' THEN 'sensitive' ELSE 'error' END AS status,
CASE a.scan_state WHEN 'failed' THEN '历史检测失败，请重新检测' WHEN 'sensitive' THEN '历史检测判定敏感' ELSE '' END AS reason,
'legacy-worker' AS provider,0 AS hit_count,a.created_at AS first_seen_at,COALESCE(a.checked_at,a.updated_at) AS updated_at
FROM ticket_attachments a WHERE a.scan_state IN ('approved','sensitive','failed') AND COALESCE(a.scan_marked_by,'') <> 'quota'
AND NOT EXISTS (SELECT 1 FROM sensitive_image_cache_entries c WHERE c.image_sha256=a.sha256)
AND NOT EXISTS (SELECT 1 FROM ticket_attachments newer WHERE newer.sha256=a.sha256 AND newer.id>a.id
AND newer.scan_state IN ('approved','sensitive','failed') AND COALESCE(newer.scan_marked_by,'') <> 'quota')`
	}
	return db.Session(&gorm.Session{NewDB: true}).Table("(" + base + ") AS image_history")
}

func persistTicketImageSharedVerdict(db *gorm.DB, row entity.TicketAttachment, scan TicketAttachmentScanResult, now time.Time) error {
	if !db.Migrator().HasTable(entity.TableNameSensitiveImageCache) {
		return nil
	}
	status := entity.SensitiveImageCacheStatusError
	switch scan.State {
	case entity.TicketAttachmentScanApproved:
		status = entity.SensitiveImageCacheStatusClean
	case entity.TicketAttachmentScanSensitive:
		status = entity.SensitiveImageCacheStatusSensitive
	}
	reason := ""
	if status == entity.SensitiveImageCacheStatusError {
		reason = "检测失败，请重新检测"
	}
	entry := entity.SensitiveImageCacheEntry{ImageSHA256: row.SHA256, ContentType: row.MediaType, FileSize: row.ByteSize,
		Status: status, Reason: reason, Provider: "ticket-worker", FirstSeenAt: row.CreatedAt, UpdatedAt: now}
	if entry.FirstSeenAt.IsZero() {
		entry.FirstSeenAt = now
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "image_sha256"}},
		DoUpdates: clause.AssignmentColumns([]string{"status", "reason", "provider", "updated_at", "content_type", "file_size"})}).Create(&entry).Error; err != nil {
		return err
	}
	if db.Migrator().HasTable(entity.TableNameTicketAttachmentBlob) {
		return db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketAttachmentBlob{}).Where("sha256 = ?", row.SHA256).
			Updates(map[string]any{"scan_state": scan.State, "updated_at": now}).Error
	}
	return nil
}

func normalizeImageTransferStatus(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "clean", "approved":
		return entity.SensitiveImageCacheStatusClean, true
	case "sensitive":
		return entity.SensitiveImageCacheStatusSensitive, true
	case "error", "failed":
		return entity.SensitiveImageCacheStatusError, true
	default:
		return "", false
	}
}
