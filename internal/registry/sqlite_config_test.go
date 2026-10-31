package registry

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestSQLiteProductionDSNAddsPragmasWithoutChangingMemoryDSN(t *testing.T) {
	if got := sqliteProductionDSN("file:test.db?cache=shared"); !strings.Contains(got, "&_pragma=busy_timeout(5000)") || !strings.Contains(got, "_pragma=journal_mode(WAL)") {
		t.Fatalf("production DSN missing pragmas: %s", got)
	}
	memory := "file:test?mode=memory&cache=shared"
	if got := sqliteProductionDSN(memory); got != memory {
		t.Fatalf("memory DSN changed: got %s", got)
	}
}

func TestPrepareStatementsRemainEnabledForMySQLOnly(t *testing.T) {
	if prepareStatementsFor("mysql://db", "") != true {
		t.Fatal("mysql should keep prepared statements enabled")
	}
	if prepareStatementsFor("", "mysql://db") != true {
		t.Fatal("SQL_DSN should keep prepared statements enabled")
	}
	if prepareStatementsFor("", "") {
		t.Fatal("sqlite should disable prepared statements")
	}
}

func TestConfigureSQLiteConnectionUsesWALBusyTimeoutAndSingleConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiku.db")
	db, err := gorm.Open(sqliteQuietDialector{Dialector: sqlite.Open(sqliteProductionDSN(path))}, &gorm.Config{PrepareStmt: false})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := configureSQLiteConnection(db, path); err != nil {
		t.Fatalf("configure sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if stats := sqlDB.Stats(); stats.MaxOpenConnections != 1 {
		t.Fatalf("max open connections = %d, want 1", stats.MaxOpenConnections)
	}
	var journalMode, busyTimeout string
	if err := db.Raw("PRAGMA journal_mode").Scan(&journalMode).Error; err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if err := db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error; err != nil {
		t.Fatalf("read busy timeout: %v", err)
	}
	if !strings.EqualFold(strings.TrimSpace(journalMode), "wal") {
		t.Fatalf("journal mode = %q, want wal", journalMode)
	}
	if strings.TrimSpace(busyTimeout) != "5000" {
		t.Fatalf("busy timeout = %q, want 5000", busyTimeout)
	}
}

func TestEnsureBillingRecordIndexesIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&entity.BillingRecord{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := ensureBillingRecordIndexes(db); err != nil {
		t.Fatalf("create indexes: %v", err)
	}
	if err := ensureBillingRecordIndexes(db); err != nil {
		t.Fatalf("repeat indexes: %v", err)
	}
	wants := map[string][]string{
		"idx_billing_records_dedup":       {"user_id", "request_id", "type", "hidden_from_statement", "id"},
		"idx_billing_records_aggregation": {"type", "request_id", "user_id", "hidden_from_statement"},
	}
	for index, wantColumns := range wants {
		if !db.Migrator().HasIndex(&entity.BillingRecord{}, index) {
			t.Fatalf("missing index %s", index)
		}
		var rows []struct {
			Seq  int    `gorm:"column:seqno"`
			Name string `gorm:"column:name"`
		}
		if err := db.Raw(fmt.Sprintf("PRAGMA index_info(\"%s\")", index)).Scan(&rows).Error; err != nil {
			t.Fatalf("read index %s: %v", index, err)
		}
		columns := make([]string, len(rows))
		for _, row := range rows {
			if row.Seq >= 0 && row.Seq < len(columns) {
				columns[row.Seq] = row.Name
			}
		}
		if strings.Join(columns, ",") != strings.Join(wantColumns, ",") {
			t.Fatalf("index %s columns = %v, want %v", index, columns, wantColumns)
		}
	}
}
