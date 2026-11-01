package registry

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestSQLiteProductionDSNAddsPragmasWithoutChangingMemoryDSN(t *testing.T) {
	if got := sqliteProductionDSN("file:test.db?cache=shared"); !strings.Contains(got, "_txlock=immediate") || !strings.Contains(got, "&_pragma=busy_timeout(5000)") || !strings.Contains(got, "_pragma=journal_mode(WAL)") {
		t.Fatalf("production DSN missing pragmas: %s", got)
	}
	memory := "file:test?mode=memory&cache=shared"
	if got := sqliteProductionDSN(memory); got != memory {
		t.Fatalf("memory DSN changed: got %s", got)
	}
}

// A separate process cannot share the admission scheduler. Exercise two real
// SQLite connections so a read-to-write upgrade cannot hide behind one pool.
func TestSQLiteImmediateTransactionsAvoidBusySnapshotUpgrade(t *testing.T) {
	for _, immediate := range []bool{false, true} {
		t.Run(fmt.Sprintf("immediate=%t", immediate), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "snapshot.db")
			dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
			if immediate {
				dsn = sqliteProductionDSN(path)
			}
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if _, err := db.Exec("CREATE TABLE counter (id INTEGER PRIMARY KEY, value INTEGER); INSERT INTO counter VALUES (1, 0)"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			first, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			second, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			tx, err := first.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var value int
			if err := tx.QueryRowContext(ctx, "SELECT value FROM counter WHERE id = 1").Scan(&value); err != nil {
				t.Fatal(err)
			}
			if !immediate {
				if _, err := second.ExecContext(ctx, "UPDATE counter SET value = value + 1 WHERE id = 1"); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, "UPDATE counter SET value = value + 1 WHERE id = 1"); err == nil || (!strings.Contains(err.Error(), "SQLITE_BUSY") && !strings.Contains(err.Error(), "database is locked")) {
					t.Fatalf("deferred upgrade error = %v, want SQLITE_BUSY/database is locked", err)
				}
				return
			}
			started := make(chan struct{})
			otherWrite := make(chan error, 1)
			go func() {
				close(started)
				_, err := second.ExecContext(ctx, "UPDATE counter SET value = value + 1 WHERE id = 1")
				otherWrite <- err
			}()
			<-started
			select {
			case err := <-otherWrite:
				t.Fatalf("other writer did not wait for transaction: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			if _, err := tx.ExecContext(ctx, "UPDATE counter SET value = value + 1 WHERE id = 1"); err != nil {
				t.Fatalf("immediate read-to-write transaction failed: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-otherWrite; err != nil {
				t.Fatalf("other writer failed after transaction commit: %v", err)
			}
			if err := first.QueryRowContext(ctx, "SELECT value FROM counter WHERE id = 1").Scan(&value); err != nil || value != 2 {
				t.Fatalf("committed counter = %d, err = %v, want 2", value, err)
			}
		})
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
