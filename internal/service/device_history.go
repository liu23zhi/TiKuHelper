package service

import (
	"database/sql"
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrDeviceHistoryNotRemoved = errors.New("只能删除已移除且未拉黑的设备历史")
var ErrDeviceHistoryUnavailable = errors.New("设备历史功能尚未就绪")
var ErrDeviceAlreadyGloballyBanned = errors.New("该设备已被全局拉黑，不能改为账号级拉黑")

// The database lock orders same-device mutations across processes. The bounded
// mutex also avoids competing SQLite writers for this key and covers old schemas
// during the migration window; history deletion itself requires the new schema.
var deviceLifecycleStripes [64]sync.Mutex

func withDeviceLifecycleWrite(db *gorm.DB, deviceID string, fn func(*gorm.DB) error) error {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(deviceID))
	lock := &deviceLifecycleStripes[hash.Sum32()%uint32(len(deviceLifecycleStripes))]
	lock.Lock()
	defer lock.Unlock()
	db = db.Session(&gorm.Session{NewDB: true})
	hasLockTable := db.Migrator().HasTable(&entity.DeviceLifecycleLock{})
	return db.Transaction(func(tx *gorm.DB) error {
		if hasLockTable {
			row := entity.DeviceLifecycleLock{DeviceKey: BlacklistPatternHashHex(deviceID)}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

// ReadDeviceHistorySnapshot keeps the rules and the binding version from one
// read snapshot. MariaDB may be configured for READ COMMITTED, so request
// repeatable reads explicitly for this transaction. SQLite has snapshot reads.
func ReadDeviceHistorySnapshot(db *gorm.DB, read func(*gorm.DB) error) error {
	if db.Dialector.Name() == "mysql" {
		return db.Transaction(read, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	}
	return db.Transaction(read)
}

func hasDeviceHistoryColumn(db *gorm.DB) bool {
	return db.Migrator().HasColumn(entity.TableNameAccountDeviceBinding, "history_hidden_at")
}

// VisibleDeviceHistory excludes presentation-only tombstones when the column
// exists; it does not change any security or quota queries.
func VisibleDeviceHistory(db, query *gorm.DB) *gorm.DB {
	if hasDeviceHistoryColumn(db) {
		return query.Where(entity.TableNameAccountDeviceBinding + ".history_hidden_at IS NULL")
	}
	return query
}

// AccountDeviceHasBlock allows both display and mutation to check the exact
// per-account rule using the same predicate and propagates database failures.
func AccountDeviceHasBlock(db *gorm.DB, userID int32, deviceID string) (bool, error) {
	var count int64
	if !db.Migrator().HasTable(&entity.UserDeviceBlock{}) {
		return false, nil
	}
	err := db.Model(&entity.UserDeviceBlock{}).
		Where("user_id = ? AND device_id = ?", userID, NormalizeDevicePoolDeviceID(deviceID)).
		Count(&count).Error
	return count != 0, err
}

type DeviceHistoryActor struct {
	UserID   int32
	Username string
}

type deviceHistoryBinding struct {
	ID              int64
	UserID          int32
	DeviceID        string
	RevokedAt       *time.Time
	RevokedReason   string
	HistoryHiddenAt *time.Time
}

func findDeviceHistoryBinding(db *gorm.DB, id int64, ownerID int32) (deviceHistoryBinding, error) {
	var record deviceHistoryBinding
	query := db.Table(entity.TableNameAccountDeviceBinding).Where("id = ?", id)
	if ownerID > 0 {
		query = query.Where("user_id = ?", ownerID)
	}
	if err := query.Take(&record).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return record, ErrDeviceBindingNotFound
	} else if err != nil {
		return record, err
	}
	return record, nil
}

func deviceHistoryAudit(tx *gorm.DB, actor DeviceHistoryActor, action int32, description string, row deviceHistoryBinding, remark string) error {
	return WriteAccountAuditWithDB(tx, AccountAuditEvent{
		UserID: actor.UserID, Username: actor.Username, Module: "device", Action: action, Description: description,
		Result: map[string]any{
			"targetUserId": row.UserID, "requestId": strconv.FormatInt(row.ID, 10),
			"name": MaskBlacklistDeviceID(row.DeviceID), "remark": remark,
		},
	})
}

func deviceRevocationAuditKey(row deviceHistoryBinding) string {
	return "device-revoke:" + strconv.FormatInt(row.ID, 10) + ":" +
		BlacklistPatternHashHex(row.RevokedAt.UTC().Format(time.RFC3339Nano)+"\x00"+row.RevokedReason)
}

func deviceRevocationAudit(tx *gorm.DB, actor DeviceHistoryActor, action int32, description string, row deviceHistoryBinding, remark string) error {
	if row.RevokedAt == nil {
		return errors.New("设备撤销审计缺少已保存的撤销时间")
	}
	scope := "binding_revocation"
	if actor.UserID == 0 {
		scope = "legacy_revocation"
	}
	return WriteAccountAuditWithDB(tx, AccountAuditEvent{
		UserID: actor.UserID, Username: actor.Username, Module: "device", Action: action,
		Description: description, CorrelationID: deviceRevocationAuditKey(row),
		Result: map[string]any{
			"targetUserId": row.UserID, "requestId": strconv.FormatInt(row.ID, 10),
			"name": MaskBlacklistDeviceID(row.DeviceID), "remark": remark, "method": row.RevokedReason,
			"scope": scope, "revokedAt": row.RevokedAt.UTC().Format(time.RFC3339Nano),
		},
	})
}

// preserveDeviceRevocation archives only facts still present on an old
// binding, before rebind clears them. New transition audits use the same key
// (derived from database-stored time), so they are not copied or rewritten.
func preserveDeviceRevocation(tx *gorm.DB, row deviceHistoryBinding) error {
	if row.RevokedAt == nil || row.RevokedReason == "" {
		return nil
	}
	var existing int64
	if err := tx.Model(&entity.Log{}).
		Where("module = ? AND correlation_id = ?", "device", deviceRevocationAuditKey(row)).
		Count(&existing).Error; err != nil {
		return err
	}
	if existing != 0 {
		return nil
	}
	return deviceRevocationAudit(tx, DeviceHistoryActor{}, 2, "保全历史设备撤销证据", row, row.RevokedReason)
}

// ClearDeviceHistory is idempotent while the record remains removed. A concurrent
// rebind or block is checked under the same lock before hiding any history.
func ClearDeviceHistory(db *gorm.DB, id int64, ownerID int32, actor DeviceHistoryActor) (string, error) {
	if !deviceBindingSchemaReady(db) {
		return "", ErrDeviceBindingNotFound
	}
	first, err := findDeviceHistoryBinding(db, id, ownerID)
	if err != nil {
		return "", err
	}
	if !deviceRevocationColumnsAvailable(db) || !hasDeviceHistoryColumn(db) {
		return "", ErrDeviceHistoryUnavailable
	}
	err = withDeviceLifecycleWrite(db, NormalizeDevicePoolDeviceID(first.DeviceID), func(tx *gorm.DB) error {
		row, err := findDeviceHistoryBinding(tx, id, ownerID)
		if err != nil {
			return err
		}
		if row.RevokedAt == nil {
			return ErrDeviceHistoryNotRemoved
		}
		personal, err := AccountDeviceHasBlock(tx, row.UserID, row.DeviceID)
		if err != nil {
			return err
		}
		global, err := LoadActiveDeviceBansWithDB(tx, []string{row.DeviceID}, time.Now())
		if err != nil {
			return err
		}
		if personal || len(global) != 0 {
			return ErrDeviceHistoryNotRemoved
		}
		if row.HistoryHiddenAt != nil {
			return nil // A duplicate cannot hide any newly visible record.
		}
		result := tx.Model(&entity.AccountDeviceBinding{}).
			Where("id = ? AND user_id = ? AND revoked_at IS NOT NULL AND history_hidden_at IS NULL", id, row.UserID)
		if tx.Migrator().HasTable(&entity.UserDeviceBlock{}) {
			result = result.Where("NOT EXISTS (SELECT 1 FROM user_device_blocks AS b WHERE b.user_id = account_device_bindings.user_id AND b.device_id = account_device_bindings.device_id)")
		}
		updated := result.UpdateColumn("history_hidden_at", time.Now())
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrDeviceHistoryNotRemoved
		}
		return deviceHistoryAudit(tx, actor, 4, "删除设备历史展示", row, row.RevokedReason)
	})
	return first.DeviceID, err
}

// BlockAccountDevice preserves previous removal information and writes the rule,
// revocation and its audit atomically. A duplicate keeps the original reason.
func BlockAccountDevice(db *gorm.DB, id int64, actor DeviceHistoryActor, reason string) (entity.UserDeviceBlock, error) {
	var stored entity.UserDeviceBlock
	if !deviceBindingSchemaReady(db) {
		return stored, ErrDeviceBindingNotFound
	}
	first, err := findDeviceHistoryBinding(db, id, actor.UserID)
	if err != nil {
		return stored, err
	}
	deviceID := NormalizeDevicePoolDeviceID(first.DeviceID)
	if deviceID == "" {
		return stored, ErrInvalidDeviceID
	}
	if !deviceRevocationColumnsAvailable(db) || !db.Migrator().HasTable(&entity.UserDeviceBlock{}) {
		return stored, ErrDeviceHistoryUnavailable
	}
	err = withDeviceLifecycleWrite(db, deviceID, func(tx *gorm.DB) error {
		row, err := findDeviceHistoryBinding(tx, id, actor.UserID)
		if err != nil {
			return err
		}
		if row.HistoryHiddenAt != nil {
			return ErrDeviceBindingNotFound
		}
		global, err := LoadActiveDeviceBansWithDB(tx, []string{row.DeviceID}, time.Now())
		if err != nil {
			return err
		}
		if len(global) != 0 {
			return ErrDeviceAlreadyGloballyBanned
		}
		reason = strings.TrimSpace(reason)
		if runes := []rune(reason); len(runes) > 255 {
			reason = string(runes[:255])
		}
		err = tx.Where("user_id = ? AND device_id = ?", row.UserID, deviceID).Take(&stored).Error
		created := errors.Is(err, gorm.ErrRecordNotFound)
		if created {
			stored = entity.UserDeviceBlock{UserID: row.UserID, DeviceID: deviceID, Reason: reason}
			err = tx.Create(&stored).Error
		}
		if err != nil {
			return err
		}
		result := tx.Model(&entity.AccountDeviceBinding{}).Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, row.UserID).
			UpdateColumns(map[string]any{"revoked_at": time.Now(), "revoked_reason": entity.DeviceBindingRevokeReasonUserBlocked})
		if result.Error != nil {
			return result.Error
		}
		if !created && result.RowsAffected == 0 {
			return nil
		}
		if result.RowsAffected != 0 {
			saved, err := findDeviceHistoryBinding(tx, id, actor.UserID)
			if err != nil {
				return err
			}
			return deviceRevocationAudit(tx, actor, 3, "拉黑账号设备", saved, stored.Reason)
		}
		return deviceHistoryAudit(tx, actor, 3, "拉黑账号设备", row, stored.Reason)
	})
	return stored, err
}

// RemoveAccountDevice releases the slot and audits the first transition without
// changing the device's first/latest use. A permitted registration may later
// clear the binding's revocation fields, but never the audit.
func RemoveAccountDevice(db *gorm.DB, id int64, actor DeviceHistoryActor) (entity.AccountDeviceBinding, bool, error) {
	var result entity.AccountDeviceBinding
	if !deviceBindingSchemaReady(db) {
		return result, false, ErrDeviceBindingNotFound
	}
	first, err := findDeviceHistoryBinding(db, id, actor.UserID)
	if err != nil {
		return result, false, err
	}
	if !deviceRevocationColumnsAvailable(db) {
		return result, false, ErrDeviceHistoryUnavailable
	}
	already := false
	err = withDeviceLifecycleWrite(db, NormalizeDevicePoolDeviceID(first.DeviceID), func(tx *gorm.DB) error {
		row, err := findDeviceHistoryBinding(tx, id, actor.UserID)
		if err != nil {
			return err
		}
		if row.HistoryHiddenAt != nil {
			return ErrDeviceBindingNotFound
		}
		result = entity.AccountDeviceBinding{ID: row.ID, DeviceID: row.DeviceID,
			RevokedAt: row.RevokedAt, RevokedReason: row.RevokedReason}
		if row.RevokedAt != nil {
			already = true
			return nil
		}
		now := time.Now()
		result.RevokedAt, result.RevokedReason = &now, entity.DeviceBindingRevokeReasonUserRemoved
		update := tx.Model(&entity.AccountDeviceBinding{}).Where("id = ? AND revoked_at IS NULL", id).
			UpdateColumns(map[string]any{"revoked_at": now, "revoked_reason": result.RevokedReason})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			// An existing path such as the idle worker may already have removed
			// it without taking a device lock. Preserve the actual first reason.
			latest, err := findDeviceHistoryBinding(tx, id, actor.UserID)
			if err != nil {
				return err
			}
			result.RevokedAt, result.RevokedReason = latest.RevokedAt, latest.RevokedReason
			already = true
			return nil
		}
		saved, err := findDeviceHistoryBinding(tx, id, actor.UserID)
		if err != nil {
			return err
		}
		result.RevokedAt = saved.RevokedAt
		return deviceRevocationAudit(tx, actor, 2, "移除账号设备", saved, result.RevokedReason)
	})
	return result, already, err
}

// UnblockAccountDevice keeps the existing explicit, account-scoped undo
// operation. Legacy rules may still have an apparently active binding; finalize
// that rule's original revocation before removing it, never restore a slot here.
func UnblockAccountDevice(db *gorm.DB, id int64, actor DeviceHistoryActor) (entity.UserDeviceBlock, error) {
	var stored entity.UserDeviceBlock
	if !db.Migrator().HasTable(&entity.UserDeviceBlock{}) {
		return stored, ErrDeviceHistoryUnavailable
	}
	find := func(db *gorm.DB) error {
		if err := db.Where("id = ? AND user_id = ?", id, actor.UserID).Take(&stored).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDeviceBindingNotFound
		} else {
			return err
		}
	}
	if err := find(db); err != nil {
		return stored, err
	}
	err := withDeviceLifecycleWrite(db, NormalizeDevicePoolDeviceID(stored.DeviceID), func(tx *gorm.DB) error {
		if err := find(tx); err != nil {
			return err
		}
		if deviceBindingSchemaReady(tx) {
			if !deviceRevocationColumnsAvailable(tx) {
				return ErrDeviceHistoryUnavailable
			}
			when := stored.CreatedAt
			if when.IsZero() {
				when = time.Now()
			}
			update := tx.Model(&entity.AccountDeviceBinding{}).
				Where("user_id = ? AND device_id = ? AND revoked_at IS NULL", actor.UserID, stored.DeviceID).
				UpdateColumns(map[string]any{"revoked_at": when, "revoked_reason": entity.DeviceBindingRevokeReasonUserBlocked})
			if update.Error != nil {
				return update.Error
			}
		}
		result := tx.Where("id = ? AND user_id = ?", id, actor.UserID).Delete(&entity.UserDeviceBlock{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrDeviceBindingNotFound
		}
		return nil
	})
	return stored, err
}
