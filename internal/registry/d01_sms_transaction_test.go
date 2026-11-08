package registry

import (
	"context"
	"database/sql"
	"errors"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// All proxies delegate to a real, test-owned SQLite connection. No helper API is referenced.
type d01TxProbePool struct {
	*sql.DB
	beginError, commitBeforeError, commitAfterError, rollbackBeforeError error
	beginCalls, commitCalls, rollbackCalls                               int
	transactions                                                         []*d01RealTxProbe
}
type d01RealTxProbe struct {
	*sql.Tx
	owner *d01TxProbePool
}

func (p *d01TxProbePool) GetDBConn() (*sql.DB, error) { return p.DB, nil }
func (p *d01TxProbePool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	p.beginCalls++
	if p.beginError != nil {
		return nil, p.beginError
	}
	real, err := p.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	tx := &d01RealTxProbe{Tx: real, owner: p}
	p.transactions = append(p.transactions, tx)
	return tx, nil
}
func (p *d01RealTxProbe) GetDBConn() (*sql.DB, error) { return p.owner.DB, nil }
func (p *d01RealTxProbe) Commit() error {
	p.owner.commitCalls++
	if p.owner.commitBeforeError != nil {
		return p.owner.commitBeforeError
	} // before delegate; real tx remains open
	err := p.Tx.Commit()
	if err != nil {
		return err
	}
	return p.owner.commitAfterError // synthetic ack fault AFTER actual successful commit; NOT rollback-recoverable
}
func (p *d01RealTxProbe) Rollback() error {
	p.owner.rollbackCalls++
	if p.owner.rollbackBeforeError != nil {
		return p.owner.rollbackBeforeError
	} // real tx remains open until explicit test-owned cleanup
	return p.Tx.Rollback()
}
func (p *d01TxProbePool) cleanupOwned(t *testing.T) {
	t.Helper()
	for _, tx := range p.transactions {
		if err := tx.Tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("D01_STAGE_GATE_INFRA_FAILURE owned realTx cleanup: %v", err)
		}
	}
}

type d01StageFixture struct {
	db     *gorm.DB
	raw    *sql.DB
	before d01SMSView
}

func d01NewStageFixture(t *testing.T, prepared bool) d01StageFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic-native-old-sms.sqlite")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{PrepareStmt: prepared, Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE open: %v", err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE raw handle: %v", err)
	}
	raw.SetMaxOpenConns(1)
	raw.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Errorf("D01_STAGE_GATE_INFRA_FAILURE owned DB close: %v", err)
		}
	})
	if err = db.Exec("PRAGMA journal_mode=DELETE").Error; err != nil {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE journal: %v", err)
	}
	if err = db.AutoMigrate(&legacySMSVerificationCodeWithoutScene{}); err != nil {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE old model: %v", err)
	}
	fixed := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	consumed := fixed.Add(time.Minute)
	row := legacySMSVerificationCodeWithoutScene{ID: int32(d01SyntheticID), Phone: "D01_SYNTHETIC_PHONE", CodeHash: strings.Repeat("d", 64), Status: "consumed", ExpiresAt: fixed.Add(time.Hour), ConsumedAt: &consumed, RequestIP: "192.0.2.1", CreatedAt: fixed, UpdatedAt: fixed.Add(2 * time.Minute)}
	if err = db.Create(&row).Error; err != nil {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE fixture row: %v", err)
	}
	before := d01View(db)
	if !before.RowPresent || before.PrimaryCount != 1 || len(before.Row) != 9 || before.LegacyTablePresent || before.QuickCheck != "ok" || len(before.Errors) != 0 {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE baseline invalid: %+v", before)
	}
	return d01StageFixture{db: db, raw: raw, before: before}
}
func d01InstallProbe(t *testing.T, f d01StageFixture) *d01TxProbePool {
	t.Helper()
	p := &d01TxProbePool{DB: f.raw}
	f.db.ConnPool = p
	f.db.Statement.ConnPool = p
	t.Cleanup(func() { p.cleanupOwned(t) })
	return p
}
func d01TxAssert(t *testing.T, ok bool, message string) {
	t.Helper()
	if !ok {
		t.Errorf("D01_TRANSACTION_ASSERTION_RED %s", message)
	}
}
func d01InjectRaw(t *testing.T, db *gorm.DB, prefix string, problem error, panicValue any) *int {
	t.Helper()
	hits := new(int)
	if err := db.Callback().Raw().Before("gorm:raw").Register("d01:stage-fault", func(tx *gorm.DB) {
		if strings.HasPrefix(strings.TrimSpace(tx.Statement.SQL.String()), prefix) {
			*hits++
			if panicValue != nil {
				panic(panicValue)
			}
			tx.AddError(problem)
		}
	}); err != nil {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE callback install: %v", err)
	}
	t.Cleanup(func() { db.Callback().Raw().Remove("d01:stage-fault") })
	return hits
}
func d01CheckMigrated(t *testing.T, f d01StageFixture) {
	t.Helper()
	after := d01View(f.db)
	d01TxAssert(t, reflect.DeepEqual(after.Row, f.before.Row) && after.PrimaryCount == 1 && !after.LegacyTablePresent && after.QuickCheck == "ok" && len(after.Errors) == 0, "successful retry lost one of original nine fields or left legacy")
	d01TxAssert(t, f.db.Migrator().HasColumn("sms_verification_codes", "scene") && f.db.Migrator().HasIndex("sms_verification_codes", "idx_sms_verification_codes_scene"), "successful retry missing scene or original scene index")
}

func TestD01SMSPhaseFaultsRestoreAndRetry(t *testing.T) {
	phases := []struct{ name, prefix string }{{"rename", "ALTER TABLE sms_verification_codes RENAME TO"}, {"create", "CREATE TABLE sms_verification_codes ("}, {"copy", "INSERT INTO sms_verification_codes (`id`,`phone`,`scene`"}, {"drop", "DROP TABLE sms_verification_codes__legacy_scene"}, {"index", "CREATE INDEX idx_sms_verification_codes_scene"}}
	for _, stage := range phases {
		t.Run(stage.name, func(t *testing.T) {
			f := d01NewStageFixture(t, false)
			problem := errors.New("D01_SYNTHETIC_" + stage.name + "_FAULT")
			hits := d01InjectRaw(t, f.db, stage.prefix, problem, nil)
			err := ensureSMSVerificationSchemaForDriver(f.db, "sqlite")
			f.db.Callback().Raw().Remove("d01:stage-fault")
			if *hits != 1 {
				t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE fault hook missed or repeated: %d", *hits)
			}
			d01TxAssert(t, errors.Is(err, problem), "original stage SQL error chain lost")
			d01TxAssert(t, reflect.DeepEqual(d01View(f.db), f.before), "stage failure failed full original schema/nine-field/legacy restoration")
			if err = ensureSMSVerificationSchemaForDriver(f.db, "sqlite"); err != nil {
				t.Errorf("D01_TRANSACTION_ASSERTION_RED retry after stage failure failed: %v", err)
				return
			}
			d01CheckMigrated(t, f)
			after := d01View(f.db)
			if err = ensureSMSVerificationSchemaForDriver(f.db, "sqlite"); err != nil {
				t.Errorf("D01_TRANSACTION_ASSERTION_RED completed migration not idempotent: %v", err)
				return
			}
			d01TxAssert(t, reflect.DeepEqual(d01View(f.db), after), "idempotent retry changed schema or row")
		})
	}
}
func TestD01SMSTransactionBeginFailure(t *testing.T) {
	f := d01NewStageFixture(t, false)
	p := d01InstallProbe(t, f)
	problem := errors.New("D01_SYNTHETIC_BEGIN_FAULT")
	p.beginError = problem
	err := ensureSMSVerificationSchemaForDriver(f.db, "sqlite")
	p.cleanupOwned(t)
	d01TxAssert(t, p.beginCalls == 1 && errors.Is(err, problem), "begin fault was not returned before writes")
	d01TxAssert(t, reflect.DeepEqual(d01View(f.db), f.before), "begin failure changed original schema or row")
}
func TestD01SMSTransactionPreCommitFailure(t *testing.T) {
	f := d01NewStageFixture(t, false)
	p := d01InstallProbe(t, f)
	problem := errors.New("D01_SYNTHETIC_PRECOMMIT_FAULT")
	p.commitBeforeError = problem
	err := ensureSMSVerificationSchemaForDriver(f.db, "sqlite")
	p.cleanupOwned(t)
	d01TxAssert(t, p.commitCalls == 1 && errors.Is(err, problem), "pre-delegate commit fault chain missing")
	d01TxAssert(t, p.rollbackCalls == 1, "pre-delegate commit fault did not attempt rollback")
	d01TxAssert(t, reflect.DeepEqual(d01View(f.db), f.before), "pre-delegate commit failure failed full restoration")
}
func TestD01SMSTransactionPostCommitAckFailure(t *testing.T) {
	f := d01NewStageFixture(t, false)
	p := d01InstallProbe(t, f)
	problem := errors.New("D01_SYNTHETIC_POSTCOMMIT_ACK_FAULT")
	p.commitAfterError = problem
	err := ensureSMSVerificationSchemaForDriver(f.db, "sqlite")
	p.cleanupOwned(t)
	d01TxAssert(t, p.commitCalls == 1 && errors.Is(err, problem), "post-delegate acknowledgement fault chain missing")
	d01TxAssert(t, errors.Is(err, sql.ErrTxDone), "rollback after actual commit must remain observable, not claimed recovered")
	d01CheckMigrated(t, f)
	t.Log("D01_FAULT_TIMING post-delegate commit already succeeded; primary migrated state is directly observed, recovery is NOT asserted")
}
func TestD01SMSTransactionRollbackFailureChain(t *testing.T) {
	f := d01NewStageFixture(t, false)
	p := d01InstallProbe(t, f)
	sqlProblem := errors.New("D01_SYNTHETIC_COPY_FAULT")
	rbProblem := errors.New("D01_SYNTHETIC_ROLLBACK_BEFORE_DELEGATE_FAULT")
	p.rollbackBeforeError = rbProblem
	hits := d01InjectRaw(t, f.db, "INSERT INTO sms_verification_codes (`id`,`phone`,`scene`", sqlProblem, nil)
	err := ensureSMSVerificationSchemaForDriver(f.db, "sqlite")
	f.db.Callback().Raw().Remove("d01:stage-fault")
	p.cleanupOwned(t) // release real SQLite transaction BEFORE querying the one-connection database
	if *hits != 1 {
		t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE copy fault hook missed")
	}
	d01TxAssert(t, p.rollbackCalls == 1 && errors.Is(err, sqlProblem) && errors.Is(err, rbProblem), "original SQL plus independent rollback errors.Is chain not both retained")
	t.Log("D01_FAULT_TIMING rollback fault before delegate; explicit test-owned realTx cleanup performed, no product restoration/retry claim")
}
func TestD01SMSNestedTransactionBoundary(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		for _, fault := range []bool{false, true} {
			name := "plain"
			if prepared {
				name = "prepared"
			}
			if fault {
				name += "-copy-fault"
			} else {
				name += "-success"
			}
			t.Run(name, func(t *testing.T) {
				f := d01NewStageFixture(t, prepared)
				outer := f.db.Begin()
				if outer.Error != nil {
					t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE outer begin: %v", outer.Error)
				}
				t.Cleanup(func() {
					if err := outer.Rollback().Error; err != nil && !errors.Is(err, sql.ErrTxDone) {
						t.Errorf("D01_STAGE_GATE_INFRA_FAILURE outer cleanup: %v", err)
					}
				})
				if err := outer.Exec("CREATE TABLE d01_outer_marker (id integer PRIMARY KEY,value text)").Error; err != nil {
					t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE caller marker: %v", err)
				}
				if err := outer.Exec("INSERT INTO d01_outer_marker(id,value) VALUES(?,?)", 1, "caller-before").Error; err != nil {
					t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE caller row: %v", err)
				}
				problem := errors.New("D01_SYNTHETIC_NESTED_COPY_FAULT")
				var hits *int
				if fault {
					hits = d01InjectRaw(t, outer, "INSERT INTO sms_verification_codes (`id`,`phone`,`scene`", problem, nil)
				}
				err := ensureSMSVerificationSchemaForDriver(outer, "sqlite")
				outer.Callback().Raw().Remove("d01:stage-fault")
				if fault {
					if *hits != 1 {
						t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE nested hook missed")
					}
					d01TxAssert(t, errors.Is(err, problem), "nested original SQL chain missing")
					d01TxAssert(t, reflect.DeepEqual(d01View(outer), f.before), "own savepoint failure did not restore SMS state inside caller transaction")
				} else {
					d01TxAssert(t, err == nil, "nested success returned error")
				}
				d01TxAssert(t, outer.Exec("INSERT INTO d01_outer_marker(id,value) VALUES(?,?)", 2, "caller-after").Error == nil, "migration consumed or committed/rolledback caller outer transaction")
				callerRollback := outer.Rollback().Error
				d01TxAssert(t, callerRollback == nil, "caller lost rollback ownership")
				d01TxAssert(t, reflect.DeepEqual(d01View(f.db), f.before) && !f.db.Migrator().HasTable("d01_outer_marker"), "caller rollback failed to remove its own writes and restore original SMS schema/row")
			})
		}
	}
}

type d01OriginalPanicCarrier interface{ OriginalPanic() any }

func TestD01SMSPanicRecoveryCauseAndRollback(t *testing.T) {
	for _, rbFault := range []bool{false, true} {
		name := "rollback-ok"
		if rbFault {
			name = "rollback-before-delegate-fault"
		}
		t.Run(name, func(t *testing.T) {
			f := d01NewStageFixture(t, false)
			p := d01InstallProbe(t, f)
			panicValue := errors.New("D01_SYNTHETIC_PANIC_VALUE")
			rbProblem := errors.New("D01_SYNTHETIC_PANIC_ROLLBACK_FAULT")
			if rbFault {
				p.rollbackBeforeError = rbProblem
			}
			hits := d01InjectRaw(t, f.db, "INSERT INTO sms_verification_codes (`id`,`phone`,`scene`", nil, panicValue)
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				_ = ensureSMSVerificationSchemaForDriver(f.db, "sqlite")
			}()
			f.db.Callback().Raw().Remove("d01:stage-fault")
			p.cleanupOwned(t)
			if *hits != 1 {
				t.Fatalf("D01_STAGE_GATE_INFRA_FAILURE panic hook missed")
			}
			cause, ok := recovered.(error)
			d01TxAssert(t, ok && errors.Is(cause, panicValue), "original panic error cause lost or swallowed")
			if rbFault {
				carrier, has := recovered.(d01OriginalPanicCarrier)
				d01TxAssert(t, has && carrier.OriginalPanic() == panicValue && errors.Is(cause, rbProblem), "panic value and independent rollback failure not both traceable")
				t.Log("D01_FAULT_TIMING panic rollback fault is before delegate; subsequent restoration belongs to explicit test-owned cleanup")
			} else {
				d01TxAssert(t, recovered == panicValue && p.rollbackCalls == 1, "successful rollback must re-panic exact original value")
				d01TxAssert(t, reflect.DeepEqual(d01View(f.db), f.before), "panic path failed original SMS schema/nine-field restoration")
			}
		})
	}
}
