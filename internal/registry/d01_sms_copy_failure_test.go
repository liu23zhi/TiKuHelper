package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const d01CopyStatement = "INSERT INTO sms_verification_codes (`id`,`phone`,`scene`,`code_hash`,`status`,`expires_at`,`consumed_at`,`request_ip`,`created_at`,`updated_at`) SELECT `id`,`phone`,'' AS `scene`,`code_hash`,`status`,`expires_at`,`consumed_at`,`request_ip`,`created_at`,`updated_at` FROM sms_verification_codes__legacy_scene"
const d01SyntheticID int64 = 91001

type d01SMSView struct {
	Schema             string   `json:"schema"`
	PrimaryCount       int64    `json:"primary_count"`
	Row                []any    `json:"row"`
	RowPresent         bool     `json:"row_present"`
	LegacyTablePresent bool     `json:"legacy_table_present"`
	QuickCheck         string   `json:"quick_check"`
	Errors             []string `json:"errors"`
}

func d01View(db *gorm.DB) d01SMSView {
	v := d01SMSView{Errors: []string{}}
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE type='table' AND name='sms_verification_codes'").Row().Scan(&v.Schema); err != nil {
		v.Errors = append(v.Errors, "schema:"+err.Error())
	}
	if err := db.Raw("SELECT COUNT(*) FROM sms_verification_codes").Row().Scan(&v.PrimaryCount); err != nil {
		v.Errors = append(v.Errors, "count:"+err.Error())
	}
	var id int64
	var phone, code, status, expires, consumed, ip, created, updated string
	if err := db.Raw("SELECT id,phone,code_hash,status,CAST(expires_at AS TEXT),COALESCE(CAST(consumed_at AS TEXT),'<D01_NULL>'),request_ip,CAST(created_at AS TEXT),CAST(updated_at AS TEXT) FROM sms_verification_codes WHERE id=?", d01SyntheticID).Row().Scan(&id, &phone, &code, &status, &expires, &consumed, &ip, &created, &updated); err == nil {
		v.RowPresent = true
		v.Row = []any{id, phone, code, status, expires, consumed, ip, created, updated}
	} else {
		v.Errors = append(v.Errors, "row:"+err.Error())
	}
	v.LegacyTablePresent = db.Migrator().HasTable("sms_verification_codes__legacy_scene")
	if err := db.Raw("PRAGMA quick_check").Row().Scan(&v.QuickCheck); err != nil {
		v.Errors = append(v.Errors, "quick_check:"+err.Error())
	}
	return v
}
func d01Open(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE open own synthetic DB: %v", err)
	}
	return db
}
func d01Close(t *testing.T, db *gorm.DB) {
	t.Helper()
	handle, err := db.DB()
	if err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE sql handle: %v", err)
	}
	if err = handle.Close(); err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE close own synthetic DB: %v", err)
	}
}
func d01Hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestD01SMSCopyFailureRestoresOriginal(t *testing.T) {
	// Only synthetic native GORM old-model schema; never RegisterDB/config/env/real DB.
	root := t.TempDir()
	baselinePath := filepath.Join(root, "baseline-synthetic.sqlite")
	candidatePath := filepath.Join(root, "candidate-synthetic.sqlite")
	restoredPath := filepath.Join(root, "restored-synthetic-copy.sqlite")
	baseline := d01Open(t, baselinePath)
	if err := baseline.Exec("PRAGMA journal_mode=DELETE").Error; err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE journal mode: %v", err)
	}
	if err := baseline.AutoMigrate(&legacySMSVerificationCodeWithoutScene{}); err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE native old-model producer: %v", err)
	}
	fixed := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	consumedAt := fixed.Add(time.Minute)
	row := legacySMSVerificationCodeWithoutScene{ID: int32(d01SyntheticID), Phone: "D01_SYNTHETIC_PHONE", CodeHash: strings.Repeat("d", 64), Status: "consumed", ExpiresAt: fixed.Add(time.Hour), ConsumedAt: &consumedAt, RequestIP: "192.0.2.1", CreatedAt: fixed, UpdatedAt: fixed.Add(2 * time.Minute)}
	if err := baseline.Create(&row).Error; err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE seed synthetic row: %v", err)
	}
	before := d01View(baseline)
	if !before.RowPresent || before.PrimaryCount != 1 || before.LegacyTablePresent || before.QuickCheck != "ok" || len(before.Errors) != 0 {
		t.Fatalf("D01_GATE_INFRA_FAILURE invalid baseline: %+v", before)
	}
	d01Close(t, baseline)
	pristine, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE snapshot: %v", err)
	}
	if err = os.WriteFile(candidatePath, pristine, 0600); err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE candidate copy: %v", err)
	}
	candidate := d01Open(t, candidatePath)
	hits := 0
	injected := errors.New("D01_SYNTHETIC_COPY_INSERT_FAILURE")
	if err = candidate.Callback().Raw().Before("gorm:raw").Register("d01:fail-copy-only", func(tx *gorm.DB) {
		if strings.TrimSpace(tx.Statement.SQL.String()) == d01CopyStatement {
			hits++
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE callback registration: %v", err)
	}
	migrationErr := ensureSMSVerificationSchemaForDriver(candidate, "sqlite")
	candidate.Callback().Raw().Remove("d01:fail-copy-only")
	after := d01View(candidate)
	d01Close(t, candidate)
	modifiedBytes, err := os.ReadFile(candidatePath)
	if err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE candidate bytes: %v", err)
	}
	if err = os.WriteFile(restoredPath, modifiedBytes, 0600); err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE separate modified rollback copy: %v", err)
	}
	rollbackBefore, err := os.ReadFile(restoredPath)
	if err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE rollback before bytes: %v", err)
	}
	rollbackStartedFromModified := d01Hash(rollbackBefore) == d01Hash(modifiedBytes)
	if err = os.WriteFile(restoredPath, pristine, 0600); err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE separate rollback copy: %v", err)
	}
	restoredDB := d01Open(t, restoredPath)
	restored := d01View(restoredDB)
	d01Close(t, restoredDB)
	restoredBytes, err := os.ReadFile(restoredPath)
	if err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE restored bytes: %v", err)
	}
	baselineAfter, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("D01_GATE_INFRA_FAILURE original bytes: %v", err)
	}
	rollbackVerified := rollbackStartedFromModified && d01Hash(restoredBytes) == d01Hash(pristine) && reflect.DeepEqual(restored, before) && d01Hash(baselineAfter) == d01Hash(pristine)
	restoreAssertion := reflect.DeepEqual(after, before)
	message := ""
	if migrationErr != nil {
		message = migrationErr.Error()
	}
	ledger := map[string]any{"schema": "D01_TARGETED_SMS_COPY_FAILURE_PREFX_V1", "fixture_only": true, "real_history_equivalence": "UNKNOWN", "target": "ensureSMSVerificationSchemaForDriver/sqlite/copy INSERT error recovery", "callback_hits": hits, "migration_error": message, "migration_error_is_injected": errors.Is(migrationErr, injected), "baseline": before, "modified": after, "rollback": restored, "baseline_sha256": d01Hash(pristine), "modified_sha256": d01Hash(modifiedBytes), "rollback_sha256": d01Hash(restoredBytes), "rollback_before_sha256": d01Hash(rollbackBefore), "rollback_started_from_modified_copy": rollbackStartedFromModified, "original_unchanged": d01Hash(baselineAfter) == d01Hash(pristine), "copy_rollback_verified": rollbackVerified, "restore_assertion_passed": restoreAssertion, "no_product_fix": true, "D01_complete": false}
	encoded, _ := json.Marshal(ledger)
	t.Logf("D01_LEDGER_JSON %s", encoded)
	if hits != 1 || !errors.Is(migrationErr, injected) || !rollbackVerified {
		t.Fatalf("D01_GATE_INFRA_FAILURE preconditions/injection/rollback did not hold; see ledger")
	}
	if !restoreAssertion {
		t.Errorf("D01_EXPECTED_PREFX_ASSERTION_RED original primary schema and single row were not restored after injected copy INSERT failure")
	}
}
