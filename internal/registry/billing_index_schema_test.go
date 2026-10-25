package registry

// Human-approved policy; historical868f/old19/6PASS13FAIL remain immutable evidence.
// Real test-owned SQLite and qualified D01 fault pools; no production seam.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const d02nIndex = "idx_billing_records_user_request_type_id"
const d02nDDL = "CREATE INDEX IF NOT EXISTS idx_billing_records_user_request_type_id ON billing_records(user_id,request_id,type,id)"
const d02nPrivate = "D02_PRIVATE_SYNTHETIC_SQL_DSN_ACCOUNT_ERROR"
const d02nCatalog = "SELECT name,tbl_name,sql FROM main.sqlite_master WHERE type='index' AND name=? COLLATE NOCASE"

func d02nHash(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func d02nAssert(t *testing.T, ok bool, s string) {
	t.Helper()
	if !ok {
		t.Fatal("D02_APPROVED_POLICY_ASSERTION: " + s)
	}
}
func d02nControl(t *testing.T, ok bool, s string) {
	t.Helper()
	if !ok {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: " + s)
	}
}
func d02nQuiet(d *gorm.DB) *gorm.DB {
	return d.Session(&gorm.Session{Logger: d.Logger.LogMode(gormlogger.Silent)})
}
func d02nExec(t *testing.T, d *gorm.DB, s string) {
	t.Helper()
	if d02nQuiet(d).Exec(s).Error != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: synthetic setup SQL")
	}
}

type d02nColumn struct {
	Cid     int
	Name    string
	Type    string
	Notnull int
	Default *string `gorm:"column:dflt_value"`
	PK      int
}
type d02nIndexRow struct {
	Name  string
	Table string `gorm:"column:tbl_name"`
	SQL   string
}
type d02nView struct {
	TableSQL string
	Columns  []d02nColumn
	Indexes  []d02nIndexRow
	Target   []d02nIndexRow
	Rows     []entity.BillingRecord
	Quick    string
}

func d02nViewOf(t *testing.T, d *gorm.DB) d02nView {
	t.Helper()
	q := d02nQuiet(d)
	v := d02nView{}
	if q.Raw("SELECT sql FROM main.sqlite_master WHERE type='table' AND name='billing_records'").Scan(&v.TableSQL).Error != nil || q.Raw("PRAGMA main.table_info('billing_records')").Scan(&v.Columns).Error != nil || q.Raw("SELECT name,tbl_name,sql FROM main.sqlite_master WHERE type='index' AND tbl_name='billing_records' ORDER BY name").Scan(&v.Indexes).Error != nil || q.Raw(d02nCatalog, d02nIndex).Scan(&v.Target).Error != nil || q.Order("id ASC").Find(&v.Rows).Error != nil || q.Raw("PRAGMA quick_check").Scan(&v.Quick).Error != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: typed schema/rows metadata")
	}
	return v
}
func d02nViewHash(v d02nView) string { b, _ := json.Marshal(v); return d02nHash(b) }
func d02nTarget(v d02nView) (d02nIndexRow, bool) {
	for _, x := range v.Target {
		if billingLookupIndexASCIIEqual(x.Name, d02nIndex) {
			return x, true
		}
	}
	return d02nIndexRow{}, false
}
func d02nColumnExists(v d02nView, n string) bool {
	for _, x := range v.Columns {
		if x.Name == n {
			return true
		}
	}
	return false
}
func d02nExact(t *testing.T, d *gorm.DB) {
	t.Helper()
	v := d02nViewOf(t, d)
	x, ok := d02nTarget(v)
	d02nAssert(t, ok && x.Table == "billing_records", "index owner/name missing")
	var list []struct {
		Name    string
		Unique  int `gorm:"column:unique"`
		Partial int
	}
	var fs []struct {
		Seqno int
		Cid   int
		Name  *string
		Desc  int
		Coll  string
		Key   int
	}
	if d02nQuiet(d).Raw("PRAGMA main.index_list('billing_records')").Scan(&list).Error != nil || d02nQuiet(d).Raw("PRAGMA main.index_xinfo('idx_billing_records_user_request_type_id')").Scan(&fs).Error != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: independent index shape")
	}
	ordinary := false
	for _, i := range list {
		if billingLookupIndexASCIIEqual(i.Name, d02nIndex) {
			ordinary = i.Unique == 0 && i.Partial == 0
		}
	}
	d02nAssert(t, ordinary, "nonunique/nonpartial shape")
	want := []string{"user_id", "request_id", "type", "id"}
	n := 0
	valid := true
	for _, f := range fs {
		if f.Key == 0 {
			continue
		}
		if n >= 4 || f.Seqno != n || f.Cid < 0 || f.Name == nil || *f.Name != want[n] || f.Desc != 0 || f.Coll != "BINARY" {
			valid = false
		}
		n++
	}
	d02nAssert(t, valid && n == 4, "independent ordered four-key shape")
}
func d02nPreserved(t *testing.T, b, a d02nView) {
	t.Helper()
	d02nAssert(t, reflect.DeepEqual(b, a), "original exact schema/index-DDL/typed rows were not restored")
}
func d02nMigrated(t *testing.T, d *gorm.DB, b d02nView) {
	t.Helper()
	a := d02nViewOf(t, d)
	bi, bok := d02nTarget(b)
	ai, aok := d02nTarget(a)
	d02nAssert(t, !d02nColumnExists(b, "client_ip") && d02nColumnExists(a, "client_ip"), "real missing column migration skipped")
	d02nAssert(t, bok && aok && bi == ai, "original exact index name/DDL/alias not preserved")
	d02nAssert(t, reflect.DeepEqual(a.Rows, b.Rows) && len(a.Rows) == 2 && a.Quick == "ok", "typed micro/negative amounts, IDs or rows changed")
	d02nExact(t, d)
}

type d02nFixture struct {
	db     *gorm.DB
	raw    *sql.DB
	before d02nView
}

func d02nFixtureDB(t *testing.T, prepared bool, alias bool) d02nFixture {
	t.Helper()
	p := filepath.Join(t.TempDir(), "synthetic-billing.sqlite")
	d, e := gorm.Open(sqlite.Open(p), &gorm.Config{PrepareStmt: prepared, Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if e != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: open SQLite")
	}
	raw, e := d.DB()
	if e != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: sql.DB")
	}
	raw.SetMaxOpenConns(1)
	raw.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if e := raw.Close(); e != nil {
			t.Error("D02_APPROVED_FIXTURE_INFRA: close own DB")
		}
	})
	if d.AutoMigrate(&entity.BillingRecord{}) != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: original entity schema")
	}
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	rows := []entity.BillingRecord{{ID: 44, UserID: 7, Type: "ai", Amount: 0.000062, BalanceBefore: 1.234567, BalanceAfter: 1.234505, RequestID: "D02_SYNTHETIC_GROUP", CreatedAt: at}, {ID: 77, UserID: 7, Type: "ai", Amount: -0.004321, BalanceBefore: 1.234505, BalanceAfter: 1.238826, RequestID: "D02_SYNTHETIC_GROUP", CreatedAt: at}}
	if d.Create(&rows).Error != nil || d.Migrator().DropColumn(&entity.BillingRecord{}, "ClientIP") != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: controlled legacy missing column")
	}
	ddl := d02nDDL
	if alias {
		ddl = "CREATE INDEX IDX_BILLING_RECORDS_USER_REQUEST_TYPE_ID ON billing_records(user_id,request_id,type,id)"
	}
	d02nExec(t, d, ddl)
	v := d02nViewOf(t, d)
	if len(v.Rows) != 2 || d02nColumnExists(v, "client_ip") || v.Quick != "ok" {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: legacy state")
	}
	return d02nFixture{d, raw, v}
}
func d02nInstallProbe(t *testing.T, f d02nFixture) *d01TxProbePool {
	t.Helper()
	p := &d01TxProbePool{DB: f.raw}
	f.db.ConnPool = p
	f.db.Statement.ConnPool = p
	t.Cleanup(func() { p.cleanupOwned(t) })
	return p
}
func d02nRaw(t *testing.T, d *gorm.DB, prefix string, problem error, pv any) *int {
	return d01InjectRaw(t, d, prefix, problem, pv)
}
func d02nRemoveRaw(d *gorm.DB) { d.Callback().Raw().Remove("d01:stage-fault") }
func d02nRowFault(t *testing.T, d *gorm.DB, phase int, problem error) *int {
	t.Helper()
	hits := new(int)
	if d.Callback().Row().Before("gorm:row").Register("d02-new-row-fault", func(tx *gorm.DB) {
		if tx.Statement.SQL.String() == d02nCatalog {
			*hits++
			if *hits == phase {
				tx.AddError(problem)
			}
		}
	}) != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: row hook")
	}
	t.Cleanup(func() { d.Callback().Row().Remove("d02-new-row-fault") })
	return hits
}
func d02nRecord(t *testing.T, name string, fn func(map[string]interface{})) {
	t.Helper()
	o := map[string]interface{}{"schema": "D02_HUMAN_APPROVED_REGRESSION_CASE_V1", "case": name, "native_real_SQLite": true, "product_seam": false, "DB_log_bodies_emitted": false}
	defer func() {
		o["assertions_passed"] = !t.Failed()
		b, e := json.Marshal(o)
		if e != nil {
			t.Fatal("observation encode")
		}
		t.Log("D02_APPROVED_POLICY_OBSERVATION " + string(b))
	}()
	fn(o)
}
func TestD02BillingIndexApprovedTransactions(t *testing.T) {
	cases := []string{"necessary_schema_restore_plain", "necessary_schema_restore_prepared", "begin_failure", "migration_error_rollback_retry", "restore_error_rollback_retry", "postmigration_metadata_error_rollback_retry", "restored_metadata_error_rollback_retry", "commit_before_error_rollback_retry", "commit_after_ack_error_no_retry", "rollback_error_chain_no_retry", "panic_rollback_ok_retry", "panic_rollback_error_chain_no_retry"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			d02nRecord(t, name, func(o map[string]interface{}) {
				prepared := name == "necessary_schema_restore_prepared"
				f := d02nFixtureDB(t, prepared, true)
				beforeIndex, _ := d02nTarget(f.before)
				if strings.HasPrefix(name, "necessary_schema_restore_") {
					sawPrepared := false
					restore := 0
					if f.db.Callback().Raw().Before("gorm:raw").Register("d02-prepared-path", func(tx *gorm.DB) {
						if _, ok := tx.Statement.ConnPool.(*gorm.PreparedStmtTX); ok {
							sawPrepared = true
						}
						if tx.Statement.SQL.String() == beforeIndex.SQL {
							restore++
						}
					}) != nil {
						t.Fatal("D02_APPROVED_FIXTURE_INFRA: prepared observer")
					}
					e := migrateSQLiteBillingRecordPreservingLookupIndex(f.db)
					f.db.Callback().Raw().Remove("d02-prepared-path")
					d02nAssert(t, e == nil, "real required migration/restore returned product error")
					d02nControl(t, restore >= 1, "real restore delegate unexercised")
					if prepared {
						d02nControl(t, sawPrepared, "actual PreparedStmtTX path unobserved")
					}
					d02nMigrated(t, f.db, f.before)
					o["actual_PreparedStmtTX_seen"] = sawPrepared
					o["restore_calls"] = restore
					o["necessary_client_ip_added"] = true
					o["original_target_index_SHA256"] = d02nHash([]byte(beforeIndex.SQL))
					return
				}
				p := d02nInstallProbe(t, f)
				problem := errors.New(d02nPrivate + "_PRIMARY")
				rbProblem := errors.New(d02nPrivate + "_ROLLBACK")
				var rawHits, rowHits *int
				switch name {
				case "begin_failure":
					p.beginError = problem
				case "migration_error_rollback_retry", "rollback_error_chain_no_retry":
					rawHits = d02nRaw(t, f.db, "INSERT INTO `billing_records__temp`", problem, nil)
				case "restore_error_rollback_retry":
					rawHits = d02nRaw(t, f.db, beforeIndex.SQL, problem, nil)
				case "postmigration_metadata_error_rollback_retry":
					rowHits = d02nRowFault(t, f.db, 2, problem)
				case "restored_metadata_error_rollback_retry":
					rowHits = d02nRowFault(t, f.db, 3, problem)
				case "commit_before_error_rollback_retry":
					p.commitBeforeError = problem
				case "commit_after_ack_error_no_retry":
					p.commitAfterError = problem
				case "panic_rollback_ok_retry", "panic_rollback_error_chain_no_retry":
					rawHits = d02nRaw(t, f.db, "INSERT INTO `billing_records__temp`", nil, problem)
				}
				if name == "rollback_error_chain_no_retry" || name == "panic_rollback_error_chain_no_retry" {
					p.rollbackBeforeError = rbProblem
				}
				var e error
				var recovered any
				func() {
					defer func() { recovered = recover() }()
					e = migrateSQLiteBillingRecordPreservingLookupIndex(f.db)
				}()
				d02nRemoveRaw(f.db)
				f.db.Callback().Row().Remove("d02-new-row-fault")
				if rawHits != nil {
					d02nControl(t, *rawHits == 1, "exact real SQL fault missed/repeated")
					o["SQL_fault_hits"] = *rawHits
				}
				if rowHits != nil {
					want := 2
					if name == "restored_metadata_error_rollback_retry" {
						want = 3
					}
					d02nControl(t, *rowHits == want, "metadata fault phase unexercised")
					o["catalog_calls"] = *rowHits
				}
				if strings.HasPrefix(name, "panic_") {
					cause, ok := recovered.(error)
					d02nAssert(t, ok && errors.Is(cause, problem), "original panic swallowed")
					if p.rollbackBeforeError != nil {
						carrier, ok := recovered.(d01OriginalPanicCarrier)
						d02nAssert(t, ok && carrier.OriginalPanic() == problem && errors.Is(cause, rbProblem), "panic/independent rollback chain lost")
					} else {
						d02nAssert(t, recovered == problem && p.rollbackCalls == 1, "successful rollback did not re-panic same value")
					}
				} else {
					d02nAssert(t, recovered == nil && errors.Is(e, problem), "original returned error chain lost")
				}
				o["begin_calls"] = p.beginCalls
				o["commit_calls"] = p.commitCalls
				o["rollback_calls"] = p.rollbackCalls
				if p.rollbackBeforeError != nil {
					d02nAssert(t, p.rollbackCalls == 1 && ((e != nil && errors.Is(e, rbProblem)) || recovered != nil), "independent rollback error not retained")
					p.cleanupOwned(t)
					d02nControl(t, !t.Failed(), "explicit test-owned cleanup failed")
					o["explicit_test_owned_cleanup"] = true
					o["product_restoration_claimed"] = false
					o["retry_attempted"] = false
					return
				}
				if name == "commit_after_ack_error_no_retry" {
					d02nAssert(t, p.commitCalls == 1 && p.rollbackCalls == 1 && errors.Is(e, sql.ErrTxDone), "post-commit ack must retain ErrTxDone")
					d02nMigrated(t, f.db, f.before)
					o["actual_commit_then_ack_error"] = true
					o["rollback_recovered_claimed"] = false
					o["retry_attempted"] = false
					return
				}
				d02nPreserved(t, f.before, d02nViewOf(t, f.db))
				o["original_schema_rows_index_restored"] = true
				if name == "begin_failure" {
					d02nAssert(t, p.beginCalls == 1 && p.commitCalls == 0 && p.rollbackCalls == 0, "begin failure wrote/finished transaction")
					o["retry_attempted"] = false
					return
				}
				d02nAssert(t, p.rollbackCalls == 1, "real owned rollback was not attempted")
				p.commitBeforeError = nil
				p.commitAfterError = nil
				d02nAssert(t, migrateSQLiteBillingRecordPreservingLookupIndex(f.db) == nil, "retry after real rollback failed")
				d02nMigrated(t, f.db, f.before)
				o["retry_attempted"] = true
				o["retry_only_after_real_successful_product_rollback"] = true
			})
		})
	}
	for _, prepared := range []bool{false, true} {
		for _, fault := range []bool{false, true} {
			name := "nested_plain_success"
			if prepared {
				name = "nested_prepared_success"
			}
			if fault {
				name = strings.TrimSuffix(name, "success") + "apply_error"
			}
			t.Run(name, func(t *testing.T) {
				d02nRecord(t, name, func(o map[string]interface{}) { d02nNested(t, prepared, "", fault, o) })
			})
		}
	}
	for _, name := range []string{"nested_savepoint_begin_error", "nested_release_error_rollback_retry", "nested_rollback_to_error_no_retry"} {
		t.Run(name, func(t *testing.T) {
			d02nRecord(t, name, func(o map[string]interface{}) {
				d02nNested(t, true, name, name == "nested_rollback_to_error_no_retry", o)
			})
		})
	}
}
func d02nNested(t *testing.T, prepared bool, mode string, applyFault bool, o map[string]interface{}) {
	t.Helper()
	f := d02nFixtureDB(t, prepared, true)
	outer := f.db.Begin()
	if outer.Error != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: caller begin")
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if e := outer.Rollback().Error; e != nil && !errors.Is(e, sql.ErrTxDone) {
				t.Error("D02_APPROVED_FIXTURE_INFRA: caller cleanup")
			}
		}
	})
	if prepared {
		_, ok := outer.Statement.ConnPool.(*gorm.PreparedStmtTX)
		d02nAssert(t, ok, "real caller PreparedStmtTX missing")
		o["caller_PreparedStmtTX_observed"] = ok
	}
	d02nExec(t, outer, "CREATE TABLE d02_caller_owned_marker(id integer PRIMARY KEY,value text)")
	d02nExec(t, outer, "INSERT INTO d02_caller_owned_marker(id,value) VALUES(1,'caller-before')")
	problem := errors.New(d02nPrivate + "_NESTED_APPLY")
	boundaryProblem := errors.New(d02nPrivate + "_NESTED_BOUNDARY")
	var hits *int
	if applyFault {
		hits = d02nRaw(t, outer, "INSERT INTO `billing_records__temp`", problem, nil)
	}
	boundaryHits := 0
	releaseCalls := 0
	rollbackToCalls := 0
	if outer.Callback().Raw().Before("gorm:raw").Register("d02-nested-boundary", func(tx *gorm.DB) {
		s := tx.Statement.SQL.String()
		if strings.HasPrefix(s, "RELEASE tiku_billing_index_schema_") {
			releaseCalls++
		}
		if strings.HasPrefix(s, "ROLLBACK TO tiku_billing_index_schema_") {
			rollbackToCalls++
		}
		match := mode == "nested_savepoint_begin_error" && strings.HasPrefix(s, "SAVEPOINT tiku_billing_index_schema_") || mode == "nested_release_error_rollback_retry" && strings.HasPrefix(s, "RELEASE tiku_billing_index_schema_") && releaseCalls == 1 || mode == "nested_rollback_to_error_no_retry" && strings.HasPrefix(s, "ROLLBACK TO tiku_billing_index_schema_")
		if match {
			boundaryHits++
			tx.AddError(boundaryProblem)
		}
	}) != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: nested callback")
	}
	e := migrateSQLiteBillingRecordPreservingLookupIndex(outer)
	d02nRemoveRaw(outer)
	outer.Callback().Raw().Remove("d02-nested-boundary")
	if hits != nil {
		d02nControl(t, *hits == 1, "nested apply SQL fault unexercised")
		d02nAssert(t, errors.Is(e, problem), "nested apply error chain lost")
	}
	if mode != "" {
		d02nControl(t, boundaryHits == 1, "savepoint boundary fault unexercised")
		d02nAssert(t, errors.Is(e, boundaryProblem), "savepoint boundary error chain lost")
	}
	if mode == "nested_rollback_to_error_no_retry" {
		d02nAssert(t, rollbackToCalls == 1 && releaseCalls == 0, "failed rollback-to boundary released or concealed")
		o["product_restoration_claimed"] = false
		o["retry_attempted"] = false
	} else if applyFault || mode != "" {
		d02nPreserved(t, f.before, d02nViewOf(t, outer))
		o["own_savepoint_restored"] = true
		if mode == "nested_release_error_rollback_retry" {
			d02nAssert(t, releaseCalls == 2 && rollbackToCalls == 1, "release error did not rollback own savepoint/release independently")
			d02nAssert(t, migrateSQLiteBillingRecordPreservingLookupIndex(outer) == nil, "nested retry after successful owned rollback failed")
			d02nMigrated(t, outer, f.before)
			o["retry_attempted"] = true
		}
	} else {
		d02nAssert(t, e == nil, "nested success failed")
		d02nMigrated(t, outer, f.before)
	}
	d02nExec(t, outer, "INSERT INTO d02_caller_owned_marker(id,value) VALUES(2,'caller-after')")
	var count int64
	d02nAssert(t, outer.Raw("SELECT count(*) FROM d02_caller_owned_marker").Scan(&count).Error == nil && count == 2, "helper consumed caller ownership or lost caller writes")
	d02nAssert(t, outer.Rollback().Error == nil, "caller rollback ownership lost")
	closed = true
	d02nPreserved(t, f.before, d02nViewOf(t, f.db))
	d02nAssert(t, !f.db.Migrator().HasTable("d02_caller_owned_marker"), "caller rollback did not remove own writes")
	o["caller_before_after_writes_and_outer_rollback_observed"] = true
	o["owned_release_calls"] = releaseCalls
	o["owned_rollback_to_calls"] = rollbackToCalls
}

// Test-only observer: embed and delegate the original Dialector/Migrator unchanged.
// Counts cover this cached startup phase, not setup; Trace counts emitted GORM SQL,
// not driver-internal execution. No SQL text, parameters, data, or DSN is emitted.
type d02nMigrationObservation struct {
	AutoMigrateEntries int `json:"auto_migrate_entries"`
	Create             int `json:"create_attempts"`
	Alter              int `json:"alter_attempts"`
	Drop               int `json:"drop_attempts"`
	Rename             int `json:"rename_attempts"`
	Insert             int `json:"insert_attempts"`
	Update             int `json:"update_attempts"`
	Delete             int `json:"delete_attempts"`
	Replace            int `json:"replace_attempts"`
	OtherWrite         int `json:"other_write_attempts"`
	Boundary           int `json:"boundary_statements"`
	ReadOrMetadata     int `json:"read_or_metadata"`
	Unclassified       int `json:"unclassified_statements"`
}

func (o *d02nMigrationObservation) addSQL(s string) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return
	}
	switch strings.ToUpper(fields[0]) {
	case "CREATE":
		o.Create++
	case "ALTER":
		o.Alter++
	case "DROP":
		o.Drop++
	case "RENAME":
		o.Rename++
	case "INSERT":
		o.Insert++
	case "UPDATE":
		o.Update++
	case "DELETE":
		o.Delete++
	case "REPLACE":
		o.Replace++
	case "VACUUM", "REINDEX", "ANALYZE", "ATTACH", "DETACH":
		o.OtherWrite++
	case "BEGIN", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE":
		o.Boundary++
	case "SELECT", "PRAGMA", "EXPLAIN":
		o.ReadOrMetadata++
	default:
		o.Unclassified++
	}
}
func (o *d02nMigrationObservation) writeAttempts() int {
	return o.Create + o.Alter + o.Drop + o.Rename + o.Insert + o.Update + o.Delete + o.Replace + o.OtherWrite
}

type d02nObservedDialector struct {
	gorm.Dialector
	observation *d02nMigrationObservation
}
type d02nObservedMigrator struct {
	gorm.Migrator
	observation *d02nMigrationObservation
}

// Preserve the original optional nested-transaction capability, not a fake success.
func (d d02nObservedDialector) SavePoint(tx *gorm.DB, name string) error {
	if original, ok := d.Dialector.(gorm.SavePointerDialectorInterface); ok {
		return original.SavePoint(tx, name)
	}
	return gorm.ErrUnsupportedDriver
}
func (d d02nObservedDialector) RollbackTo(tx *gorm.DB, name string) error {
	if original, ok := d.Dialector.(gorm.SavePointerDialectorInterface); ok {
		return original.RollbackTo(tx, name)
	}
	return gorm.ErrUnsupportedDriver
}

func (d d02nObservedDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return d02nObservedMigrator{Migrator: d.Dialector.Migrator(db), observation: d.observation}
}
func (m d02nObservedMigrator) AutoMigrate(values ...interface{}) error {
	m.observation.AutoMigrateEntries++
	return m.Migrator.AutoMigrate(values...)
}

type d02nObservedLogger struct {
	gormlogger.Interface
	observation *d02nMigrationObservation
}

func (l d02nObservedLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return d02nObservedLogger{Interface: l.Interface.LogMode(level), observation: l.observation}
}
func (l d02nObservedLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	s, rows := fc()
	l.observation.addSQL(s)
	l.Interface.Trace(ctx, begin, func() (string, int64) { return s, rows }, err)
}
func d02nObserveMigration(t *testing.T, d *gorm.DB, o *d02nMigrationObservation) {
	t.Helper()
	dialector, logger := d.Dialector, d.Logger
	d.Dialector = d02nObservedDialector{Dialector: dialector, observation: o}
	d.Logger = d02nObservedLogger{Interface: logger, observation: o}
	t.Cleanup(func() { d.Dialector = dialector; d.Logger = logger })
}

type d02nActions struct {
	GlobalDrop       int `json:"global_drop"`
	TargetIndexDrop  int `json:"target_index_drop"`
	BillingTableDrop int `json:"billing_table_drop"`
	OtherDrop        int `json:"other_drop"`
	Restore          int `json:"verified_definition_restore"`
	OptionalCreate   int `json:"optional_create"`
}

func (a *d02nActions) add(s, definition string) {
	u := strings.ToUpper(strings.Join(strings.Fields(strings.NewReplacer("`", "", "\"", "", "[", "", "]", "").Replace(s)), " "))
	if strings.HasPrefix(u, "DROP ") {
		a.GlobalDrop++
		if strings.HasPrefix(u, "DROP INDEX ") && strings.Contains(u, strings.ToUpper(d02nIndex)) {
			a.TargetIndexDrop++
		} else if u == "DROP TABLE BILLING_RECORDS" || u == "DROP TABLE IF EXISTS BILLING_RECORDS" {
			a.BillingTableDrop++
		} else {
			a.OtherDrop++
		}
	}
	if s == definition && definition != "" {
		a.Restore++
	}
	if s == d02nDDL {
		a.OptionalCreate++
	}
}
func d02nSeedRows(t *testing.T, d *gorm.DB) {
	t.Helper()
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	rows := []entity.BillingRecord{{ID: 44, UserID: 7, Type: "ai", Amount: 0.000062, BalanceBefore: 1.234567, BalanceAfter: 1.234505, RequestID: "D02_SYNTHETIC_GROUP", CreatedAt: at}, {ID: 77, UserID: 7, Type: "ai", Amount: -0.004321, BalanceBefore: 1.234505, BalanceAfter: 1.238826, RequestID: "D02_SYNTHETIC_GROUP", CreatedAt: at}}
	if d02nQuiet(d).Create(&rows).Error != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: synthetic billing seed")
	}
}
func d02nStartup(t *testing.T) (configs.Config, *gorm.DB, *bytes.Buffer) {
	t.Helper()
	t.Setenv("SQL_DSN", "")
	t.Setenv("TIKU_APP_MODE", "production")
	t.Setenv("TIKU_ADMIN_PASSWORD", "D02FixtureAdmin123!")
	resetRegistryDBState(t)
	t.Cleanup(func() { resetRegistryDBState(t) })
	var logs bytes.Buffer
	out, er := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter = &logs
	gin.DefaultErrorWriter = &logs
	t.Cleanup(func() { gin.DefaultWriter = out; gin.DefaultErrorWriter = er })
	c := configs.Config{Database: configs.DatabaseConfig{Path: filepath.Join(t.TempDir(), "startup.sqlite")}}
	d, e := RegisterDBWithError(c)
	if e != nil || d == nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: first genuine startup")
	}
	d02nSeedRows(t, d)
	logs.Reset()
	return c, d, &logs
}
func d02nWarnings(t *testing.T, b *bytes.Buffer, want string) {
	t.Helper()
	n := 0
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.Contains(line, "billing_index_startup") {
			n++
			parts := strings.SplitN(line, " | ", 2)
			d02nAssert(t, len(parts) == 2 && parts[1] == "billing_index_startup class="+want+" index="+d02nIndex && strings.Contains(line, "[WARN]"), "fixed-safe warning fields")
		}
	}
	expected := 0
	if want != "" {
		expected = 1
	}
	d02nAssert(t, n == expected && !strings.Contains(b.String(), d02nPrivate), "warning count/privacy changed")
}
func d02nWrongDDL(name string) string {
	p := "CREATE INDEX " + d02nIndex + " ON "
	switch name {
	case "wrong_table":
		return p + "other_billing_index_owner(user_id,request_id,type,id)"
	case "wrong_order", "concurrent_wrong":
		return p + "billing_records(user_id,type,request_id,id)"
	case "unique":
		return "CREATE UNIQUE INDEX " + d02nIndex + " ON billing_records(user_id,request_id,type,id)"
	case "partial":
		return p + "billing_records(user_id,request_id,type,id) WHERE id>0"
	case "expression":
		return p + "billing_records(user_id,request_id,type,(id+0))"
	case "descending":
		return p + "billing_records(user_id,request_id,type,id DESC)"
	case "collation":
		return p + "billing_records(user_id,request_id COLLATE NOCASE,type,id)"
	case "uppercase_wrong":
		return "CREATE INDEX IDX_BILLING_RECORDS_USER_REQUEST_TYPE_ID ON billing_records(user_id,type,request_id,id)"
	}
	panic("unknown fixed conflict case")
}
func TestD02BillingIndexApprovedStartupPolicy(t *testing.T) {
	for _, alias := range []bool{false, true} {
		name := "genuine_startup_correct_restore"
		if alias {
			name = "uppercase_correct_restore"
		}
		t.Run(name, func(t *testing.T) {
			d02nRecord(t, name, func(o map[string]interface{}) {
				c, d, logs := d02nStartup(t)
				if alias {
					d02nExec(t, d, "DROP INDEX "+d02nIndex)
					d02nExec(t, d, "CREATE INDEX IDX_BILLING_RECORDS_USER_REQUEST_TYPE_ID ON billing_records(user_id,request_id,type,id)")
				}
				before := d02nViewOf(t, d)
				x, _ := d02nTarget(before)
				migration := &d02nMigrationObservation{}
				d02nObserveMigration(t, d, migration)
				actions := &d02nActions{}
				d.Callback().Raw().Before("gorm:raw").Register("d02-action", func(tx *gorm.DB) { actions.add(tx.Statement.SQL.String(), x.SQL) })
				_, e := RegisterDBWithError(c)
				d.Callback().Raw().Remove("d02-action")
				d02nAssert(t, e == nil, "genuine cached startup failed")
				d02nControl(t, migration.AutoMigrateEntries > 0 && migration.ReadOrMetadata > 0, "transparent genuine migrator/SQL observer positive control unexercised")
				after := d02nViewOf(t, d)
				y, ok := d02nTarget(after)
				d02nAssert(t, ok && x == y && reflect.DeepEqual(before.Rows, after.Rows), "exact name/DDL/rows/alias not preserved")
				d02nExact(t, d)
				d02nWarnings(t, logs, "")
				d02nAssert(t, actions.TargetIndexDrop == 0 && actions.GlobalDrop == actions.TargetIndexDrop+actions.BillingTableDrop+actions.OtherDrop, "target index action/global accounting invalid")
				o["actions"] = actions
				o["migration_observation"] = migration
				o["genuine_startup_calls"] = 2
				o["target_index_definition_SHA256"] = d02nHash([]byte(x.SQL))
				o["required_rebuild_allowed_not_CREATE0_claim"] = true
			})
		})
	}
	t.Run("typed_conflict_no_delete", func(t *testing.T) {
		d02nRecord(t, "typed_conflict_no_delete", func(o map[string]interface{}) {
			f := d02nFixtureDB(t, false, false)
			d02nExec(t, f.db, "DROP INDEX "+d02nIndex)
			d02nExec(t, f.db, d02nWrongDDL("wrong_order"))
			before := d02nViewOf(t, f.db)
			e := ensureSQLiteBillingLookupIndex(f.db)
			var typed billingLookupIndexShapeMismatchError
			d02nAssert(t, errors.As(e, &typed), "typed confirmed mismatch missing")
			d02nPreserved(t, before, d02nViewOf(t, f.db))
		})
	})
	for _, winner := range []bool{false, true} {
		name := "absent_create_failure_warn"
		if winner {
			name = "absent_create_error_exact_winner"
		}
		t.Run(name, func(t *testing.T) {
			d02nRecord(t, name, func(o map[string]interface{}) {
				c, d, logs := d02nStartup(t)
				d02nExec(t, d, "DROP INDEX "+d02nIndex)
				raw, e := d.DB()
				if e != nil {
					t.Fatal("D02_APPROVED_FIXTURE_INFRA: raw")
				}
				hits := 0
				d.Callback().Raw().Before("gorm:raw").Register("d02-create", func(tx *gorm.DB) {
					if tx.Statement.SQL.String() == d02nDDL {
						hits++
						if winner {
							if _, e := raw.ExecContext(context.Background(), d02nDDL); e != nil {
								tx.AddError(e)
								return
							}
						}
						tx.AddError(errors.New(d02nPrivate))
					}
				})
				_, e = RegisterDBWithError(c)
				d.Callback().Raw().Remove("d02-create")
				d02nControl(t, hits == 1, "genuine CREATE fault control unexercised")
				d02nAssert(t, e == nil, "genuine CREATE error policy returned product error")
				_, exists := d02nTarget(d02nViewOf(t, d))
				d02nAssert(t, exists == winner, "known winner/absence state")
				want := "create_unavailable"
				if winner {
					want = ""
					d02nExact(t, d)
				}
				d02nWarnings(t, logs, want)
				o["create_fault_hits"] = hits
				o["winner_known_exact"] = winner
			})
		})
	}
	t.Run("two_concurrent_create_rechecks", func(t *testing.T) {
		d02nRecord(t, "two_concurrent_create_rechecks", func(o map[string]interface{}) {
			f := d02nFixtureDB(t, false, false)
			d02nExec(t, f.db, "DROP INDEX "+d02nIndex)
			f.raw.SetMaxOpenConns(4)
			ctx, cancel := context.WithCancel(context.Background())
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			out := make(chan error, 2)
			var releaseOnce, removeOnce sync.Once
			var workers sync.WaitGroup
			var hits atomic.Int32
			releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
			removeCallback := func() { removeOnce.Do(func() { f.db.Callback().Raw().Remove("d02-concurrent") }) }
			if f.db.Callback().Raw().Before("gorm:raw").Register("d02-concurrent", func(tx *gorm.DB) {
				if tx.Statement.SQL.String() == d02nDDL {
					hits.Add(1)
					select {
					case entered <- struct{}{}:
					case <-ctx.Done():
						tx.AddError(ctx.Err())
						return
					}
					select {
					case <-release:
					case <-ctx.Done():
						tx.AddError(ctx.Err())
					}
				}
			}) != nil {
				cancel()
				t.Fatal("D02_APPROVED_FIXTURE_INFRA: concurrent callback registration")
			}
			workers.Add(2)
			done := make(chan struct{})
			for i := 0; i < 2; i++ {
				go func() { defer workers.Done(); out <- ensureSQLiteBillingLookupIndex(f.db.WithContext(ctx)) }()
			}
			go func() { workers.Wait(); close(done) }()
			// No Fatal/control/policy checks until real workers finish. A buffer is not a join.
			stopAndDrain := func() {
				releaseGate()
				cancel()
				timer := time.NewTimer(30 * time.Second)
				defer timer.Stop()
				select {
				case <-done:
					removeCallback()
				case <-timer.C:
					fmt.Fprintln(os.Stderr, "D02_APPROVED_FIXTURE_INFRA: cancelled concurrent SQL workers did not drain; test binary hard-stop; cleanup not claimed")
					os.Exit(2)
				}
			}
			t.Cleanup(stopAndDrain)
			gateTimer := time.NewTimer(10 * time.Second)
			gateMiss := false
			n := 0
			for n < 2 && !gateMiss {
				select {
				case <-entered:
					n++
				case <-gateTimer.C:
					gateMiss = true
				}
			}
			gateTimer.Stop()
			releaseGate()
			if gateMiss {
				cancel()
			}
			results := make([]error, 0, 2)
			collectMiss := false
			collectTimer := time.NewTimer(15 * time.Second)
			for len(results) < 2 && !collectMiss {
				select {
				case e := <-out:
					results = append(results, e)
				case <-collectTimer.C:
					collectMiss = true
					cancel()
				}
			}
			collectTimer.Stop()
			stopAndDrain()
			// WaitGroup completion guarantees both buffered results were actually produced.
			for len(results) < 2 {
				results = append(results, <-out)
			}
			d02nControl(t, !gateMiss && n == 2, "bounded two-CREATE gate missed after workers drained")
			d02nControl(t, !collectMiss, "bounded two-CREATE result deadline missed after workers drained")
			d02nControl(t, hits.Load() == 2, "both CREATE delegates not exercised")
			classes := []string{}
			for _, e := range results {
				if e == nil {
					classes = append(classes, "exact")
				} else {
					var op *billingLookupIndexOperationError
					ok := errors.As(e, &op) && op.class == "metadata_unknown"
					d02nAssert(t, ok, "concurrency returned unsupported mismatch/error")
					classes = append(classes, "metadata_unknown_STOP")
				}
			}
			d02nExact(t, f.db)
			o["create_calls"] = hits.Load()
			o["outcomes"] = classes
			o["worker_results_collected"] = len(results)
			o["workers_completed_before_assertions_and_callback_cleanup"] = true
		})
	})
	fatal := []string{"unknown_catalog", "unknown_list", "unknown_xinfo", "postcreate_unknown", "restore_failure", "wrong_table", "wrong_order", "unique", "partial", "expression", "descending", "collation", "uppercase_wrong", "concurrent_wrong"}
	for _, name := range fatal {
		t.Run(name+"_STOP", func(t *testing.T) {
			d02nRecord(t, name+"_STOP", func(o map[string]interface{}) { d02nFatalParent(t, name, o) })
		})
	}
}

type d02nChildWriter struct {
	actions   *d02nActions
	hits      *int
	migration *d02nMigrationObservation
}

func (w d02nChildWriter) Write(p []byte) (int, error) {
	s := string(p)
	if strings.Contains(s, d02nPrivate) {
		fmt.Fprintln(os.Stdout, "D02_LOG_PRIVATE_MARKER_SEEN")
	}
	if strings.Contains(s, "billing_index_startup class=") {
		class := strings.Fields(strings.SplitN(s, "billing_index_startup class=", 2)[1])[0]
		switch class {
		case "shape_mismatch", "metadata_unknown", "restore_failed", "restore_verification_failed", "billing_migration_failed", "migration_transaction_failed", "transaction_begin_failed", "savepoint_begin_failed":
		default:
			class = "UNRECOGNIZED"
		}
		b, _ := json.Marshal(map[string]interface{}{"class": class, "fatal": strings.Contains(s, "[FATAL]"), "actions": w.actions, "fault_hits": *w.hits, "migration_observation": w.migration})
		fmt.Fprintln(os.Stdout, "D02_SAFE_FATAL "+string(b))
	}
	return len(p), nil
}
func TestD02BillingIndexApprovedFatalChild(t *testing.T) {
	name := os.Getenv("D02_APPROVED_FATAL_CHILD")
	if name == "" {
		t.Skip("bounded subprocess helper only")
	}
	p := os.Getenv("D02_APPROVED_CHILD_DB")
	if !filepath.IsAbs(p) || filepath.Base(p) != "startup.sqlite" || !strings.HasPrefix(p, os.TempDir()+string(os.PathSeparator)) {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: own child path")
	}
	for q := filepath.Dir(p); ; q = filepath.Dir(q) {
		if _, e := os.Lstat(filepath.Join(q, ".git")); e == nil || !os.IsNotExist(e) {
			t.Fatal("D02_APPROVED_FIXTURE_INFRA: no Git ancestor")
		}
		if q == "/" {
			break
		}
	}
	t.Setenv("SQL_DSN", "")
	t.Setenv("TIKU_APP_MODE", "production")
	t.Setenv("TIKU_ADMIN_PASSWORD", "D02FixtureAdmin123!")
	resetRegistryDBState(t)
	actions := &d02nActions{}
	faultHits := 0
	migration := &d02nMigrationObservation{}
	gin.DefaultWriter = d02nChildWriter{actions, &faultHits, migration}
	gin.DefaultErrorWriter = gin.DefaultWriter
	c := configs.Config{Database: configs.DatabaseConfig{Path: p}}
	d, e := RegisterDBWithError(c)
	if e != nil || d == nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: first child genuine startup")
	}
	d02nSeedRows(t, d)
	if name == "concurrent_wrong" || name == "postcreate_unknown" {
		d02nExec(t, d, "DROP INDEX "+d02nIndex)
	} else if !strings.HasPrefix(name, "unknown_") && name != "restore_failure" {
		d02nExec(t, d, "DROP INDEX "+d02nIndex)
		if name == "wrong_table" {
			d02nExec(t, d, "CREATE TABLE other_billing_index_owner(user_id INTEGER,request_id TEXT,type TEXT,id INTEGER)")
		}
		d02nExec(t, d, d02nWrongDDL(name))
	}
	before := d02nViewOf(t, d)
	ix, _ := d02nTarget(before)
	b, _ := json.Marshal(map[string]interface{}{"before_hash": d02nViewHash(before), "target_definition_SHA256": d02nHash([]byte(ix.SQL))})
	fmt.Fprintln(os.Stdout, "D02_CHILD_BEFORE "+string(b))
	raw, e := d.DB()
	if e != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: child raw")
	}
	created := false
	if name == "restore_failure" {
		// This fault fixture explicitly selects the underlying legacy SQLite
		// migrator so a REAL table rebuild occurs. Production RegisterDB keeps
		// sqliteQuietDialector; independent quiet tests forbid column rewrites.
		// Without this fixture precondition upstream quiet correctly avoids the
		// old reference's redundant rebuild, so no restore fault can be injected.
		d.Dialector = sqlite.Open(p)
	}
	d02nObserveMigration(t, d, migration)
	d.Callback().Raw().Before("gorm:raw").Register("d02-child-raw", func(tx *gorm.DB) {
		s := tx.Statement.SQL.String()
		actions.add(s, ix.SQL)
		if name == "restore_failure" && s == ix.SQL {
			faultHits++
			tx.AddError(errors.New(d02nPrivate))
		}
		if name == "concurrent_wrong" && s == d02nDDL {
			faultHits++
			if _, e := raw.ExecContext(context.Background(), d02nWrongDDL("concurrent_wrong")); e != nil {
				tx.AddError(e)
				return
			}
			tx.AddError(errors.New(d02nPrivate))
		}
	})
	d.Callback().Raw().After("gorm:raw").Register("d02-child-created", func(tx *gorm.DB) {
		if tx.Statement.SQL.String() == d02nDDL && tx.Error == nil {
			created = true
		}
	})
	d.Callback().Row().Before("gorm:row").Register("d02-child-row", func(tx *gorm.DB) {
		s := tx.Statement.SQL.String()
		match := name == "unknown_catalog" && s == d02nCatalog || name == "unknown_list" && s == "PRAGMA main.index_list('billing_records')" || name == "unknown_xinfo" && s == "PRAGMA main.index_xinfo('idx_billing_records_user_request_type_id')" || name == "postcreate_unknown" && created && s == d02nCatalog
		if match {
			faultHits++
			tx.AddError(errors.New(d02nPrivate))
		}
	})
	fmt.Fprintln(os.Stdout, "D02_CALLING_GENUINE_STARTUP")
	_, e = RegisterDBWithError(c)
	_ = e
	fmt.Fprintf(os.Stdout, "D02_UNEXPECTED_STARTUP_RETURNED hits=%d\n", faultHits)
}
func d02nFatalParent(t *testing.T, name string, o map[string]interface{}) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "startup.sqlite")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestD02BillingIndexApprovedFatalChild$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "D02_APPROVED_FATAL_CHILD="+name, "D02_APPROVED_CHILD_DB="+p)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	var childExit *int
	launchFailed, timedOut := false, false
	nb, nf, nr := 0, 0, 0
	o["fixed_class"] = nil
	o["actions"] = nil
	o["migration_observation"] = nil
	o["fault_hits"] = nil
	o["before_hash"] = nil
	o["after_hash"] = nil
	o["before_state_observed"] = false
	o["post_state_observed"] = false
	o["safe_fatal_observed"] = false
	// Always preserve actual child bytes/exit before any semantic Fatal can abort.
	// Missing fatal/poststate data remain null/observedfalse, never expected values.
	defer func() {
		o["child_exit"] = childExit
		o["child_launch_failed"] = launchFailed
		o["child_timed_out"] = timedOut
		o["raw_before_record_count"] = nb
		o["raw_safe_fatal_count"] = nf
		o["raw_returned_record_count"] = nr
		o["child_stdout_SHA256"] = d02nHash(stdout.Bytes())
		o["child_stderr_SHA256"] = d02nHash(stderr.Bytes())
		b, _ := json.Marshal(map[string]interface{}{"case": name, "exit": childExit, "stdout": stdout.String(), "stderr": stderr.String(), "launch_failed": launchFailed, "timed_out": timedOut})
		t.Log("D02_APPROVED_CHILD_LITERAL " + string(b))
	}()
	e := cmd.Run()
	status := 0
	if e != nil {
		var ex *exec.ExitError
		if errors.As(e, &ex) {
			status = ex.ExitCode()
			childExit = &status
		} else {
			launchFailed = true
		}
	} else {
		childExit = &status
	}
	timedOut = ctx.Err() != nil
	d02nControl(t, !launchFailed && childExit != nil, "child launch/exit unavailable")
	d02nControl(t, !timedOut, "child deadline")
	text := stdout.String()
	var before map[string]string
	var returnedHits *int
	var observed struct {
		Class     string                   `json:"class"`
		Fatal     bool                     `json:"fatal"`
		Actions   d02nActions              `json:"actions"`
		FaultHits int                      `json:"fault_hits"`
		Migration d02nMigrationObservation `json:"migration_observation"`
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "D02_UNEXPECTED_STARTUP_RETURNED hits=") {
			nr++
			var hits int
			if _, err := fmt.Sscanf(line, "D02_UNEXPECTED_STARTUP_RETURNED hits=%d", &hits); err != nil {
				t.Fatal("D02_APPROVED_FIXTURE_INFRA: returned child counter")
			}
			returnedHits = &hits
		}
		if strings.HasPrefix(line, "D02_CHILD_BEFORE ") {
			nb++
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "D02_CHILD_BEFORE ")), &before) != nil {
				t.Fatal("D02_APPROVED_FIXTURE_INFRA: child before record")
			}
		}
		if strings.HasPrefix(line, "D02_SAFE_FATAL ") {
			nf++
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "D02_SAFE_FATAL ")), &observed) != nil {
				t.Fatal("D02_APPROVED_FIXTURE_INFRA: child fixed diagnostic")
			}
		}
	}
	o["safe_fatal_observed"] = nf == 1
	o["before_state_observed"] = nb == 1
	if nb == 1 {
		o["before_hash"] = before["before_hash"]
	}
	if nf == 1 {
		o["fixed_class"] = observed.Class
		o["actions"] = observed.Actions
		o["migration_observation"] = observed.Migration
		o["fault_hits"] = observed.FaultHits
	} else if nf == 0 && nr == 1 && returnedHits != nil {
		o["fault_hits"] = *returnedHits
	}
	d02nControl(t, nb == 1 && strings.Count(text, "D02_CALLING_GENUINE_STARTUP") == 1, "child setup/genuine startup reachability unavailable")
	want := "shape_mismatch"
	if strings.HasPrefix(name, "unknown_") || name == "postcreate_unknown" {
		want = "metadata_unknown"
	}
	if name == "restore_failure" {
		want = "restore_failed"
	}
	d02nAssert(t, status == 1 && nb == 1 && nf == 1 && observed.Fatal && observed.Class == want && strings.Contains(text, "D02_CALLING_GENUINE_STARTUP") && !strings.Contains(text, "D02_UNEXPECTED_STARTUP_RETURNED") && !strings.Contains(text, "D02_LOG_PRIVATE_MARKER_SEEN"), "real startup did not STOP safely at requested policy")
	expectedHits := 0
	if strings.HasPrefix(name, "unknown_") || name == "postcreate_unknown" || name == "restore_failure" || name == "concurrent_wrong" {
		expectedHits = 1
	}
	d02nControl(t, observed.FaultHits == expectedHits, "real fatal phase injection missed/repeated")
	d, er := gorm.Open(sqlite.Open(p), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if er != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: reopen own child DB")
	}
	sd, er := d.DB()
	if er != nil {
		t.Fatal("D02_APPROVED_FIXTURE_INFRA: child sql.DB")
	}
	defer sd.Close()
	after := d02nViewOf(t, d)
	o["post_state_observed"] = true
	o["after_hash"] = d02nViewHash(after)
	index, present := d02nTarget(after)
	if name != "concurrent_wrong" && name != "postcreate_unknown" {
		d02nAssert(t, d02nViewHash(after) == before["before_hash"], "STOP/rollback changed original exact billing schema/index/rows")
	}
	if name == "concurrent_wrong" {
		d02nAssert(t, present && index.SQL == d02nWrongDDL(name), "conflicting concurrent winner deleted/replaced")
	}
	if strings.HasPrefix(name, "unknown_") || (!strings.HasPrefix(name, "unknown_") && name != "restore_failure" && name != "concurrent_wrong" && name != "postcreate_unknown") {
		d02nAssert(t, observed.Migration.AutoMigrateEntries == 0 && observed.Migration.writeAttempts() == 0 && observed.Migration.Boundary == 0 && observed.Migration.Unclassified == 0, "preflight STOP occurred after genuine AutoMigrate entry or emitted migration/write SQL")
		d02nControl(t, observed.Migration.ReadOrMetadata > 0, "preflight metadata observation unexercised")
		o["preflight_before_any_AutoMigrate_or_emitted_write_observed"] = true
	}
	d02nAssert(t, observed.Actions.TargetIndexDrop == 0 && observed.Actions.GlobalDrop == observed.Actions.TargetIndexDrop+observed.Actions.BillingTableDrop+observed.Actions.OtherDrop, "target-vs-global DROP accounting")
	o["postcreate_unknown_may_leave_created_index_not_rollback_claim"] = name == "postcreate_unknown"
}
