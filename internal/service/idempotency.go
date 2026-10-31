package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	IdempotencyStatusProcessing = "processing"
	IdempotencyStatusSucceeded  = "succeeded"
	IdempotencyStatusFailed     = "failed"
	IdempotencyStatusUnknown    = "unknown"
	idempotencyRetention        = 24 * time.Hour
)

var ErrIdempotencyConflict = errors.New("幂等键已用于不同请求")
var ErrIdempotencyKeyInvalid = errors.New("幂等键无效")
var ErrIdempotencyPending = errors.New("请求仍在处理或结果待核对，请勿重复提交")

func hashIdempotencyValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func ClaimIdempotencyWithDB(db *gorm.DB, scope, operation, key string, request []byte) (entity.IdempotencyRecord, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 256 || strings.TrimSpace(scope) == "" || strings.TrimSpace(operation) == "" {
		return entity.IdempotencyRecord{}, false, ErrIdempotencyKeyInvalid
	}
	sum := sha256.Sum256(request)
	requestHash := hex.EncodeToString(sum[:])
	identity := entity.IdempotencyRecord{
		ScopeHash: hashIdempotencyValue(scope), Operation: operation, KeyHash: hashIdempotencyValue(key),
		RequestHash: requestHash, Status: IdempotencyStatusProcessing,
		ExpiresAt: time.Now().Add(idempotencyRetention),
	}
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.IdempotencyRecord{}).
		Where("scope_hash = ? AND operation = ? AND key_hash = ?", identity.ScopeHash, identity.Operation, identity.KeyHash)
	var existing entity.IdempotencyRecord
	if err := query.First(&existing).Error; err == nil {
		if existing.Status != IdempotencyStatusProcessing && existing.Status != IdempotencyStatusUnknown && !existing.ExpiresAt.After(time.Now()) {
			if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ? AND expires_at <= ? AND status IN ?", existing.ID, time.Now(), []string{IdempotencyStatusSucceeded, IdempotencyStatusFailed}).Delete(&entity.IdempotencyRecord{}).Error; err != nil {
				return entity.IdempotencyRecord{}, false, err
			}
		} else {
			if existing.RequestHash != requestHash {
				return entity.IdempotencyRecord{}, false, ErrIdempotencyConflict
			}
			return existing, false, nil
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return entity.IdempotencyRecord{}, false, err
	}
	inserted := db.Session(&gorm.Session{NewDB: true}).Clauses(clause.OnConflict{DoNothing: true}).Create(&identity)
	if inserted.Error != nil {
		return entity.IdempotencyRecord{}, false, inserted.Error
	}
	if inserted.RowsAffected == 1 {
		return identity, true, nil
	}
	if err := query.First(&existing).Error; err != nil {
		return entity.IdempotencyRecord{}, false, err
	}
	if existing.RequestHash != requestHash {
		return entity.IdempotencyRecord{}, false, ErrIdempotencyConflict
	}
	return existing, false, nil
}

func FinishIdempotencyWithDB(db *gorm.DB, id int64, status string, httpStatus int, response []byte) error {
	updated := db.Session(&gorm.Session{NewDB: true}).Model(&entity.IdempotencyRecord{}).
		Where("id = ? AND status IN ?", id, []string{IdempotencyStatusProcessing, IdempotencyStatusUnknown}).
		Updates(map[string]any{"status": status, "http_status": httpStatus, "response_json": string(response), "expires_at": time.Now().Add(idempotencyRetention)})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func CompleteIdempotencyWithDB(db *gorm.DB, id int64, httpStatus int, response []byte) error {
	return FinishIdempotencyWithDB(db, id, IdempotencyStatusSucceeded, httpStatus, response)
}

func MarkIdempotencyUnknownWithDB(db *gorm.DB, id int64) error {
	return db.Session(&gorm.Session{NewDB: true}).Model(&entity.IdempotencyRecord{}).
		Where("id = ? AND status = ?", id, IdempotencyStatusProcessing).
		Update("status", IdempotencyStatusUnknown).Error
}

func ReleaseIdempotencyWithDB(db *gorm.DB, id int64) error {
	return db.Session(&gorm.Session{NewDB: true}).Where("id = ? AND status = ?", id, IdempotencyStatusProcessing).Delete(&entity.IdempotencyRecord{}).Error
}

func SaveIdempotencyProgressWithDB(db *gorm.DB, id int64, response []byte) error {
	return db.Session(&gorm.Session{NewDB: true}).Model(&entity.IdempotencyRecord{}).
		Where("id = ? AND status = ?", id, IdempotencyStatusProcessing).
		Update("response_json", string(response)).Error
}

func CleanupExpiredIdempotencyWithDB(db *gorm.DB, now time.Time) error {
	return db.WithContext(dbqueue.WithPriority(context.Background(), dbqueue.PriorityLow)).Session(&gorm.Session{NewDB: true}).Where("expires_at <= ? AND status IN ?", now, []string{IdempotencyStatusSucceeded, IdempotencyStatusFailed}).Delete(&entity.IdempotencyRecord{}).Error
}

func StartIdempotencyCleanup(db *gorm.DB, interval time.Duration) func() {
	if interval <= 0 {
		interval = time.Hour
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				if err := CleanupExpiredIdempotencyWithDB(db, now); err != nil {
					logger.SysWarn("清理过期幂等记录失败：" + err.Error())
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}
