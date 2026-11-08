package service

import (
	"errors"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrScriptEscrowWorkDenied    = errors.New("脚本已停止接收新请求")
	ErrScriptEscrowWorkDuplicate = errors.New("请求已经受理，不再重复执行")
	ErrScriptEscrowWorkMissing   = errors.New("缺少已受理的脚本结算记录")
)

func validScriptWorkRequest(scriptID int32, requestID string) bool {
	return scriptID > 0 && requestID != "" && len(requestID) <= 64 && requestID == strings.TrimSpace(requestID)
}

// BeginScriptEscrowWorkWithDB serializes admission with closing the same
// script. The caller must already have validated the run's proof and source.
// Rechecking the exact session prevents a replacement lease from admitting
// an old request. Do not invoke any provider unless this transaction commits.
func BeginScriptEscrowWorkWithDB(db *gorm.DB, scriptID int32, requestID string, sessionID int64) error {
	if db == nil || sessionID <= 0 || !validScriptWorkRequest(scriptID, requestID) {
		return ErrScriptEscrowWorkDenied
	}
	return db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var script entity.UserScript
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND billing_mode = ? AND enabled = ? AND deleted_at IS NULL AND escrow_closing_at IS NULL",
				scriptID, entity.ScriptBillingModeEscrow, true).First(&script).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptEscrowWorkDenied
			}
			return err
		}
		var session entity.ScriptRunSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND script_id = ? AND user_id = ? AND access_token_id = ? AND lease_slot = ?",
				sessionID, scriptID, script.UserID, script.AccessTokenID, scriptID).First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptEscrowWorkDenied
			}
			return err
		}
		now := time.Now()
		if session.SourceZoneHash == "" || !ScriptRunSessionUsableAt(session, now) {
			return ErrScriptEscrowWorkDenied
		}
		var previous entity.ScriptEscrowWork
		err := tx.Where("script_id = ? AND request_id = ?", scriptID, requestID).Take(&previous).Error
		if err == nil {
			return ErrScriptEscrowWorkDuplicate
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&entity.ScriptEscrowWork{
			ScriptID: scriptID, RequestID: requestID, SessionID: sessionID, CreatedAt: now,
		}).Error
	})
}

// CompleteScriptEscrowWorkInTx is used by the same transaction that records a
// charge. It deliberately does not lock the script: the close worker acquires
// the script first and will only release after this outcome is committed.
func CompleteScriptEscrowWorkInTx(tx *gorm.DB, scriptID int32, requestID string) error {
	if tx == nil || !validScriptWorkRequest(scriptID, requestID) {
		return ErrScriptEscrowWorkMissing
	}
	result := tx.Model(&entity.ScriptEscrowWork{}).
		Where("script_id = ? AND request_id = ? AND done_at IS NULL", scriptID, requestID).
		UpdateColumn("done_at", time.Now())
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var work entity.ScriptEscrowWork
	if err := tx.Where("script_id = ? AND request_id = ?", scriptID, requestID).Take(&work).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrScriptEscrowWorkMissing
		}
		return err
	}
	if work.DoneAt == nil {
		return ErrScriptEscrowWorkMissing
	}
	return nil
}

// CompleteScriptEscrowWorkWithDB is reserved for known no-charge outcomes.
func CompleteScriptEscrowWorkWithDB(db *gorm.DB, scriptID int32, requestID string) error {
	if db == nil {
		return ErrScriptEscrowWorkMissing
	}
	return db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		return CompleteScriptEscrowWorkInTx(tx, scriptID, requestID)
	})
}
