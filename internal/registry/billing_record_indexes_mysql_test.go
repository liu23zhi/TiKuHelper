package registry

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// billingIndexMySQLConnector exercises the real MySQL dialect and migration SQL
// entirely in process. It only models index metadata and records DDL; it does
// not claim to validate a MySQL server's optimizer or storage engine.
type billingIndexMySQLConnector struct {
	indexes    map[string][]string
	statements []string
}

func (c *billingIndexMySQLConnector) Connect(context.Context) (driver.Conn, error) {
	return &billingIndexMySQLConn{state: c}, nil
}

func (c *billingIndexMySQLConnector) Driver() driver.Driver { return billingIndexMySQLDriver{} }

type billingIndexMySQLDriver struct{}

func (billingIndexMySQLDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("this test driver must only be opened through its local connector")
}

type billingIndexMySQLConn struct{ state *billingIndexMySQLConnector }

func (*billingIndexMySQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statements are not part of this migration")
}
func (*billingIndexMySQLConn) Close() error              { return nil }
func (*billingIndexMySQLConn) Begin() (driver.Tx, error) { return billingIndexMySQLTx{}, nil }

type billingIndexMySQLTx struct{}

func (billingIndexMySQLTx) Commit() error   { return nil }
func (billingIndexMySQLTx) Rollback() error { return nil }

func (c *billingIndexMySQLConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.statements = append(c.state.statements, query)
	upper := strings.ToUpper(query)
	switch {
	case upper == "SELECT DATABASE()", strings.Contains(upper, "FROM INFORMATION_SCHEMA.SCHEMATA"):
		return &billingIndexMySQLRows{columns: []string{"database"}, values: [][]driver.Value{{"billing_index_test"}}}, nil
	case strings.Contains(upper, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES"):
		return &billingIndexMySQLRows{columns: []string{"count(*)"}, values: [][]driver.Value{{int64(1)}}}, nil
	case strings.Contains(upper, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS"):
		count := int64(0)
		if _, exists := c.state.indexes[fmt.Sprint(args[2].Value)]; exists {
			count = 1
		}
		return &billingIndexMySQLRows{columns: []string{"count(*)"}, values: [][]driver.Value{{count}}}, nil
	case strings.Contains(upper, "SELECT SEQ_IN_INDEX, COLUMN_NAME FROM INFORMATION_SCHEMA.STATISTICS"):
		rows := &billingIndexMySQLRows{columns: []string{"SEQ_IN_INDEX", "COLUMN_NAME"}}
		for i, column := range c.state.indexes[fmt.Sprint(args[1].Value)] {
			rows.values = append(rows.values, []driver.Value{int64(i + 1), column})
		}
		return rows, nil
	default:
		return nil, fmt.Errorf("unexpected MySQL migration query: %s", query)
	}
}

func (c *billingIndexMySQLConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.state.statements = append(c.state.statements, query)
	switch {
	case strings.HasPrefix(query, "CREATE INDEX "):
		parts := strings.SplitN(strings.TrimPrefix(query, "CREATE INDEX "), " ON ", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid index DDL: %s", query)
		}
		name := strings.Trim(parts[0], "`")
		start, end := strings.Index(parts[1], "("), strings.LastIndex(parts[1], ")")
		if start < 0 || end < start {
			return nil, fmt.Errorf("invalid index columns: %s", query)
		}
		columns := strings.Split(parts[1][start+1:end], ",")
		for i := range columns {
			columns[i] = strings.Trim(strings.TrimSpace(columns[i]), "`")
		}
		c.state.indexes[name] = columns
	case strings.HasPrefix(query, "DELETE FROM `system_settings` WHERE `key` = "), strings.HasPrefix(query, "INSERT INTO `system_settings` "):
		// The actual GORM SQL must quote the MySQL reserved column key.
	default:
		return nil, fmt.Errorf("unexpected MySQL migration statement: %s", query)
	}
	return billingIndexMySQLResult{}, nil
}

type billingIndexMySQLResult struct{}

func (billingIndexMySQLResult) LastInsertId() (int64, error) { return 1, nil }
func (billingIndexMySQLResult) RowsAffected() (int64, error) { return 1, nil }

type billingIndexMySQLRows struct {
	columns []string
	values  [][]driver.Value
	current int
}

func (r *billingIndexMySQLRows) Columns() []string { return r.columns }
func (*billingIndexMySQLRows) Close() error        { return nil }
func (r *billingIndexMySQLRows) Next(values []driver.Value) error {
	if r.current >= len(r.values) {
		return io.EOF
	}
	copy(values, r.values[r.current])
	r.current++
	return nil
}

func TestBillingUsageIndexMySQLMigrationSQL(t *testing.T) {
	state := &billingIndexMySQLConnector{indexes: map[string][]string{
		"idx_billing_records_dedup":       {"user_id", "request_id", "type", "hidden_from_statement", "id"},
		"idx_billing_records_aggregation": {"type", "request_id", "user_id", "hidden_from_statement"},
	}}
	sqlDB := sql.OpenDB(state)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureBillingRecordIndexes(db); err != nil {
		t.Fatalf("MySQL migration SQL failed: %v", err)
	}
	want := "CREATE INDEX `idx_billing_records_usage` ON `billing_records` (`type`, `user_id`, `request_id`, `hidden_from_statement`, `provider`, `input_tokens`, `cached_input_tokens`, `amount`)"
	var ddl []string
	for _, statement := range state.statements {
		if strings.HasPrefix(statement, "CREATE ") || strings.HasPrefix(statement, "DROP ") || strings.HasPrefix(statement, "ALTER ") {
			ddl = append(ddl, statement)
		}
	}
	if len(ddl) != 1 || ddl[0] != want {
		t.Fatalf("expected only the new portable MySQL covering index DDL, got %v", ddl)
	}
	state.statements = nil
	if err := ensureBillingRecordIndexes(db); err != nil {
		t.Fatalf("repeat MySQL migration: %v", err)
	}
	for _, statement := range state.statements {
		if strings.HasPrefix(statement, "CREATE ") || strings.HasPrefix(statement, "DROP ") || strings.HasPrefix(statement, "ALTER ") {
			t.Fatalf("repeat MySQL migration must reuse matching index metadata: %s", statement)
		}
	}
}
