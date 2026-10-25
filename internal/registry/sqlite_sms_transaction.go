package registry

import (
	"errors"
	"fmt"
	"gorm.io/gorm"
	"sync/atomic"
)

// Only the SQLite SMS compatibility rebuild/index belongs to this boundary.
// A caller-owned transaction gets an owned savepoint, never a Commit/Rollback.
var smsMigrationSavepointID uint64

type smsMigrationPanic struct {
	value       any
	rollbackErr error
}

func (p *smsMigrationPanic) Error() string {
	return fmt.Sprintf("SQLite SMS migration panic: %v; rollback: %v", p.value, p.rollbackErr)
}
func (p *smsMigrationPanic) OriginalPanic() any { return p.value }
func (p *smsMigrationPanic) Unwrap() []error {
	causes := []error{p.rollbackErr}
	if original, ok := p.value.(error); ok {
		causes = append([]error{original}, causes...)
	}
	return causes
}

func withSQLiteSMSMigrationBoundary(db *gorm.DB, apply func(*gorm.DB) error) (err error) {
	// Use the current statement pool, including PreparedStmtTX, not the base Config pool.
	work := db.Session(&gorm.Session{NewDB: true})
	var finish, rollback func() error
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		name := fmt.Sprintf("tiku_sms_schema_%d", atomic.AddUint64(&smsMigrationSavepointID, 1))
		if err := work.Exec("SAVEPOINT " + name).Error; err != nil {
			return fmt.Errorf("begin SQLite SMS savepoint: %w", err)
		}
		finish = func() error { return work.Exec("RELEASE " + name).Error }
		rollback = func() error {
			if err := work.Exec("ROLLBACK TO " + name).Error; err != nil {
				return err // retain the recovery boundary if rollback-to itself failed
			}
			return work.Exec("RELEASE " + name).Error
		}
	} else {
		work = work.Begin()
		if work.Error != nil {
			return fmt.Errorf("begin SQLite SMS migration: %w", work.Error)
		}
		committer, ok := work.Statement.ConnPool.(gorm.TxCommitter)
		if !ok {
			return gorm.ErrInvalidTransaction
		}
		// Direct calls keep each independent error; a failed Commit must not pollute Rollback.Error.
		finish, rollback = committer.Commit, committer.Rollback
	}
	defer func() {
		if value := recover(); value != nil {
			if rollbackErr := rollback(); rollbackErr != nil {
				panic(&smsMigrationPanic{value: value, rollbackErr: rollbackErr})
			}
			panic(value)
		}
	}()
	if err = apply(work); err != nil {
		return errors.Join(err, rollback())
	}
	if err = finish(); err != nil {
		// An acknowledgement error after actual commit cannot be claimed recovered: ErrTxDone is retained.
		return errors.Join(err, rollback())
	}
	return nil
}
