package registry

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const billingLookupIndexName = "idx_billing_records_user_request_type_id"
const billingLookupIndexDDL = "CREATE INDEX IF NOT EXISTS idx_billing_records_user_request_type_id ON billing_records(user_id,request_id,type,id)"

type billingLookupIndexShapeMismatchError struct{}

func (billingLookupIndexShapeMismatchError) Error() string {
	return "billing_index_startup class=shape_mismatch index=" + billingLookupIndexName
}

// Error() never renders the cause; errors.Is/As still retain original SQL,
// commit and independent rollback errors through Unwrap.
type billingLookupIndexOperationError struct {
	class string
	cause error
}

func (e *billingLookupIndexOperationError) Error() string {
	return "billing_index_startup class=" + e.class + " index=" + billingLookupIndexName
}
func (e *billingLookupIndexOperationError) Unwrap() error { return e.cause }
func billingLookupIndexFailure(class string, cause error) error {
	return &billingLookupIndexOperationError{class: class, cause: cause}
}

type billingLookupIndexState uint8

const (
	billingLookupIndexUnknown billingLookupIndexState = iota
	billingLookupIndexAbsent
	billingLookupIndexExact
	billingLookupIndexMismatch
)

type billingLookupIndexSnapshot struct {
	state      billingLookupIndexState
	name       string
	definition string
}

func billingLookupIndexASCIIEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
func billingLookupIndexQuiet(database *gorm.DB) *gorm.DB {
	return database.Session(&gorm.Session{Logger: database.Logger.LogMode(gormlogger.Silent)})
}

// Names are compared using SQLite's ASCII logical-name semantics, not Unicode
// normalization. Table/column names, collation and key order are not generalized.
func readSQLiteBillingLookupIndex(database *gorm.DB) (billingLookupIndexSnapshot, error) {
	var owners []struct {
		Name  string
		Table string `gorm:"column:tbl_name"`
		SQL   *string
	}
	if err := database.Raw("SELECT name,tbl_name,sql FROM main.sqlite_master WHERE type='index' AND name=? COLLATE NOCASE", billingLookupIndexName).Scan(&owners).Error; err != nil {
		return billingLookupIndexSnapshot{}, err
	}
	if len(owners) == 0 {
		return billingLookupIndexSnapshot{state: billingLookupIndexAbsent}, nil
	}
	if len(owners) != 1 {
		return billingLookupIndexSnapshot{}, errors.New("ambiguous index metadata")
	}
	owner := owners[0]
	snapshot := billingLookupIndexSnapshot{state: billingLookupIndexMismatch, name: owner.Name}
	if !billingLookupIndexASCIIEqual(owner.Name, billingLookupIndexName) || owner.Table != "billing_records" || owner.SQL == nil {
		return snapshot, nil
	}
	snapshot.definition = *owner.SQL
	var indexes []struct {
		Name    string
		Unique  int `gorm:"column:unique"`
		Origin  string
		Partial int
	}
	if err := database.Raw("PRAGMA main.index_list('billing_records')").Scan(&indexes).Error; err != nil {
		return billingLookupIndexSnapshot{}, err
	}
	found := false
	for _, index := range indexes {
		if !billingLookupIndexASCIIEqual(index.Name, billingLookupIndexName) {
			continue
		}
		if found {
			return billingLookupIndexSnapshot{}, errors.New("ambiguous index-list metadata")
		}
		found = true
		if index.Unique != 0 || index.Partial != 0 || index.Origin != "c" {
			return snapshot, nil
		}
	}
	if !found {
		return billingLookupIndexSnapshot{}, errors.New("index disappeared during inspection")
	}
	var fields []struct {
		Seqno int
		Cid   int
		Name  *string
		Desc  int
		Coll  string
		Key   int
	}
	if err := database.Raw("PRAGMA main.index_xinfo('idx_billing_records_user_request_type_id')").Scan(&fields).Error; err != nil {
		return billingLookupIndexSnapshot{}, err
	}
	want := []string{"user_id", "request_id", "type", "id"}
	keys := 0
	for _, field := range fields {
		if field.Key == 0 {
			continue
		}
		if field.Key != 1 || keys >= len(want) || field.Seqno != keys || field.Cid < 0 || field.Name == nil || *field.Name != want[keys] || field.Desc != 0 || field.Coll != "BINARY" {
			return snapshot, nil
		}
		keys++
	}
	if keys == 0 {
		return billingLookupIndexSnapshot{}, errors.New("empty index-key metadata")
	}
	if keys != len(want) {
		return snapshot, nil
	}
	snapshot.state = billingLookupIndexExact
	return snapshot, nil
}
func checkedSQLiteBillingLookupIndex(database *gorm.DB) (billingLookupIndexSnapshot, error) {
	snapshot, err := readSQLiteBillingLookupIndex(database)
	if err != nil || snapshot.state == billingLookupIndexUnknown {
		return snapshot, billingLookupIndexFailure("metadata_unknown", err)
	}
	if snapshot.state == billingLookupIndexMismatch {
		return snapshot, billingLookupIndexShapeMismatchError{}
	}
	return snapshot, nil
}

// Read-only startup gate runs before the first AutoMigrate. No unknown metadata
// or confirmed conflicting definition is allowed to be erased by migrations.
func validateExistingSQLiteBillingLookupIndex(database *gorm.DB) error {
	if database == nil || database.Dialector == nil {
		return billingLookupIndexFailure("metadata_unknown", gorm.ErrInvalidDB)
	}
	if database.Dialector.Name() != "sqlite" {
		return nil
	}
	_, err := checkedSQLiteBillingLookupIndex(billingLookupIndexQuiet(database))
	return err
}
func warnSQLiteBillingLookupIndex(class string) {
	logger.SysWarn("billing_index_startup class=" + class + " index=" + billingLookupIndexName)
}
func ensureSQLiteBillingLookupIndex(database *gorm.DB) error {
	if database == nil || database.Dialector == nil {
		return billingLookupIndexFailure("metadata_unknown", gorm.ErrInvalidDB)
	}
	if database.Dialector.Name() != "sqlite" {
		return nil
	}
	quiet := billingLookupIndexQuiet(database)
	before, err := checkedSQLiteBillingLookupIndex(quiet)
	if err != nil {
		return err
	}
	if before.state == billingLookupIndexExact {
		return nil
	}
	createErr := quiet.Exec(billingLookupIndexDDL).Error
	// A correct concurrent winner succeeds, a conflicting winner stops, and
	// unknown metadata stops. Only confirmed absence can warn and degrade.
	after, err := checkedSQLiteBillingLookupIndex(quiet)
	if err != nil {
		return err
	}
	if after.state == billingLookupIndexExact {
		return nil
	}
	if createErr != nil {
		warnSQLiteBillingLookupIndex("create_unavailable")
	} else {
		warnSQLiteBillingLookupIndex("postcreate_unavailable")
	}
	return nil
}

// Keep MySQL's original AutoMigrate model sequence untouched. SQLite alone
// isolates BillingRecord so its table rebuild and index restoration share one
// explicitly owned transaction/savepoint. No migration or field is skipped.
func autoMigrateAccountDomainWithBillingLookupIndex(database *gorm.DB, models []any) error {
	if database.Dialector.Name() != "sqlite" {
		return database.AutoMigrate(models...)
	}
	at := -1
	for i, model := range models {
		if _, ok := model.(*entity.BillingRecord); ok {
			if at >= 0 {
				return billingLookupIndexFailure("migration_model_scope", nil)
			}
			at = i
		}
	}
	if at < 0 {
		return billingLookupIndexFailure("migration_model_scope", nil)
	}
	if at > 0 {
		if err := database.AutoMigrate(models[:at]...); err != nil {
			return err
		}
	}
	if err := migrateSQLiteBillingRecordPreservingLookupIndex(database); err != nil {
		return err
	}
	if at+1 < len(models) {
		return database.AutoMigrate(models[at+1:]...)
	}
	return nil
}
func migrateSQLiteBillingRecordPreservingLookupIndex(database *gorm.DB) error {
	quiet := billingLookupIndexQuiet(database)
	err := withSQLiteBillingIndexMigrationBoundary(quiet, func(tx *gorm.DB) error {
		before, err := checkedSQLiteBillingLookupIndex(tx)
		if err != nil {
			return err
		}
		if err := tx.AutoMigrate(&entity.BillingRecord{}); err != nil {
			return billingLookupIndexFailure("billing_migration_failed", err)
		}
		after, err := checkedSQLiteBillingLookupIndex(tx)
		if err != nil {
			return err
		}
		if before.state != billingLookupIndexExact {
			return nil
		}
		if after.state == billingLookupIndexAbsent {
			// Only a definition already verified correct before this migration is
			// restored. No DROP/replace of a conflicting index is ever attempted.
			if err := tx.Exec(before.definition).Error; err != nil {
				return billingLookupIndexFailure("restore_failed", err)
			}
			after, err = checkedSQLiteBillingLookupIndex(tx)
			if err != nil {
				return err
			}
		}
		if after.state != billingLookupIndexExact || after.name != before.name || after.definition != before.definition {
			return billingLookupIndexFailure("restore_verification_failed", nil)
		}
		return nil
	})
	if err == nil {
		return nil
	}
	// Join can contain an independent rollback error. Preserve the full chain but
	// never let fmt/FatalLog render raw SQL/DSN through a joined error's Error().
	class := "migration_transaction_failed"
	var shape billingLookupIndexShapeMismatchError
	var operation *billingLookupIndexOperationError
	if errors.As(err, &shape) {
		class = "shape_mismatch"
	} else if errors.As(err, &operation) {
		class = operation.class
	}
	return billingLookupIndexFailure(class, err)
}

var billingIndexMigrationSavepointID uint64

type billingIndexMigrationPanic struct {
	value       any
	rollbackErr error
}

func (p *billingIndexMigrationPanic) Error() string {
	return "billing_index_startup class=panic_rollback_failed index=" + billingLookupIndexName
}
func (p *billingIndexMigrationPanic) OriginalPanic() any { return p.value }
func (p *billingIndexMigrationPanic) Unwrap() []error {
	causes := []error{p.rollbackErr}
	if original, ok := p.value.(error); ok {
		causes = append([]error{original}, causes...)
	}
	return causes
}
func withSQLiteBillingIndexMigrationBoundary(database *gorm.DB, apply func(*gorm.DB) error) (err error) {
	// Same ownership rules as qualified D01: inspect Statement.ConnPool (including
	// PreparedStmtTX); never Commit/Rollback a caller-owned outer transaction.
	work := database.Session(&gorm.Session{NewDB: true})
	var finish, rollback func() error
	if _, nested := database.Statement.ConnPool.(gorm.TxCommitter); nested {
		name := fmt.Sprintf("tiku_billing_index_schema_%d", atomic.AddUint64(&billingIndexMigrationSavepointID, 1))
		if err := work.Exec("SAVEPOINT " + name).Error; err != nil {
			return billingLookupIndexFailure("savepoint_begin_failed", err)
		}
		finish = func() error { return work.Exec("RELEASE " + name).Error }
		rollback = func() error {
			if err := work.Exec("ROLLBACK TO " + name).Error; err != nil {
				return err
			}
			return work.Exec("RELEASE " + name).Error
		}
	} else {
		work = work.Begin()
		if work.Error != nil {
			return billingLookupIndexFailure("transaction_begin_failed", work.Error)
		}
		committer, ok := work.Statement.ConnPool.(gorm.TxCommitter)
		if !ok {
			return gorm.ErrInvalidTransaction
		}
		finish, rollback = committer.Commit, committer.Rollback
	}
	defer func() {
		if value := recover(); value != nil {
			if rollbackErr := rollback(); rollbackErr != nil {
				panic(&billingIndexMigrationPanic{value: value, rollbackErr: rollbackErr})
			}
			panic(value)
		}
	}()
	if err = apply(work); err != nil {
		return errors.Join(err, rollback())
	}
	if err = finish(); err != nil {
		return errors.Join(err, rollback())
	}
	return nil
}
