package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const d01rpPrimary = "sms_verification_codes"
const d01rpShadow = "sms_verification_codes__legacy_scene"

// Same synthetic nine-column input shape used by the existing D01 fixtures.
// This is not an authentic historical database or a physical crash simulation.
type d01rpNine struct {
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

func (*d01rpNine) TableName() string { return d01rpPrimary }

type d01rpColumn struct {
	CID     int     `gorm:"column:cid"`
	Name    string  `gorm:"column:name"`
	Type    string  `gorm:"column:type"`
	NotNull int     `gorm:"column:notnull"`
	Default *string `gorm:"column:dflt_value"`
	PK      int     `gorm:"column:pk"`
}

type d01rpTableState struct {
	Exists          bool          `json:"exists"`
	Columns         []d01rpColumn `json:"columns"`
	Count           int           `json:"count"`
	NineTypedSHA256 string        `json:"nineTypedSHA256"`
}

type d01rpState struct {
	SchemaSHA256 string          `json:"schemaSHA256"`
	Primary      d01rpTableState `json:"primary"`
	Shadow       d01rpTableState `json:"shadow"`
	QuickCheck   string          `json:"quickCheck"`
}

type d01rpCall struct {
	Call      int        `json:"call"`
	ErrorNil  bool       `json:"errorNil"`
	ErrorText string     `json:"literalError"`
	Unchanged bool       `json:"stateUnchanged"`
	After     d01rpState `json:"after"`
}

func d01rpHash(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal("observation infrastructure: cannot encode typed state")
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func d01rpTyped(value any) [2]string {
	switch v := value.(type) {
	case nil:
		return [2]string{"null", ""}
	case []byte:
		return [2]string{"blob", hex.EncodeToString(v)}
	case time.Time:
		return [2]string{"time.Time", v.Format(time.RFC3339Nano)}
	default:
		return [2]string{fmt.Sprintf("%T", value), fmt.Sprint(value)}
	}
}

func d01rpObserveTable(t *testing.T, db *gorm.DB, table string) d01rpTableState {
	t.Helper()
	if table != d01rpPrimary && table != d01rpShadow {
		t.Fatal("observation infrastructure: invalid fixed table")
	}
	state := d01rpTableState{Columns: []d01rpColumn{}}
	var exists int
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&exists).Error; err != nil {
		t.Fatal(err)
	}
	state.Exists = exists == 1
	values := make([][][2]string, 0)
	if state.Exists {
		if err := db.Raw("PRAGMA table_info(" + table + ")").Scan(&state.Columns).Error; err != nil {
			t.Fatal(err)
		}
		rows, err := db.Raw("SELECT id,phone,code_hash,status,expires_at,consumed_at,request_ip,created_at,updated_at FROM " + table + " ORDER BY id").Rows()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw [9]any
			pointers := make([]any, 9)
			for i := range raw {
				pointers[i] = &raw[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			typed := make([][2]string, 9)
			for i, value := range raw {
				typed[i] = d01rpTyped(value)
			}
			values = append(values, typed)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	state.Count = len(values)
	state.NineTypedSHA256 = d01rpHash(t, values)
	return state
}

func d01rpObserve(t *testing.T, db *gorm.DB) d01rpState {
	t.Helper()
	var schema []struct {
		Type  string  `gorm:"column:type"`
		Name  string  `gorm:"column:name"`
		Table string  `gorm:"column:tbl_name"`
		SQL   *string `gorm:"column:sql"`
	}
	if err := db.Raw("SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name").Scan(&schema).Error; err != nil {
		t.Fatal(err)
	}
	var quick string
	if err := db.Raw("PRAGMA quick_check").Scan(&quick).Error; err != nil {
		t.Fatal(err)
	}
	return d01rpState{SchemaSHA256: d01rpHash(t, schema), Primary: d01rpObserveTable(t, db, d01rpPrimary), Shadow: d01rpObserveTable(t, db, d01rpShadow), QuickCheck: quick}
}

func TestSMSRenameOnlyShadowRequiresManualRecovery(t *testing.T) {
	for _, scenario := range []string{"rename_only_primary_absent_shadow_populated", "normal_empty_no_shadow"} {
		t.Run(scenario, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "owned-contract.sqlite")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			pool.SetMaxOpenConns(1)
			pool.SetMaxIdleConns(1)
			closed := false
			t.Cleanup(func() {
				if !closed {
					if err := pool.Close(); err != nil {
						t.Error("owned pool cleanup:", err)
					}
				}
			})
			originalNine := ""
			if scenario == "rename_only_primary_absent_shadow_populated" {
				if err := db.AutoMigrate(&d01rpNine{}); err != nil {
					t.Fatal(err)
				}
				fixed := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
				row := d01rpNine{ID: 91001, Phone: "D01_SYNTHETIC_PHONE", CodeHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", Status: "consumed", ExpiresAt: fixed.Add(time.Hour), RequestIP: "192.0.2.1", CreatedAt: fixed, UpdatedAt: fixed.Add(time.Minute)}
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
				originalNine = d01rpObserveTable(t, db, d01rpPrimary).NineTypedSHA256
				if err := db.Exec("ALTER TABLE sms_verification_codes RENAME TO sms_verification_codes__legacy_scene").Error; err != nil {
					t.Fatal(err)
				}
			}
			before := d01rpObserve(t, db)
			if before.QuickCheck != "ok" || before.Primary.Exists {
				t.Fatal("unqualified input: expected absent primary and sound owned DB")
			}
			if scenario == "rename_only_primary_absent_shadow_populated" {
				if !before.Shadow.Exists || before.Shadow.Count != 1 || len(before.Shadow.Columns) != 9 || before.Shadow.NineTypedSHA256 != originalNine {
					t.Fatal("unqualified synthetic historic rename prefix")
				}
			} else if before.Shadow.Exists {
				t.Fatal("unqualified normal empty control")
			}
			calls := make([]d01rpCall, 0, 2)
			for n := 1; n <= 2; n++ {
				// User-approved fail-closed policy: retain all bytes and report manual recovery.
				err := ensureSMSVerificationSchemaForDriver(db, "sqlite")
				if scenario == "rename_only_primary_absent_shadow_populated" {
					if err == nil {
						t.Errorf("approved rename-only shadow must require manual recovery on call %d; got <nil>", n)
					} else if !strings.Contains(err.Error(), "primary is absent") || !strings.Contains(err.Error(), "legacy shadow") || !strings.Contains(err.Error(), "manual recovery required") {
						t.Errorf("rename-only error lacks explicit approved recovery diagnosis on call %d: %v", n, err)
					}
				} else if err != nil {
					t.Errorf("normal empty database without shadow must remain allowed on call %d: %v", n, err)
				}
				after := d01rpObserve(t, db)
				unchanged := reflect.DeepEqual(before, after)
				if !unchanged || after.QuickCheck != "ok" {
					t.Error("state/whole schema/nine typed values changed during helper call", n)
				}
				calls = append(calls, d01rpCall{Call: n, ErrorNil: err == nil, ErrorText: fmt.Sprintf("%v", err), Unchanged: unchanged, After: after})
			}
			if err := pool.Close(); err != nil {
				t.Error("explicit owned pool close:", err)
			} else {
				closed = true
			}
			encoded, err := json.Marshal(struct {
				Scenario     string      `json:"scenario"`
				Before       d01rpState  `json:"before"`
				Calls        []d01rpCall `json:"calls"`
				PoolClosed   bool        `json:"poolClosed"`
				PolicyChosen bool        `json:"policyChosen"`
			}{scenario, before, calls, closed, true})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("D01_RENAME_ONLY_CONTRACT %s", encoded)
		})
	}
}
