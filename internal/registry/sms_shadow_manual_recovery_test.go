package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	smsManualPrimary = "sms_verification_codes"
	smsManualLegacy  = "sms_verification_codes__legacy_scene"
	smsManualIndex   = "idx_sms_verification_codes_scene"

	// Exact first two statements and index statement in the actual old helper:
	// e53986b0469604d3250e619477a671fc8c964c81:internal/registry/db.go
	// file SHA256 6ffc0793287b234f3dd45f9dadb94b302942845c6404a444f9a594c8f6ad2b4d.
	smsManualRename   = "ALTER TABLE sms_verification_codes RENAME TO sms_verification_codes__legacy_scene"
	smsManualCreate   = "CREATE TABLE sms_verification_codes (`id` integer PRIMARY KEY AUTOINCREMENT,`phone` varchar(32) NOT NULL,`scene` varchar(32) NOT NULL DEFAULT '',`code_hash` char(64) NOT NULL,`status` varchar(32) NOT NULL,`expires_at` datetime NOT NULL,`consumed_at` datetime,`request_ip` varchar(64),`created_at` datetime,`updated_at` datetime)"
	smsManualAddIndex = "CREATE INDEX idx_sms_verification_codes_scene ON sms_verification_codes(scene)"
)

type smsManualState struct {
	Phase                  string   `json:"phase"`
	PrimaryExists          bool     `json:"primaryExists"`
	ShadowExists           bool     `json:"shadowExists"`
	PrimaryCount           int64    `json:"primaryCount"`
	ShadowCount            int64    `json:"shadowCount"`
	PrimaryColumns         []string `json:"primaryColumns"`
	ShadowColumns          []string `json:"shadowColumns"`
	PrimaryHasScene        bool     `json:"primaryHasScene"`
	SceneIndexExists       bool     `json:"sceneIndexExists"`
	PrimaryNineSHA256      string   `json:"primaryNineSHA256"`
	ShadowNineSHA256       string   `json:"shadowNineSHA256"`
	PrimaryMatchesOriginal bool     `json:"primaryMatchesOriginal"`
	ShadowMatchesOriginal  bool     `json:"shadowMatchesOriginal"`
	SchemaSHA256           string   `json:"schemaSHA256"`
	PrimarySchemaSHA256    string   `json:"primarySchemaSHA256"`
	ShadowSchemaSHA256     string   `json:"shadowSchemaSHA256"`
	SceneIndexSQLSHA256    string   `json:"sceneIndexSQLSHA256"`
	QuickCheckOK           bool     `json:"quickCheckOK"`
}

type smsManualSchema struct {
	Type      string `gorm:"column:type"`
	Name      string `gorm:"column:name"`
	TableName string `gorm:"column:tbl_name"`
	SQL       string `gorm:"column:sql"`
}

func smsManualHash(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA: cannot encode owned observation")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func smsManualColumns(t *testing.T, db *gorm.DB, table string) []string {
	t.Helper()
	if table != smsManualPrimary && table != smsManualLegacy {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA: unexpected table identifier")
	}
	var columns []struct{ Name string }
	if err := db.Raw("PRAGMA table_info(" + table + ")").Scan(&columns).Error; err != nil {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA: owned table metadata read failed")
	}
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.Name)
	}
	return names
}

func smsManualRows(t *testing.T, db *gorm.DB, table string) (int64, string) {
	t.Helper()
	if table != smsManualPrimary && table != smsManualLegacy {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA: unexpected row table")
	}
	rows := make([]smsManualLegacyNine, 0)
	if err := db.Table(table).Select("id,phone,code_hash,status,expires_at,consumed_at,request_ip,created_at,updated_at").Order("id").Find(&rows).Error; err != nil {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA: owned nine-value read failed")
	}
	// Only the digest leaves this function; no phone, code hash, IP or raw row is logged.
	return int64(len(rows)), smsManualHash(t, rows)
}

func smsManualObserve(t *testing.T, db *gorm.DB, phase, originalNineSHA256 string) smsManualState {
	t.Helper()
	state := smsManualState{Phase: phase}
	var schema []smsManualSchema
	if err := db.Raw("SELECT type,name,tbl_name,sql FROM sqlite_master WHERE name IN (?,?,?) ORDER BY type,name", smsManualPrimary, smsManualLegacy, smsManualIndex).Scan(&schema).Error; err != nil {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA: owned schema read failed")
	}
	state.SchemaSHA256 = smsManualHash(t, schema)
	for _, entry := range schema {
		switch entry.Name {
		case smsManualPrimary:
			state.PrimaryExists = entry.Type == "table"
			state.PrimarySchemaSHA256 = smsManualHash(t, entry.SQL)
		case smsManualLegacy:
			state.ShadowExists = entry.Type == "table"
			state.ShadowSchemaSHA256 = smsManualHash(t, entry.SQL)
		case smsManualIndex:
			state.SceneIndexExists = entry.Type == "index"
			state.SceneIndexSQLSHA256 = smsManualHash(t, entry.SQL)
		}
	}
	state.PrimaryColumns = smsManualColumns(t, db, smsManualPrimary)
	state.ShadowColumns = smsManualColumns(t, db, smsManualLegacy)
	for _, column := range state.PrimaryColumns {
		if column == "scene" {
			state.PrimaryHasScene = true
		}
	}
	if state.PrimaryExists {
		state.PrimaryCount, state.PrimaryNineSHA256 = smsManualRows(t, db, smsManualPrimary)
		state.PrimaryMatchesOriginal = originalNineSHA256 != "" && state.PrimaryNineSHA256 == originalNineSHA256
	}
	if state.ShadowExists {
		state.ShadowCount, state.ShadowNineSHA256 = smsManualRows(t, db, smsManualLegacy)
		state.ShadowMatchesOriginal = originalNineSHA256 != "" && state.ShadowNineSHA256 == originalNineSHA256
	}
	var quickCheck []struct {
		QuickCheck string `gorm:"column:quick_check"`
	}
	if err := db.Raw("PRAGMA quick_check").Scan(&quickCheck).Error; err != nil {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA: owned integrity read failed")
	}
	state.QuickCheckOK = len(quickCheck) == 1 && quickCheck[0].QuickCheck == "ok"
	return state
}

// Standalone copy of the existing synthetic nine-field model, not an authentic historical DB dump.
type smsManualLegacyNine struct {
	ID         int32      `gorm:"primaryKey;autoIncrement"`
	Phone      string     `gorm:"column:phone;type:varchar(32);not null"`
	CodeHash   string     `gorm:"column:code_hash;type:char(64);not null"`
	Status     string     `gorm:"column:status;type:varchar(32);not null"`
	ExpiresAt  time.Time  `gorm:"column:expires_at;not null"`
	ConsumedAt *time.Time `gorm:"column:consumed_at"`
	RequestIP  string     `gorm:"column:request_ip;type:varchar(64)"`
	CreatedAt  time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt  time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

func (*smsManualLegacyNine) TableName() string {
	return smsManualPrimary
}

type smsManualCheck struct {
	Label     string `json:"label"`
	Category  string `json:"category"`
	Qualified bool   `json:"qualified"`
	Passed    bool   `json:"passed"`
}

type smsManualCall struct {
	Call                 int            `json:"call"`
	ErrorNonNil          bool           `json:"errorNonNil"`
	ManualRecoveryNamed  bool           `json:"manualRecoveryNamed"`
	ErrorPrivateDataFree bool           `json:"errorPrivateDataFree"`
	State                smsManualState `json:"state"`
}

func smsManualRecord(t *testing.T, checks *[]smsManualCheck, label, category string, qualified, passed bool) {
	t.Helper()
	*checks = append(*checks, smsManualCheck{label, category, qualified, passed})
	if qualified && !passed {
		t.Error("D01_SMS_FAIL_CLOSED_ASSERT " + label)
	}
}

func smsManualSameState(a, b smsManualState) bool {
	a.Phase, b.Phase = "", ""
	return reflect.DeepEqual(a, b)
}

func smsManualSyntheticNine() smsManualLegacyNine {
	fixed := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	consumed := fixed.Add(time.Minute)
	return smsManualLegacyNine{ID: 91001, Phone: "D01_SYNTHETIC_PHONE", CodeHash: strings.Repeat("d", 64), Status: "consumed", ExpiresAt: fixed.Add(time.Hour), ConsumedAt: &consumed, RequestIP: "192.0.2.1", CreatedAt: fixed, UpdatedAt: fixed.Add(2 * time.Minute)}
}

func smsManualOpen(t *testing.T) (*gorm.DB, func() bool) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "synthetic-manual-recovery.sqlite")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA owned SQLite open failed")
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA owned pool unavailable")
	}
	raw.SetMaxOpenConns(1)
	raw.SetMaxIdleConns(1)
	closed := false
	closeOwned := func() bool {
		if closed {
			return true
		}
		if err := raw.Close(); err != nil {
			return false
		}
		closed = true
		return true
	}
	t.Cleanup(func() {
		if !closeOwned() {
			t.Error("SMS_MANUAL_CONTRACT_INFRA owned pool cleanup failed")
		}
	})
	if err := db.Exec("PRAGMA journal_mode=DELETE").Error; err != nil {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA owned journal setup failed")
	}
	return db, closeOwned
}

func smsManualInterruptedInput(t *testing.T, populated bool) (*gorm.DB, func() bool, string) {
	t.Helper()
	db, closeOwned := smsManualOpen(t)
	if err := db.AutoMigrate(&smsManualLegacyNine{}); err != nil {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA synthetic nine-field model setup failed")
	}
	if populated {
		row := smsManualSyntheticNine()
		if err := db.Create(&row).Error; err != nil {
			t.Fatal("SMS_MANUAL_CONTRACT_INFRA fixed synthetic row setup failed")
		}
	}
	old := smsManualObserve(t, db, "synthetic-nine-field-source", "")
	wantCount := int64(0)
	if populated {
		wantCount = 1
	}
	if old.PrimaryCount != wantCount || len(old.PrimaryColumns) != 9 || old.PrimaryHasScene || old.ShadowExists || old.SceneIndexExists || !old.QuickCheckOK {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA old-model prerequisite failed")
	}
	// Exact e539 SQL prefix plus its scene-present restart index action.
	// This constructs an SQL boundary, not SIGKILL, a real incident, or a historical DB dump.
	for _, statement := range []string{smsManualRename, smsManualCreate, smsManualAddIndex} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal("SMS_MANUAL_CONTRACT_INFRA exact old SQL boundary setup failed")
		}
	}
	state := smsManualObserve(t, db, "qualified-interrupted-input", old.PrimaryNineSHA256)
	if !state.PrimaryExists || !state.ShadowExists || !state.PrimaryHasScene || !state.SceneIndexExists || state.PrimaryCount != 0 || state.ShadowCount != wantCount || !state.ShadowMatchesOriginal || !state.QuickCheckOK {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA precise target prerequisite failed")
	}
	return db, closeOwned, old.PrimaryNineSHA256
}

func smsManualErrorSemantics(err error) (bool, bool) {
	if err == nil {
		return false, false
	}
	text := err.Error()
	lower := strings.ToLower(text)
	named := (strings.Contains(lower, "manual") && strings.Contains(lower, "recover")) || (strings.Contains(text, "人工") && strings.Contains(text, "恢复"))
	privateFree := !strings.Contains(text, "D01_SYNTHETIC_PHONE") && !strings.Contains(text, strings.Repeat("d", 64)) && !strings.Contains(text, "192.0.2.1")
	return named, privateFree
}

func smsManualEmit(t *testing.T, scenario string, before smsManualState, calls []smsManualCall, checks []smsManualCheck, inspectionHits int, closeOwned func() bool) {
	t.Helper()
	closed := closeOwned()
	if !closed {
		t.Error("SMS_MANUAL_CONTRACT_INFRA explicit pool close failed")
	}
	out := struct {
		SchemaVersion  int              `json:"schemaVersion"`
		Scenario       string           `json:"scenario"`
		Scope          string           `json:"scope"`
		Before         smsManualState   `json:"before"`
		Calls          []smsManualCall  `json:"calls"`
		Checks         []smsManualCheck `json:"checks"`
		InspectionHits int              `json:"inspectionHits"`
		PoolClosed     bool             `json:"poolClosed"`
	}{1, scenario, "synthetic SQL boundary; not real incident or authentic historical DB; no automatic recovery", before, calls, checks, inspectionHits, closed}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA observation encoding failed")
	}
	fmt.Printf("D01_SMS_FAIL_CLOSED_OBSERVATION %s\n", encoded)
}

// Approved policy applies only to new scene+index / empty primary / populated shadow.
func TestD01SMSShadowRequiresManualRecovery(t *testing.T) {
	db, closeOwned, originalNine := smsManualInterruptedInput(t, true)
	before := smsManualObserve(t, db, "before-current", originalNine)
	var checks []smsManualCheck
	var calls []smsManualCall
	for call := 1; call <= 2; call++ {
		err := ensureSMSVerificationSchemaForDriver(db, "sqlite")
		named, privateFree := smsManualErrorSemantics(err)
		after := smsManualObserve(t, db, fmt.Sprintf("after-current-%d", call), originalNine)
		calls = append(calls, smsManualCall{call, err != nil, named, privateFree, after})
		label := fmt.Sprintf("call%d ", call)
		smsManualRecord(t, &checks, label+"approved ambiguous input returns non-nil error", "primary", true, err != nil)
		smsManualRecord(t, &checks, label+"error explicitly names manual recovery", "dependent-on-error", err != nil, named)
		smsManualRecord(t, &checks, label+"error contains no synthetic private values", "dependent-on-error", err != nil, privateFree)
		smsManualRecord(t, &checks, label+"both tables schema counts full-nine hashes and integrity remain exact", "independent-invariant", true, smsManualSameState(before, after))
	}
	smsManualEmit(t, "approved-primary0-shadow1-two-calls", before, calls, checks, 0, closeOwned)
}

func TestD01SMSShadowAllowsUnaffectedStates(t *testing.T) {
	for _, scenario := range []string{"new-empty-no-shadow", "new-populated-no-shadow", "empty-shadow", "populated-primary-and-shadow"} {
		t.Run(scenario, func(t *testing.T) {
			var db *gorm.DB
			var closeOwned func() bool
			originalNine := ""
			if scenario == "empty-shadow" || scenario == "populated-primary-and-shadow" {
				db, closeOwned, originalNine = smsManualInterruptedInput(t, scenario == "populated-primary-and-shadow")
				if scenario == "populated-primary-and-shadow" {
					row := smsManualSyntheticNine()
					if err := db.Create(&row).Error; err != nil {
						t.Fatal("SMS_MANUAL_CONTRACT_INFRA populated-primary control seed failed")
					}
				}
			} else {
				db, closeOwned = smsManualOpen(t)
				if err := db.AutoMigrate(&entity.SMSVerificationCode{}); err != nil {
					t.Fatal("SMS_MANUAL_CONTRACT_INFRA current native schema setup failed")
				}
				if scenario == "new-populated-no-shadow" {
					old := smsManualSyntheticNine()
					row := entity.SMSVerificationCode{ID: old.ID, Phone: old.Phone, Scene: "login", CodeHash: old.CodeHash, Status: old.Status, ExpiresAt: old.ExpiresAt, ConsumedAt: old.ConsumedAt, RequestIP: old.RequestIP, CreatedAt: old.CreatedAt, UpdatedAt: old.UpdatedAt}
					if err := db.Create(&row).Error; err != nil {
						t.Fatal("SMS_MANUAL_CONTRACT_INFRA current native row setup failed")
					}
				}
			}
			before := smsManualObserve(t, db, "before-control", originalNine)
			if !before.PrimaryHasScene || !before.SceneIndexExists || !before.QuickCheckOK {
				t.Fatal("SMS_MANUAL_CONTRACT_INFRA unaffected-control schema prerequisite failed")
			}
			wantPrimary, wantShadow := int64(0), int64(0)
			if scenario == "new-populated-no-shadow" || scenario == "populated-primary-and-shadow" {
				wantPrimary = 1
			}
			if scenario == "populated-primary-and-shadow" {
				wantShadow = 1
			}
			wantShadowTable := scenario == "empty-shadow" || scenario == "populated-primary-and-shadow"
			if before.PrimaryCount != wantPrimary || before.ShadowCount != wantShadow || before.ShadowExists != wantShadowTable {
				t.Fatal("SMS_MANUAL_CONTRACT_INFRA unaffected-control exact row prerequisite failed")
			}
			err := ensureSMSVerificationSchemaForDriver(db, "sqlite")
			after := smsManualObserve(t, db, "after-control", originalNine)
			named, privateFree := smsManualErrorSemantics(err)
			var checks []smsManualCheck
			smsManualRecord(t, &checks, "unaffected input retains original nil result", "positive-control", true, err == nil)
			smsManualRecord(t, &checks, "unaffected input schema counts values and integrity remain exact", "positive-control", true, smsManualSameState(before, after))
			smsManualEmit(t, scenario, before, []smsManualCall{{1, err != nil, named, privateFree, after}}, checks, 0, closeOwned)
		})
	}
}

func TestD01SMSShadowInspectionErrorPreservesState(t *testing.T) {
	db, closeOwned, originalNine := smsManualInterruptedInput(t, true)
	before := smsManualObserve(t, db, "before-inspection-fault", originalNine)
	injected := errors.New("synthetic shadow inspection read failure")
	hits := 0
	callback := func(tx *gorm.DB) {
		sql := strings.ToLower(strings.TrimSpace(tx.Statement.SQL.String()))
		read := strings.HasPrefix(sql, "select") || strings.HasPrefix(sql, "pragma") || strings.HasPrefix(sql, "with")
		shadow := strings.Contains(sql, smsManualLegacy)
		for _, value := range tx.Statement.Vars {
			if value == smsManualLegacy {
				shadow = true
			}
		}
		if read && shadow {
			hits++
			tx.AddError(injected)
		}
	}
	const callbackName = "d01:manual-recovery-shadow-read-fault"
	if err := db.Callback().Row().Before("gorm:row").Register(callbackName, callback); err != nil {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA row callback install failed")
	}
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, callback); err != nil {
		t.Fatal("SMS_MANUAL_CONTRACT_INFRA query callback install failed")
	}
	removed := false
	remove := func() {
		if removed {
			return
		}
		if err := db.Callback().Row().Remove(callbackName); err != nil {
			t.Error("SMS_MANUAL_CONTRACT_INFRA row callback cleanup failed")
		}
		if err := db.Callback().Query().Remove(callbackName); err != nil {
			t.Error("SMS_MANUAL_CONTRACT_INFRA query callback cleanup failed")
		}
		removed = true
	}
	t.Cleanup(remove)
	err := ensureSMSVerificationSchemaForDriver(db, "sqlite")
	remove()
	after := smsManualObserve(t, db, "after-inspection-fault", originalNine)
	named, privateFree := smsManualErrorSemantics(err)
	var checks []smsManualCheck
	smsManualRecord(t, &checks, "current helper actually attempts shadow inspection", "primary", true, hits > 0)
	smsManualRecord(t, &checks, "inspection failure remains in returned error chain", "dependent-on-interception", hits > 0, err != nil && errors.Is(err, injected))
	smsManualRecord(t, &checks, "inspection failure leaves both tables schema counts nine values and integrity exact", "independent-invariant", true, smsManualSameState(before, after))
	smsManualEmit(t, "injected-owned-shadow-read-error", before, []smsManualCall{{1, err != nil, named, privateFree, after}}, checks, hits, closeOwned)
}
