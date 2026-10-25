package registry

import (
	"database/sql"
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Current native entity producer, not a legacy-model migration; only remove its scene index as fixture setup.
func d01NewIndexOnlyFixture(t *testing.T, prepared bool) d01StageFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "synthetic-current-scene-no-index.sqlite")), &gorm.Config{PrepareStmt: prepared, Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE current native DB open: %v", err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE current raw DB: %v", err)
	}
	raw.SetMaxOpenConns(1)
	raw.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Errorf("D01_STAGE_GATE_INFRA_FAILURE current DB close: %v", err)
		}
	})
	if err = db.Exec("PRAGMA journal_mode=DELETE").Error; err != nil {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE journal mode: %v", err)
	}
	if err = db.AutoMigrate(&entity.SMSVerificationCode{}); err != nil {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE current native entity producer: %v", err)
	}
	fixed := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	consumed := fixed.Add(time.Minute)
	row := entity.SMSVerificationCode{ID: int32(d01SyntheticID), Phone: "D01_SYNTHETIC_PHONE", Scene: "D01_EXISTING_SCENE", CodeHash: strings.Repeat("d", 64), Status: "consumed", ExpiresAt: fixed.Add(time.Hour), ConsumedAt: &consumed, RequestIP: "192.0.2.1", CreatedAt: fixed, UpdatedAt: fixed.Add(2 * time.Minute)}
	if err = db.Create(&row).Error; err != nil {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE current synthetic row: %v", err)
	}
	if !db.Migrator().HasColumn("sms_verification_codes", "scene") || !db.Migrator().HasIndex("sms_verification_codes", "idx_sms_verification_codes_scene") {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE native current entity shape")
	}
	if err = db.Exec("DROP INDEX idx_sms_verification_codes_scene").Error; err != nil {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE fixture index removal only: %v", err)
	}
	if !db.Migrator().HasColumn("sms_verification_codes", "scene") || db.Migrator().HasIndex("sms_verification_codes", "idx_sms_verification_codes_scene") {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE exact scene-present index-missing fixture")
	}
	before := d01View(db)
	if !before.RowPresent || before.PrimaryCount != 1 || len(before.Row) != 9 || before.LegacyTablePresent || before.QuickCheck != "ok" || len(before.Errors) != 0 {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE full current baseline invalid")
	}
	return d01StageFixture{db: db, raw: raw, before: before}
}
func d01CheckIndexOnlyPreserved(t *testing.T, db *gorm.DB, before d01SMSView, wantIndex bool) {
	t.Helper()
	d01TxAssert(t, reflect.DeepEqual(d01View(db), before), "index-only path changed original schema or one of nine original fields")
	var scene string
	err := db.Raw("SELECT scene FROM sms_verification_codes WHERE id=?", d01SyntheticID).Row().Scan(&scene)
	d01TxAssert(t, err == nil && scene == "D01_EXISTING_SCENE", "index-only path changed existing scene value")
	d01TxAssert(t, db.Migrator().HasIndex("sms_verification_codes", "idx_sms_verification_codes_scene") == wantIndex, "index-only index presence differs from expected stage")
}
func d01RetryIndexOnlyAndNoop(t *testing.T, f d01StageFixture, p *d01TxProbePool) {
	t.Helper()
	if err := ensureSMSVerificationSchemaForDriver(f.db, "sqlite"); err != nil {
		t.Errorf("D01_TRANSACTION_ASSERTION_RED index-only retry failed: %v", err)
		return
	}
	d01CheckIndexOnlyPreserved(t, f.db, f.before, true)
	began, committed := p.beginCalls, p.commitCalls
	if err := ensureSMSVerificationSchemaForDriver(f.db, "sqlite"); err != nil {
		t.Errorf("D01_TRANSACTION_ASSERTION_RED index-only completed no-op failed: %v", err)
		return
	}
	d01CheckIndexOnlyPreserved(t, f.db, f.before, true)
	d01TxAssert(t, p.beginCalls == began && p.commitCalls == committed, "completed scene+index no-op opened or committed a transaction")
}
func TestD01SMSIndexOnlyTransactionRegression(t *testing.T) {
	t.Run("begin-before-index", func(t *testing.T) {
		f := d01NewIndexOnlyFixture(t, false)
		p := d01InstallProbe(t, f)
		problem := errors.New("D01_INDEX_ONLY_BEGIN_BEFORE_DELEGATE")
		p.beginError = problem
		err := ensureSMSVerificationSchemaForDriver(f.db, "sqlite")
		p.cleanupOwned(t)
		d01TxAssert(t, p.beginCalls == 1 && errors.Is(err, problem), "index-only Begin fault was bypassed or swallowed")
		d01CheckIndexOnlyPreserved(t, f.db, f.before, false)
		p.beginError = nil
		d01RetryIndexOnlyAndNoop(t, f, p)
	})
	t.Run("index-SQL-error", func(t *testing.T) {
		f := d01NewIndexOnlyFixture(t, false)
		p := d01InstallProbe(t, f)
		problem := errors.New("D01_INDEX_ONLY_CREATE_INDEX_ERROR")
		hits := d01InjectRaw(t, f.db, "CREATE INDEX idx_sms_verification_codes_scene", problem, nil)
		err := ensureSMSVerificationSchemaForDriver(f.db, "sqlite")
		f.db.Callback().Raw().Remove("d01:stage-fault")
		p.cleanupOwned(t)
		if *hits != 1 {
			t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE index-only fault missed")
		}
		d01TxAssert(t, p.beginCalls == 1 && p.rollbackCalls == 1 && p.commitCalls == 0 && errors.Is(err, problem), "index-only SQL error did not preserve its own transaction and original error")
		d01CheckIndexOnlyPreserved(t, f.db, f.before, false)
		d01RetryIndexOnlyAndNoop(t, f, p)
	})
	t.Run("rollback-before-delegate-chain", func(t *testing.T) {
		f := d01NewIndexOnlyFixture(t, false)
		p := d01InstallProbe(t, f)
		problem := errors.New("D01_INDEX_ONLY_SQL_ERROR")
		rollbackProblem := errors.New("D01_INDEX_ONLY_ROLLBACK_BEFORE_DELEGATE")
		p.rollbackBeforeError = rollbackProblem
		hits := d01InjectRaw(t, f.db, "CREATE INDEX idx_sms_verification_codes_scene", problem, nil)
		err := ensureSMSVerificationSchemaForDriver(f.db, "sqlite")
		f.db.Callback().Raw().Remove("d01:stage-fault")
		p.cleanupOwned(t) // explicit test-owned realTx cleanup before any single-connection query; not product restoration evidence
		if *hits != 1 {
			t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE index-only rollback-chain fault missed")
		}
		d01TxAssert(t, p.beginCalls == 1 && p.rollbackCalls == 1 && errors.Is(err, problem) && errors.Is(err, rollbackProblem), "index-only original SQL and independent rollback error chains not both observable")
		t.Log("D01_FAULT_TIMING index-only rollback fault before delegate; subsequent cleanup belongs to test owner, no restoration/retry claim")
	})
	for _, prepared := range []bool{false, true} {
		name := "nested-plain"
		if prepared {
			name = "nested-prepared"
		}
		t.Run(name, func(t *testing.T) {
			f := d01NewIndexOnlyFixture(t, prepared)
			outer := f.db.Begin()
			if outer.Error != nil {
				t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE index-only outer begin: %v", outer.Error)
			}
			t.Cleanup(func() {
				if err := outer.Rollback().Error; err != nil && !errors.Is(err, sql.ErrTxDone) {
					t.Errorf("D01_STAGE_GATE_INFRA_FAILURE index-only outer cleanup: %v", err)
				}
			})
			if err := outer.Exec("CREATE TABLE d01_index_outer_marker (id integer PRIMARY KEY,value text)").Error; err != nil {
				t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE index-only caller marker: %v", err)
			}
			if err := outer.Exec("INSERT INTO d01_index_outer_marker(id,value) VALUES(?,?)", 1, "before").Error; err != nil {
				t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE index-only caller before: %v", err)
			}
			saved, rolled, released := 0, 0, 0
			savedOK, rolledOK, releasedOK := 0, 0, 0
			var boundaryFailures []string
			if err := outer.Callback().Raw().Before("gorm:raw").Register("d01:index-savepoint-observer", func(tx *gorm.DB) {
				sql := strings.TrimSpace(tx.Statement.SQL.String())
				if strings.HasPrefix(sql, "SAVEPOINT ") {
					saved++
				}
				if strings.HasPrefix(sql, "ROLLBACK TO ") {
					rolled++
				}
				if strings.HasPrefix(sql, "RELEASE ") {
					released++
				}
			}); err != nil {
				t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE savepoint observer: %v", err)
			}
			if err := outer.Callback().Raw().After("gorm:raw").Register("d01:index-savepoint-outcome", func(tx *gorm.DB) {
				sql := strings.TrimSpace(tx.Statement.SQL.String())
				boundary := strings.HasPrefix(sql, "SAVEPOINT ") || strings.HasPrefix(sql, "ROLLBACK TO ") || strings.HasPrefix(sql, "RELEASE ")
				if !boundary {
					return
				}
				if tx.DryRun {
					boundaryFailures = append(boundaryFailures, sql+": DRY_RUN_NOT_REAL_SQL")
					return
				}
				if tx.Error != nil {
					boundaryFailures = append(boundaryFailures, sql+": "+tx.Error.Error())
					return
				}
				if strings.HasPrefix(sql, "SAVEPOINT ") {
					savedOK++
				}
				if strings.HasPrefix(sql, "ROLLBACK TO ") {
					rolledOK++
				}
				if strings.HasPrefix(sql, "RELEASE ") {
					releasedOK++
				}
			}); err != nil {
				t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE savepoint outcome observer: %v", err)
			}
			problem := errors.New("D01_INDEX_ONLY_NESTED_SQL_ERROR")
			hits := d01InjectRaw(t, outer, "CREATE INDEX idx_sms_verification_codes_scene", problem, nil)
			err := ensureSMSVerificationSchemaForDriver(outer, "sqlite")
			outer.Callback().Raw().Remove("d01:stage-fault")
			outer.Callback().Raw().Remove("d01:index-savepoint-observer")
			outer.Callback().Raw().Remove("d01:index-savepoint-outcome")
			if *hits != 1 {
				t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE nested index-only fault missed")
			}
			d01TxAssert(t, errors.Is(err, problem) && saved == 1 && rolled == 1 && released == 1 && savedOK == 1 && rolledOK == 1 && releasedOK == 1 && len(boundaryFailures) == 0, "index-only nested SQL error lacks successful own savepoint/rollback-to/release boundary")
			if len(boundaryFailures) != 0 {
				t.Logf("D01_INDEX_BOUNDARY_SQL_ERRORS %v", boundaryFailures)
			}
			d01CheckIndexOnlyPreserved(t, outer, f.before, false)
			d01TxAssert(t, outer.Exec("INSERT INTO d01_index_outer_marker(id,value) VALUES(?,?)", 2, "after").Error == nil, "index-only migration consumed caller transaction")
			d01TxAssert(t, outer.Rollback().Error == nil, "index-only caller lost rollback ownership")
			d01CheckIndexOnlyPreserved(t, f.db, f.before, false)
			d01TxAssert(t, !f.db.Migrator().HasTable("d01_index_outer_marker"), "caller rollback left its marker table")
		})
	}
}
