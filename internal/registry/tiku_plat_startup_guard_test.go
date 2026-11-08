package registry

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// This driver is an in-process SQL fixture, not a MySQL server. Its migrator
// answers only the two presence checks; the production DROP and metadata query
// still pass through database/sql and the real ensureTikuPlatColumnDropped.
type platGuardSQLFixture struct {
	dialect                     string
	nullable, extra, generation string
	defaultValue                driver.Value
	dropErr, metadataErr        error
	missingTable, missingColumn bool
	emptyMetadata               bool
	dropCalls, metadataCalls    int
}

type platGuardConnector struct{ fixture *platGuardSQLFixture }
type platGuardDriver struct{}
type platGuardConn struct{ fixture *platGuardSQLFixture }
type platGuardRows struct {
	columns []string
	values  []driver.Value
	done    bool
}

func (c platGuardConnector) Connect(context.Context) (driver.Conn, error) {
	return &platGuardConn{fixture: c.fixture}, nil
}
func (platGuardConnector) Driver() driver.Driver { return platGuardDriver{} }
func (platGuardDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fixture requires connector")
}
func (*platGuardConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected fixture Prepare")
}
func (*platGuardConn) Close() error { return nil }
func (*platGuardConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected fixture transaction")
}
func (c *platGuardConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if query != "ALTER TABLE tiku DROP COLUMN plat" {
		return nil, fmt.Errorf("unexpected fixture Exec: %s", query)
	}
	c.fixture.dropCalls++
	return driver.RowsAffected(0), c.fixture.dropErr
}
func (c *platGuardConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query == "select sqlite_version()" {
		return &platGuardRows{columns: []string{"sqlite_version()"}, values: []driver.Value{"3.41.2"}}, nil
	}
	const metadataQuery = "SELECT IS_NULLABLE, COLUMN_DEFAULT, EXTRA, GENERATION_EXPRESSION FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?"
	if query != metadataQuery || len(args) != 2 || args[0].Value != "tiku" || args[1].Value != "plat" {
		return nil, fmt.Errorf("unexpected fixture Query: %s args=%v", query, args)
	}
	c.fixture.metadataCalls++
	if c.fixture.metadataErr != nil {
		return nil, c.fixture.metadataErr
	}
	return &platGuardRows{
		columns: []string{"IS_NULLABLE", "COLUMN_DEFAULT", "EXTRA", "GENERATION_EXPRESSION"},
		values:  []driver.Value{c.fixture.nullable, c.fixture.defaultValue, c.fixture.extra, c.fixture.generation},
		done:    c.fixture.emptyMetadata,
	}, nil
}
func (r *platGuardRows) Columns() []string { return r.columns }
func (*platGuardRows) Close() error        { return nil }
func (r *platGuardRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	copy(dest, r.values)
	r.done = true
	return nil
}

type platGuardDialector struct {
	sqlite.Dialector
	fixture *platGuardSQLFixture
}
type platGuardMigrator struct {
	gorm.Migrator
	fixture *platGuardSQLFixture
}

func (d platGuardDialector) Name() string { return d.fixture.dialect }
func (d platGuardDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return platGuardMigrator{Migrator: d.Dialector.Migrator(db), fixture: d.fixture}
}
func (m platGuardMigrator) HasTable(value any) bool {
	return value == "tiku" && !m.fixture.missingTable
}
func (m platGuardMigrator) HasColumn(value any, name string) bool {
	return value == "tiku" && name == "plat" && !m.fixture.missingColumn
}

func openPlatGuardSQLFixture(t *testing.T, fixture *platGuardSQLFixture) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(platGuardConnector{fixture: fixture})
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(platGuardDialector{
		Dialector: sqlite.Dialector{Conn: pool}, fixture: fixture,
	}, &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open SQL fixture: %v", err)
	}
	return db
}

func TestPlatStartupGuardSQLFixture(t *testing.T) {
	dropDenied := errors.New("fixture ALTER command denied")
	metadataDenied := errors.New("fixture metadata read denied")
	tests := []struct {
		name    string
		change  func(*platGuardSQLFixture)
		guard   bool
		drops   int
		queries int
	}{
		{name: "confirmed_mandatory_old_column", guard: true, drops: 1, queries: 1},
		{name: "nullable", change: func(f *platGuardSQLFixture) { f.nullable = "YES" }, drops: 1, queries: 1},
		{name: "unknown_nullable", change: func(f *platGuardSQLFixture) { f.nullable = "" }, drops: 1, queries: 1},
		{name: "empty_default_is_a_default", change: func(f *platGuardSQLFixture) { f.defaultValue = "" }, drops: 1, queries: 1},
		{name: "zero_default", change: func(f *platGuardSQLFixture) { f.defaultValue = []byte("0") }, drops: 1, queries: 1},
		{name: "auto_increment", change: func(f *platGuardSQLFixture) { f.extra = "auto_increment" }, drops: 1, queries: 1},
		{name: "virtual_generated", change: func(f *platGuardSQLFixture) { f.extra = "VIRTUAL GENERATED" }, drops: 1, queries: 1},
		{name: "stored_generated", change: func(f *platGuardSQLFixture) { f.extra = "STORED GENERATED" }, drops: 1, queries: 1},
		{name: "unknown_extra", change: func(f *platGuardSQLFixture) { f.extra = "DEFAULT_GENERATED" }, drops: 1, queries: 1},
		{name: "generation_expression", change: func(f *platGuardSQLFixture) { f.generation = "(-1)" }, drops: 1, queries: 1},
		{name: "metadata_read_error", change: func(f *platGuardSQLFixture) { f.metadataErr = metadataDenied }, drops: 1, queries: 1},
		{name: "metadata_row_missing", change: func(f *platGuardSQLFixture) { f.emptyMetadata = true }, drops: 1, queries: 1},
		{name: "sqlite_drop_error", change: func(f *platGuardSQLFixture) { f.dialect = "sqlite" }, drops: 1},
		{name: "other_dialect_drop_error", change: func(f *platGuardSQLFixture) { f.dialect = "postgres" }, drops: 1},
		{name: "drop_succeeds", change: func(f *platGuardSQLFixture) { f.dropErr = nil }, drops: 1},
		{name: "table_missing", change: func(f *platGuardSQLFixture) { f.missingTable = true }},
		{name: "column_missing", change: func(f *platGuardSQLFixture) { f.missingColumn = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &platGuardSQLFixture{dialect: "mysql", nullable: "NO", dropErr: dropDenied}
			if tc.change != nil {
				tc.change(fixture)
			}
			err := ensureTikuPlatColumnDropped(openPlatGuardSQLFixture(t, fixture))
			if tc.guard {
				if !errors.Is(err, dropDenied) || !strings.Contains(err.Error(), "MySQL tiku.plat") {
					t.Fatalf("confirmed mandatory MySQL old column must return guard preserving DROP cause; got %v", err)
				}
			} else if err != nil {
				t.Fatalf("existing warn/retry policy expanded: %v", err)
			}
			if fixture.dropCalls != tc.drops || fixture.metadataCalls != tc.queries {
				t.Fatalf("SQL call scope: DROP=%d metadata=%d; want %d/%d", fixture.dropCalls, fixture.metadataCalls, tc.drops, tc.queries)
			}
		})
	}
}

func TestPlatStartupGuardFailedDropRetries(t *testing.T) {
	dropDenied := errors.New("fixture ALTER command denied")
	fixture := &platGuardSQLFixture{dialect: "mysql", nullable: "NO", dropErr: dropDenied}
	db := openPlatGuardSQLFixture(t, fixture)
	for attempt := 1; attempt <= 2; attempt++ {
		if err := ensureTikuPlatColumnDropped(db); !errors.Is(err, dropDenied) {
			t.Fatalf("attempt %d must preserve denied DROP for retry; got %v", attempt, err)
		}
	}
	if fixture.dropCalls != 2 || fixture.metadataCalls != 2 {
		t.Fatalf("failed DROP must not set completion marker: DROP=%d metadata=%d", fixture.dropCalls, fixture.metadataCalls)
	}
}
