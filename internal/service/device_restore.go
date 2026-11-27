package service

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DeviceRestoreTarget is a deliberate selection from one admin binding row,
// not a hint from the surrounding user filter. The current ban revision
// prevents a stale confirmation from deleting a changed rule.
type DeviceRestoreTarget struct {
	UserID      int32
	BindingID   int64
	BanEntryID  int64
	BanRevision string
}

type DeviceRestoreResult struct {
	UserID    int32  `json:"userId"`
	BindingID int64  `json:"bindingId"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
}

// UnbanDeviceAndRestoreWithAudit deletes the selected, still-active rule and
// considers only the confirmed original binding. It shares the device/account
// lock order used by registration; the quota read additionally locks the
// counted bindings while existing session reports keep their original semantics.
func UnbanDeviceAndRestoreWithAudit(db *gorm.DB, deviceID string, target DeviceRestoreTarget, actor DeviceHistoryActor) (DeviceUnbanResult, error) {
	if db == nil {
		return DeviceUnbanResult{}, ErrBlacklistCacheNoDatabase
	}
	normalized := NormalizeBlacklistDeviceID(deviceID)
	if normalized == "" || normalized != strings.TrimSpace(deviceID) || !validDeviceHostIdentifier(normalized) ||
		target.UserID <= 0 || target.BindingID <= 0 || target.BanEntryID <= 0 || actor.UserID <= 0 {
		return DeviceUnbanResult{}, ErrInvalidDeviceID
	}
	if _, err := hex.DecodeString(target.BanRevision); err != nil || len(target.BanRevision) != 64 {
		return DeviceUnbanResult{}, ErrInvalidDeviceID
	}
	sessions := deviceSessionColumnsAvailable(db)
	if !hostAdmissionSchemaReady(db) || !hasDeviceHistoryColumn(db) || !sessions.hasReportedAt {
		return DeviceUnbanResult{}, ErrDeviceHistoryUnavailable
	}
	result := DeviceUnbanResult{DeviceID: normalized, Restore: &DeviceRestoreResult{
		UserID: target.UserID, BindingID: target.BindingID, Status: "unchanged", Reason: "ban_changed",
	}}
	hash := BlacklistPatternHashHex(normalized)
	var transact deviceTransaction
	if db.Dialector.Name() == "mysql" {
		// A heartbeat may commit while the locking quota read is waiting. In
		// MariaDB with snapshot isolation enabled, mixing a prior repeatable
		// snapshot with a current read can instead return error 1020.
		transact = func(target *gorm.DB, run func(*gorm.DB) error) error {
			return target.Transaction(run, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		}
	}
	err := withDeviceRegistrationWriteUsing(db, target.UserID, normalized, true, transact, func(tx *gorm.DB) error {
		var rules []entity.BlacklistEntry
		if err := deviceAdminSession(tx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("kind = ? AND value_hash = ?", BlacklistKindDevice, hash).Order("id").Find(&rules).Error; err != nil {
			return err
		}
		var picked *entity.BlacklistEntry
		for i := range rules {
			if rules[i].ID == target.BanEntryID {
				picked = &rules[i]
				break
			}
		}
		if picked == nil || deviceBanRevision(*picked) != target.BanRevision {
			// No transition, including no durable creation of a new lock row.
			return errDeviceUnbanNoTransition
		}
		var binding entity.AccountDeviceBinding
		err := deviceAdminSession(tx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND user_id = ? AND "+exactDeviceIDSQL(tx, "device_id", "?"), target.BindingID, target.UserID, normalized).
			Take(&binding).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDeviceBindingNotFound
		}
		if err != nil {
			return err
		}

		now := time.Now()
		active := func(row *entity.BlacklistEntry) bool {
			return row.Enabled && (row.ExpiresAt == nil || row.ExpiresAt.After(now))
		}
		if !active(picked) {
			return errDeviceUnbanNoTransition
		}
		deleted := deviceAdminSession(tx).Where("id = ? AND kind = ? AND value_hash = ?", picked.ID, BlacklistKindDevice, hash).
			Delete(&entity.BlacklistEntry{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected != 1 {
			return errDeviceUnbanNoTransition
		}
		result.Deleted = 1
		result.Restore.Status = "removed"
		for i := range rules {
			if rules[i].ID != picked.ID && active(&rules[i]) {
				result.Restore.Reason = "device_still_banned"
				break
			}
		}
		if result.Restore.Reason != "device_still_banned" {
			status, reason, err := restoreSelectedDeviceBinding(tx, binding, now)
			if err != nil {
				return err
			}
			result.Restore.Status, result.Restore.Reason = status, reason
		}
		return WriteAccountAuditWithDB(tx, AccountAuditEvent{
			UserID: actor.UserID, Username: actor.Username, Module: "device", Action: 4, Description: "解封设备",
			Result: map[string]any{
				"name": maskDeviceAuditID(normalized), "scope": "global_with_explicit_binding",
				"requestId": strconv.FormatInt(picked.ID, 10), "targetUserId": target.UserID,
				"after": map[string]any{
					"requestId": strconv.FormatInt(binding.ID, 10), "status": result.Restore.Status, "remark": result.Restore.Reason,
				},
			},
		})
	})
	if errors.Is(err, ErrInvalidAccessTokenAuthorization) {
		return DeviceUnbanResult{}, ErrDeviceBindingNotFound
	}
	if err != nil && !errors.Is(err, errDeviceUnbanNoTransition) {
		return DeviceUnbanResult{}, fmt.Errorf("解封与恢复绑定失败：%w", err)
	}
	return result, nil
}

func restoreSelectedDeviceBinding(tx *gorm.DB, binding entity.AccountDeviceBinding, now time.Time) (status, reason string, err error) {
	if binding.HistoryHiddenAt != nil {
		return "removed", "binding_unavailable", nil
	}
	if binding.RevokedAt == nil {
		return "unchanged", "binding_active", nil
	}
	if binding.RevokedReason != entity.DeviceBindingRevokeReasonDeviceBanned {
		return "removed", "not_revoked_by_ban", nil
	}

	var user entity.User
	err = deviceAdminSession(tx).Select("id", "status").Where("id = ?", binding.UserID).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "removed", "account_unavailable", nil
	}
	if err != nil {
		return "", "", err
	}
	banned, err := IsBannedAt(tx, binding.UserID, now)
	if err != nil {
		return "", "", err
	}
	if banned {
		return "removed", "account_banned", nil
	}
	// IsBannedAt also understands naturally expired temporary bans whose
	// persisted status is still "banned". An explicitly disabled user is not eligible.
	if state := strings.ToLower(strings.TrimSpace(user.Status)); state != "enabled" && state != "banned" {
		return "removed", "account_unavailable", nil
	}
	blocked, err := AccountDeviceHasBlock(tx, binding.UserID, binding.DeviceID)
	if err != nil {
		return "", "", err
	}
	if blocked {
		return "removed", "account_blocked", nil
	}
	if binding.OwnerKind == entity.DeviceBindingOwnerKindToken {
		if !tx.Migrator().HasTable(&entity.AccessToken{}) {
			return "", "", ErrDeviceHistoryUnavailable
		}
		var token entity.AccessToken
		err := deviceAdminSession(tx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "status", "script_id", "total_quota_limit").
			Where("id = ? AND user_id = ?", binding.TokenID, binding.UserID).Take(&token).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "removed", "credential_unavailable", nil
		}
		if err != nil {
			return "", "", err
		}
		if !strings.EqualFold(strings.TrimSpace(token.Status), "enabled") || token.ScriptID != 0 || !AccessTokenQuotaLimitValid(token.TotalQuotaLimit) {
			return "removed", "credential_unavailable", nil
		}
	} else if binding.OwnerKind != entity.DeviceBindingOwnerKindLogin && binding.OwnerKind != "" {
		return "removed", "credential_unavailable", nil
	}
	query := devicePoolSession(tx).Model(&entity.AccountDeviceBinding{}).
		Where(exactDeviceIDSQL(tx, "device_id", "?"), binding.DeviceID)
	var claims []string
	if err := ActiveOrdinaryDeviceBindings(tx, query).Pluck("client_instance", &claims).Error; err != nil {
		return "", "", err
	}
	for _, claimed := range claims {
		if claimed != "" && claimed != binding.ClientInstance {
			return "removed", "binding_conflict", nil
		}
	}

	limit, _, err := effectiveDeviceLimit(tx, binding.UserID, true)
	if err != nil {
		return "", "", err
	}
	if limit < 0 {
		return "removed", "quota_forbidden", nil
	}
	used, err := countDeviceSlots(tx, binding.UserID, true)
	if err != nil {
		return "", "", err
	}
	if limit > 0 && used >= int(limit) {
		return "removed", "quota_full", nil
	}

	revoke := deviceHistoryBinding{
		ID: binding.ID, UserID: binding.UserID, DeviceID: binding.DeviceID,
		RevokedAt: binding.RevokedAt, RevokedReason: binding.RevokedReason,
	}
	if err := preserveDeviceRevocation(tx, revoke); err != nil {
		return "", "", err
	}
	// Restore the binding only. Do not restore old process claims or invent a
	// login/last-seen time. A restored binding consumes the ordinary one slot.
	res := deviceAdminSession(tx).Model(&entity.AccountDeviceBinding{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NOT NULL AND revoked_reason = ?", binding.ID, binding.UserID, entity.DeviceBindingRevokeReasonDeviceBanned).
		Updates(map[string]any{"revoked_at": nil, "revoked_reason": "", "session_count": 0, "session_reported_at": nil})
	if res.Error != nil {
		return "", "", res.Error
	}
	if res.RowsAffected != 1 {
		return "", "", ErrDeviceHistoryUnavailable
	}
	return "restored", "", nil
}
