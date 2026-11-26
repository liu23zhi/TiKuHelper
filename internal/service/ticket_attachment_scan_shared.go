package service

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ticketImageWorkerLeasePrefix = "ticket-worker:"
const ticketImageWorkerLeaseTime = 2 * time.Minute

var errTicketImageWorkerClaimLost = errors.New("ticket image worker claim lost")

type ticketImageWorkerClaim struct {
	row         entity.TicketAttachment
	token       string
	shared      bool
	owned       bool
	reusedState string
}

// The cache primary key is the cross-process SHA lock. A unique, locally
// generated token is stored only while pending, and checked again on finish.
// Never keep the SQL transaction open during the network call.
func claimTicketImageWorker(db *gorm.DB, row entity.TicketAttachment, now time.Time, diagnosticID string) (ticketImageWorkerClaim, error) {
	claim := ticketImageWorkerClaim{row: row}
	if !hasTicketImageSharedCache(db, row.SHA256) {
		var err error
		claim.owned, err = claimTicketImageWorkerRow(db, row, now)
		return claim, err
	}
	claim.shared, claim.token = true, ticketImageWorkerLeasePrefix+diagnosticID
	err := db.Transaction(func(tx *gorm.DB) error {
		entry := entity.SensitiveImageCacheEntry{
			ImageSHA256: row.SHA256, ContentType: row.MediaType, FileSize: row.ByteSize,
			Status: entity.SensitiveImageCacheStatusPending, Provider: claim.token, UpdatedAt: now,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error; err != nil {
			return err
		}
		current, _, err := lockTicketImageCache(tx, row.SHA256)
		if err != nil {
			return err
		}
		if current.Status == entity.SensitiveImageCacheStatusClean || current.Status == entity.SensitiveImageCacheStatusSensitive {
			var err error
			claim.reusedState, err = reuseTicketImageVerdictInTx(tx, current, now)
			return err
		}
		if current.Provider != claim.token && current.Status == entity.SensitiveImageCacheStatusPending {
			window := sensitiveImagePendingTimeout
			if strings.HasPrefix(current.Provider, ticketImageWorkerLeasePrefix) {
				window = ticketImageWorkerLeaseTime
			}
			if current.UpdatedAt.After(now.Add(-window)) {
				return nil
			}
		}

		// An old synchronous flagged upload may predate the shared cache. A real
		// legacy sensitive result (not a quota mark) is never opened by the worker.
		strict, err := hasTicketImageRestrictiveReference(tx, row.SHA256)
		if err != nil {
			return err
		}
		if strict {
			claim.reusedState, err = reuseTicketImageVerdictInTx(tx, current, now)
			return err
		}

		// Reuse the oldest most advanced reference as the durable retry anchor:
		// inserting a duplicate cannot bypass the first reference's backoff or give
		// this SHA another retry budget after exhaustion.
		var anchor entity.TicketAttachment
		err = tx.Where("sha256 = ? AND scan_state IN ?", row.SHA256, []string{
			entity.TicketAttachmentScanPending, entity.TicketAttachmentScanScanning, entity.TicketAttachmentScanFailed,
		}).Order("attempt_count DESC, id ASC").First(&anchor).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errTicketImageWorkerClaimLost
		}
		if err != nil {
			return err
		}
		claim.row = anchor
		if claim.row.ScanState == entity.TicketAttachmentScanFailed && claim.row.AttemptCount >= TicketAttachmentScanMaxAttempts {
			updates := map[string]any{"scan_state": entity.TicketAttachmentScanFailed, "attempt_count": claim.row.AttemptCount,
				"next_attempt_at": nil, "check_error": SafeSensitiveImageHistoryReason(entity.SensitiveImageCacheStatusError, claim.row.CheckError), "updated_at": now}
			if err := updateTicketImageWorkerReferences(tx, row.SHA256, updates); err != nil {
				return err
			}
			return persistTicketImageSharedVerdict(tx, claim.row, TicketAttachmentScanResult{
				State: entity.TicketAttachmentScanFailed, CheckError: claim.row.CheckError,
			}, entity.TicketAttachmentScanFailed, now)
		}
		owned, err := claimTicketImageWorkerRow(tx, claim.row, now)
		if err != nil {
			return err
		}
		if !owned {
			return errTicketImageWorkerClaimLost
		}
		if err := tx.Model(&entity.SensitiveImageCacheEntry{}).Where("image_sha256 = ?", row.SHA256).
			Updates(map[string]any{"status": entity.SensitiveImageCacheStatusPending, "provider": claim.token,
				"reason": "", "updated_at": now}).Error; err != nil {
			return err
		}
		claim.owned = true
		return nil
	})
	if errors.Is(err, errTicketImageWorkerClaimLost) {
		return claim, nil
	}
	return claim, err
}

func hasTicketImageSharedCache(db *gorm.DB, sha string) bool {
	return isTicketImageDigest(sha) && db.Migrator().HasTable(entity.TableNameSensitiveImageCache)
}

func isTicketImageDigest(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	_, err := hex.DecodeString(sha)
	return err == nil
}

func hasTicketImageRestrictiveReference(tx *gorm.DB, sha string) (bool, error) {
	var prior entity.TicketAttachment
	err := tx.Select("id").Where("sha256 = ? AND scan_state = ? AND scan_marked_by <> ?",
		sha, entity.TicketAttachmentScanSensitive, entity.TicketAttachmentMarkedByQuota).First(&prior).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

func reuseTicketImageVerdictInTx(tx *gorm.DB, entry entity.SensitiveImageCacheEntry, now time.Time) (string, error) {
	if entry.Status != entity.SensitiveImageCacheStatusSensitive {
		strict, err := hasTicketImageRestrictiveReference(tx, entry.ImageSHA256)
		if err != nil {
			return "", err
		}
		if strict {
			entry.Status, entry.UpdatedAt = entity.SensitiveImageCacheStatusSensitive, now
			if err := tx.Model(&entity.SensitiveImageCacheEntry{}).Where("image_sha256 = ?", entry.ImageSHA256).
				Updates(map[string]any{"status": entry.Status, "provider": "legacy-worker",
					"reason": "历史检测判定敏感", "updated_at": now}).Error; err != nil {
				return "", err
			}
		}
	}
	state := ticketScanStateFromSharedCache(entry.Status)
	if state == "" {
		return "", nil
	}
	checked := entry.UpdatedAt
	if checked.IsZero() {
		checked = now
	}
	if err := tx.Model(&entity.TicketAttachment{}).Where("sha256 = ?", entry.ImageSHA256).
		Where("scan_state <> ? OR scan_marked_by <> ?", state, entity.TicketAttachmentMarkedByUpstream).
		Updates(map[string]any{"scan_state": state, "scan_marked_by": entity.TicketAttachmentMarkedByUpstream,
			"checked_at": checked, "updated_at": now, "check_error": "", "next_attempt_at": nil}).Error; err != nil {
		return "", err
	}
	if ticketAttachmentBlobReady(tx) {
		if err := tx.Model(&entity.TicketAttachmentBlob{}).Where("sha256 = ? AND scan_state <> ?", entry.ImageSHA256, state).
			Updates(map[string]any{"scan_state": state, "updated_at": now}).Error; err != nil {
			return "", err
		}
	}
	return state, nil
}

func lockTicketImageCache(tx *gorm.DB, sha string) (entity.SensitiveImageCacheEntry, bool, error) {
	var entry entity.SensitiveImageCacheEntry
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("image_sha256 = ?", sha).First(&entry).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return entry, false, nil
	}
	return entry, err == nil, err
}

func ticketScanStateFromSharedCache(status string) string {
	switch status {
	case entity.SensitiveImageCacheStatusClean:
		return entity.TicketAttachmentScanApproved
	case entity.SensitiveImageCacheStatusSensitive:
		return entity.TicketAttachmentScanSensitive
	default:
		return ""
	}
}

func claimTicketImageWorkerRow(db *gorm.DB, row entity.TicketAttachment, now time.Time) (bool, error) {
	if row.ScanState != entity.TicketAttachmentScanPending || row.AttemptCount >= TicketAttachmentScanMaxAttempts {
		return false, nil
	}
	claim := db.Model(&entity.TicketAttachment{}).
		Where("id = ? AND scan_state = ? AND attempt_count = ? AND updated_at = ?", row.ID, entity.TicketAttachmentScanPending, row.AttemptCount, row.UpdatedAt).
		Where("next_attempt_at IS NULL OR next_attempt_at <= ?", now).
		Updates(map[string]any{"scan_state": entity.TicketAttachmentScanScanning, "updated_at": now})
	return claim.RowsAffected > 0, claim.Error
}

type ticketImageWorkerOutcome struct{ event, state string }

func completeTicketImageWorker(db *gorm.DB, claim ticketImageWorkerClaim, scan TicketAttachmentScanResult, now time.Time) (ticketImageWorkerOutcome, error) {
	outcome := ticketImageWorkerOutcome{event: "detect_superseded", state: "-"}
	err := db.Transaction(func(tx *gorm.DB) error {
		if claim.shared {
			current, _, err := lockTicketImageCache(tx, claim.row.SHA256)
			if err != nil {
				return err
			}
			if current.Status != entity.SensitiveImageCacheStatusPending || current.Provider != claim.token {
				if state := ticketScanStateFromSharedCache(current.Status); state != "" {
					outcome.state = state
					return nil
				}
				return readTicketImageWorkerFinalState(tx, claim.row.ID, &outcome)
			}
		}
		updates, state := ticketAttachmentScanUpdates(claim.row, scan, now)
		accepted := tx.Model(&entity.TicketAttachment{}).
			Where("id = ? AND scan_state = ? AND attempt_count = ?", claim.row.ID, entity.TicketAttachmentScanScanning, claim.row.AttemptCount).
			Updates(updates)
		if accepted.Error != nil {
			return accepted.Error
		}
		if accepted.RowsAffected == 0 {
			return readTicketImageWorkerFinalState(tx, claim.row.ID, &outcome)
		}
		if claim.shared {
			if err := updateTicketImageWorkerReferences(tx, claim.row.SHA256, updates); err != nil {
				return err
			}
		}
		if err := persistTicketImageSharedVerdict(tx, claim.row, scan, state, now); err != nil {
			return err
		}
		outcome.event, outcome.state = "detect_applied", state
		if state == entity.TicketAttachmentScanPending {
			outcome.event = "detect_retry_scheduled"
		}
		return nil
	})
	return outcome, err
}

func readTicketImageWorkerFinalState(tx *gorm.DB, id int64, out *ticketImageWorkerOutcome) error {
	var current entity.TicketAttachment
	err := tx.Select("scan_state").Where("id = ?", id).Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	switch current.ScanState {
	case entity.TicketAttachmentScanPending, entity.TicketAttachmentScanScanning, entity.TicketAttachmentScanApproved,
		entity.TicketAttachmentScanSensitive, entity.TicketAttachmentScanFailed:
		out.state = current.ScanState
	}
	return err
}

func updateTicketImageWorkerReferences(tx *gorm.DB, sha string, updates map[string]any) error {
	query := tx.Model(&entity.TicketAttachment{}).Where("sha256 = ?", sha)
	switch updates["scan_state"] {
	case entity.TicketAttachmentScanApproved:
		query = query.Where("scan_state <> ? OR scan_marked_by = ?", entity.TicketAttachmentScanSensitive, entity.TicketAttachmentMarkedByQuota)
	case entity.TicketAttachmentScanSensitive:
		// An affirmative restrictive ruling can hide all references.
	default:
		query = query.Where("scan_state IN ?", []string{entity.TicketAttachmentScanPending, entity.TicketAttachmentScanScanning})
	}
	return query.Updates(updates).Error
}

// Publish the stronger synchronous ruling before the attachment is returned
// to the HTTP handler. Existing references and blobs update in the same DB tx.
func applyTicketUploadVerdict(tx *gorm.DB, row *entity.TicketAttachment, now time.Time) error {
	if !hasTicketImageSharedCache(tx, row.SHA256) {
		return nil
	}
	if row.ScanState == entity.TicketAttachmentScanSensitive {
		entry := entity.SensitiveImageCacheEntry{
			ImageSHA256: row.SHA256, ContentType: row.MediaType, FileSize: row.ByteSize,
			Status: entity.SensitiveImageCacheStatusSensitive, Provider: "ticket-upload",
			Reason: "上游判定为敏感图片", UpdatedAt: now,
		}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "image_sha256"}},
			DoUpdates: clause.AssignmentColumns([]string{"status", "provider", "reason", "updated_at"})}).Create(&entry).Error; err != nil {
			return err
		}
		row.CheckedAt = &now
		row.ScanMarkedBy = entity.TicketAttachmentMarkedByUpstream
		_, err := syncSensitiveImageScanStateInDB(tx, row.SHA256, entry.Status, now)
		return err
	}
	current, _, err := lockTicketImageCache(tx, row.SHA256)
	if err != nil {
		return err
	}
	if ticketScanStateFromSharedCache(current.Status) != "" {
		state, err := reuseTicketImageVerdictInTx(tx, current, now)
		if err != nil {
			return err
		}
		row.ScanState = state
		row.CheckedAt = &now
		row.ScanMarkedBy = entity.TicketAttachmentMarkedByUpstream
	}
	return nil
}
