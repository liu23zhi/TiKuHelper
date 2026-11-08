package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ScriptCloseStatusSettling = "settling"
	ScriptCloseStatusRefunded = "refunded"
	ScriptCloseStatusDeleted  = "deleted"
)

var ErrScriptActiveCloseConfirmationRequired = errors.New("脚本客户端正在运行，需要再次确认关闭")

type ScriptCloseResult struct {
	Status              string `json:"status"`
	RefundedMicros      int64  `json:"refundedMicros"`
	ActiveRunSessionID  int64  `json:"activeRunSessionId,omitempty"`
	NeedsReconciliation bool   `json:"needsReconciliation,omitempty"`
}

// CloseUserScriptWithDB serializes close with runtime activation and paid work
// admission on the script row. The client must confirm the currently active
// lease, not a boolean that could also approve a replacement lease.
func CloseUserScriptWithDB(db *gorm.DB, operator entity.User, scriptID int32, confirmedRunSessionID int64) (ScriptCloseResult, error) {
	var result ScriptCloseResult
	if db == nil || scriptID <= 0 || operator.ID <= 0 || confirmedRunSessionID < 0 {
		return result, ErrScriptNotFound
	}
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var script entity.UserScript
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", scriptID)
		if !isAdminUser(operator) {
			query = query.Where("user_id = ?", operator.ID)
		}
		if err := query.Take(&script).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptNotFound
			}
			return err
		}
		now := time.Now()
		if script.BillingMode != entity.ScriptBillingModeEscrow {
			if err := deleteNonEscrowScriptInTx(tx, script, now); err != nil {
				return err
			}
			result.Status = ScriptCloseStatusDeleted
			return nil
		}
		if script.DeletedAt != nil {
			var receipt entity.ScriptEscrowEntry
			err := tx.Where("script_id = ? AND user_id = ? AND operation_hash = ? AND kind = ?",
				script.ID, script.UserID, hashScriptSecret(scriptEscrowRelease), scriptEscrowRelease).Take(&receipt).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptEscrowClosePending
			}
			if err != nil {
				return err
			}
			result = ScriptCloseResult{Status: ScriptCloseStatusRefunded, RefundedMicros: receipt.AmountMicros}
			return nil
		}
		if script.EscrowClosingAt == nil {
			activeID, err := activeScriptCloseSessionInTx(tx, script, now)
			if err != nil {
				return err
			}
			if activeID != 0 && confirmedRunSessionID != activeID {
				result.ActiveRunSessionID = activeID
				return ErrScriptActiveCloseConfirmationRequired
			}
			if err := tx.Model(&script).Updates(map[string]any{
				"escrow_closing_at": now, "enabled": false, "access_key": "", "instruction": "",
			}).Error; err != nil {
				return err
			}
			if err := revokeClosedScriptCredentialsInTx(tx, script, now); err != nil {
				return err
			}
			script.EscrowClosingAt = &now
		}
		hadRun, err := scriptCloseHasRunInTx(tx, script.ID)
		if err != nil {
			return err
		}
		if hadRun {
			// Do not couple Key revocation to a later wallet or billing
			// failure. The worker releases an already closed script in its
			// own transaction after every admitted request is accounted for.
			result = ScriptCloseResult{Status: ScriptCloseStatusSettling, NeedsReconciliation: !script.EscrowWorkTracked}
			return nil
		}
		result, err = finalizeEscrowScriptInTx(tx, script, now)
		return err
	})
	if err != nil && !errors.Is(err, ErrScriptActiveCloseConfirmationRequired) {
		return ScriptCloseResult{}, err
	}
	return result, err
}

func activeScriptCloseSessionInTx(tx *gorm.DB, script entity.UserScript, now time.Time) (int64, error) {
	var sessions []entity.ScriptRunSession
	err := tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("script_id = ? AND user_id = ? AND access_token_id = ? AND revoked_at IS NULL", script.ID, script.UserID, script.AccessTokenID).
		Order("id DESC").Find(&sessions).Error
	if err != nil {
		return 0, err
	}
	for _, session := range sessions {
		if ScriptRunSessionUsableAt(session, now) {
			return session.ID, nil
		}
	}
	return 0, nil
}

func revokeClosedScriptCredentialsInTx(tx *gorm.DB, script entity.UserScript, now time.Time) error {
	if script.AccessTokenID > 0 {
		// Keep the disabled accounting identity, not the plaintext credential.
		// Already admitted charges still reference its ID and quota history.
		if err := tx.Model(&entity.AccessToken{}).
			Where("id = ? AND user_id = ? AND script_id = ?", script.AccessTokenID, script.UserID, script.ID).
			Updates(map[string]any{"status": "disabled", "token_value": "", "token_prefix": ""}).Error; err != nil {
			return err
		}
	}
	return tx.Model(&entity.ScriptRunSession{}).Where("script_id = ? AND revoked_at IS NULL", script.ID).
		Updates(map[string]any{"revoked_at": now, "lease_slot": nil}).Error
}

func deleteNonEscrowScriptInTx(tx *gorm.DB, script entity.UserScript, now time.Time) error {
	if script.DeletedAt != nil {
		return nil
	}
	if script.BillingMode != "" && script.BillingMode != entity.ScriptBillingModeActivation {
		return ErrScriptBillingModeInvalid
	}
	if err := tx.Model(&script).Updates(map[string]any{"deleted_at": now}).Error; err != nil {
		return err
	}
	if err := revokeClosedScriptCredentialsInTx(tx, script, now); err != nil {
		return err
	}
	if script.BillingMode == entity.ScriptBillingModeActivation {
		return nil
	}
	var user entity.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, script.UserID).Error; err != nil {
		return err
	}
	before := util.RoundMoney(user.Balance)
	after := util.RoundMoney(before + script.ReservedFee)
	if err := tx.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", after).Error; err != nil {
		return err
	}
	return tx.Create(&entity.BillingRecord{UserID: user.ID, Type: "script_refund", Provider: "script", Amount: script.ReservedFee, BalanceBefore: before, BalanceAfter: after, RequestID: fmt.Sprintf("script_delete_%d", script.ID), Remark: "删除脚本释放预扣费用"}).Error
}

func finalizeEscrowScriptInTx(tx *gorm.DB, script entity.UserScript, now time.Time) (ScriptCloseResult, error) {
	pending := ScriptCloseResult{Status: ScriptCloseStatusSettling}
	if script.EscrowClosingAt == nil || script.DeletedAt != nil || script.BillingMode != entity.ScriptBillingModeEscrow {
		return pending, ErrScriptEscrowClosePending
	}
	fresh := tx.Session(&gorm.Session{NewDB: true})
	hadRun, err := scriptCloseHasRunInTx(tx, script.ID)
	if err != nil {
		return pending, err
	}
	if hadRun && !script.EscrowWorkTracked {
		// A pre-journal run can have a late provider result that cannot be
		// inferred from lease expiry or an empty work table.
		pending.NeedsReconciliation = true
		return pending, nil
	}
	if script.EscrowWorkTracked {
		// Admission and close serialize on script. An ordinary snapshot read
		// here may only conservatively see a still-pending row; it never waits
		// for a billing transaction that holds a wallet lock.
		var work entity.ScriptEscrowWork
		err := fresh.Select("script_id").Where("script_id = ? AND done_at IS NULL", script.ID).Take(&work).Error
		if err == nil {
			return pending, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return pending, err
		}
	}
	// Tombstone and money movement are atomic. Never let the ordinary delete
	// path credit the legacy fixed reserve for a managed escrow.
	if err := fresh.Model(&entity.UserScript{}).Where("id = ? AND deleted_at IS NULL", script.ID).
		UpdateColumn("deleted_at", now).Error; err != nil {
		return pending, err
	}
	var movement ScriptEscrowMovement
	if hadRun {
		movement, err = releaseTrackedScriptEscrowInTx(tx, script.ID, script.UserID)
	} else {
		movement, err = ReleaseNeverRunScriptEscrowInTx(tx, script.ID, script.UserID)
	}
	if err != nil {
		return pending, err
	}
	return ScriptCloseResult{Status: ScriptCloseStatusRefunded, RefundedMicros: movement.AmountMicros}, nil
}

func scriptCloseHasRunInTx(tx *gorm.DB, scriptID int32) (bool, error) {
	var run entity.ScriptRunSession
	err := tx.Session(&gorm.Session{NewDB: true}).Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("script_id = ?", scriptID).Take(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

// FinalizeClosingScriptEscrowsWithDB is safe to call repeatedly from a periodic
// worker. It selects only bounded candidates, then rechecks each under lock.
func FinalizeClosingScriptEscrowsWithDB(db *gorm.DB, limit int) error {
	if db == nil {
		return ErrScriptEscrowInvalid
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	fresh := db.Session(&gorm.Session{NewDB: true})
	runs := fresh.Model(&entity.ScriptRunSession{}).Select("1").Where("script_id = user_scripts.id")
	work := db.Session(&gorm.Session{NewDB: true}).Model(&entity.ScriptEscrowWork{}).
		Select("1").Where("script_id = user_scripts.id AND done_at IS NULL")
	var ids []int32
	err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.UserScript{}).
		Where("billing_mode = ? AND escrow_closing_at IS NOT NULL AND deleted_at IS NULL", entity.ScriptBillingModeEscrow).
		Where("escrow_work_tracked = ? OR NOT EXISTS (?)", true, runs).
		Where("NOT EXISTS (?)", work).Order("id").Limit(limit).Pluck("id", &ids).Error
	if err != nil {
		return err
	}
	var result error
	for _, id := range ids {
		err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
			var script entity.UserScript
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND escrow_closing_at IS NOT NULL AND deleted_at IS NULL", id).Take(&script).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			_, err = finalizeEscrowScriptInTx(tx, script, time.Now())
			return err
		})
		if err != nil {
			result = errors.Join(result, fmt.Errorf("finalize script %d: %w", id, err))
		}
	}
	return result
}
