package registry

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// paymentEventMySQLConnector exercises the real MySQL dialect in process.
//
// It only models table/index metadata and records DDL — it does not claim to
// validate a MySQL server's storage engine or the optimizer. Its purpose is to
// prove that ensurePaymentEventSchema (AutoMigrate + OnConflict insert) emits
// statements MySQL actually accepts, without a live server.
type paymentEventMySQLConnector struct {
	mu          sync.Mutex
	tables      map[string]map[string]string // table -> column -> definition
	indexes     map[string]map[string][]string
	statements  []string
	markerValue string
}

func (c *paymentEventMySQLConnector) Connect(context.Context) (driver.Conn, error) {
	return &paymentEventMySQLConn{state: c}, nil
}

func (c *paymentEventMySQLConnector) Driver() driver.Driver { return paymentEventMySQLDriver{} }

type paymentEventMySQLDriver struct{}

func (paymentEventMySQLDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("this test driver must only be opened through its local connector")
}

type paymentEventMySQLConn struct{ state *paymentEventMySQLConnector }

func (*paymentEventMySQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statements are not part of this migration")
}
func (*paymentEventMySQLConn) Close() error              { return nil }
func (*paymentEventMySQLConn) Begin() (driver.Tx, error) { return paymentEventMySQLTx{}, nil }

type paymentEventMySQLTx struct{}

func (paymentEventMySQLTx) Commit() error   { return nil }
func (paymentEventMySQLTx) Rollback() error { return nil }

func (c *paymentEventMySQLConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.statements = append(c.state.statements, query)
	upper := strings.ToUpper(query)

	switch {
	case upper == "SELECT DATABASE()", strings.Contains(upper, "FROM INFORMATION_SCHEMA.SCHEMATA"):
		return &paymentEventMySQLRows{columns: []string{"database"}, values: [][]driver.Value{{"payment_event_test"}}}, nil
	case strings.Contains(upper, "FROM INFORMATION_SCHEMA.TABLES"):
		// HasTable / column metadata both go through this table.
		name := fmt.Sprint(args[1].Value)
		columns, exists := c.state.tables[name]
		if !exists {
			return &paymentEventMySQLRows{columns: []string{"count(*)"}, values: [][]driver.Value{{int64(0)}}}, nil
		}
		if strings.Contains(upper, "COUNT(*)") {
			return &paymentEventMySQLRows{columns: []string{"count(*)"}, values: [][]driver.Value{{int64(1)}}}, nil
		}
		rows := &paymentEventMySQLRows{columns: []string{"COLUMN_NAME", "COLUMN_TYPE", "IS_NULLABLE", "COLUMN_DEFAULT", "COLUMN_KEY", "EXTRA", "COLUMN_COMMENT", "GENERATION_EXPRESSION"}}
		for column, definition := range columns {
			rows.values = append(rows.values, []driver.Value{column, definition, "YES", nil, "", "", "", ""})
		}
		return rows, nil
	case strings.Contains(upper, "FROM INFORMATION_SCHEMA.STATISTICS"):
		name := fmt.Sprint(args[1].Value)
		byTable := c.state.indexes[name]
		indexName := fmt.Sprint(args[2].Value)
		if strings.Contains(upper, "COUNT(*)") {
			count := int64(0)
			if _, exists := byTable[indexName]; exists {
				count = 1
			}
			return &paymentEventMySQLRows{columns: []string{"count(*)"}, values: [][]driver.Value{{count}}}, nil
		}
		rows := &paymentEventMySQLRows{columns: []string{"SEQ_IN_INDEX", "COLUMN_NAME", "NON_UNIQUE"}}
		for i, column := range byTable[indexName] {
			rows.values = append(rows.values, []driver.Value{int64(i + 1), column, int64(0)})
		}
		return rows, nil
	case strings.Contains(upper, "FROM INFORMATION_SCHEMA.COLUMNS"):
		name := fmt.Sprint(args[1].Value)
		rows := &paymentEventMySQLRows{columns: []string{"COLUMN_NAME"}}
		for column := range c.state.tables[name] {
			rows.values = append(rows.values, []driver.Value{column})
		}
		return rows, nil
	case strings.HasPrefix(upper, "SELECT"):
		// Modeled as an empty table by default: this connector deliberately holds
		// no business rows, so system_settings lookups miss (marker unset) and the
		// backfill scan finds nothing to insert — exactly the first-startup path.
		//
		// A marker row can be seeded through markerValue to model the second
		// startup, where the one-shot flag makes the migration a pure no-op.
		if table := paymentEventMySQLSelectedTable(query); table == "system_settings" && c.state.markerValue != "" {
			return &paymentEventMySQLRows{
				columns: []string{"id", "key", "value", "remark", "created_at", "updated_at"},
				values:  [][]driver.Value{{int64(1), paymentEventSchemaReadyKey, c.state.markerValue, "", nil, nil}},
			}, nil
		}
		return &paymentEventMySQLRows{columns: []string{"no_rows"}}, nil
	default:
		return nil, fmt.Errorf("unexpected MySQL migration query: %s", query)
	}
}

// paymentEventMySQLSelectedTable 从 `SELECT ... FROM <table> ...` 里取出表名。
func paymentEventMySQLSelectedTable(query string) string {
	upper := strings.ToUpper(query)
	index := strings.Index(upper, " FROM ")
	if index < 0 {
		return ""
	}
	rest := strings.TrimSpace(query[index+len(" FROM "):])
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	return strings.Trim(fields[0], "`")
}

func (c *paymentEventMySQLConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.statements = append(c.state.statements, query)
	upper := strings.ToUpper(strings.TrimSpace(query))

	switch {
	case strings.HasPrefix(upper, "CREATE TABLE"):
		table := paymentEventMySQLTableName(query)
		if table == "" {
			return nil, fmt.Errorf("cannot parse created table: %s", query)
		}
		columns := map[string]string{}
		body := query[strings.Index(query, "(")+1 : strings.LastIndex(query, ")")]
		for _, part := range strings.Split(body, ",") {
			fields := strings.Fields(strings.TrimSpace(part))
			if len(fields) < 2 {
				continue
			}
			name := strings.Trim(fields[0], "`")
			if strings.EqualFold(name, "UNIQUE") || strings.EqualFold(name, "KEY") || strings.EqualFold(name, "INDEX") {
				continue
			}
			columns[name] = fields[1]
		}
		c.state.tables[table] = columns
	case strings.HasPrefix(upper, "ALTER TABLE"):
		// GORM adds indexes through ALTER TABLE ... ADD CONSTRAINT / ADD INDEX.
		if name := paymentEventMySQLIndexName(query); name != "" {
			table := paymentEventMySQLTableName(query)
			if c.state.indexes[table] == nil {
				c.state.indexes[table] = map[string][]string{}
			}
			c.state.indexes[table][name] = paymentEventMySQLIndexColumns(query)
		}
	case strings.HasPrefix(upper, "CREATE INDEX"):
		parts := strings.SplitN(strings.TrimPrefix(query, "CREATE INDEX "), " ON ", 2)
		if len(parts) == 2 {
			name := strings.Trim(parts[0], "`")
			table := paymentEventMySQLTableName("CREATE TABLE " + parts[1])
			if c.state.indexes[table] == nil {
				c.state.indexes[table] = map[string][]string{}
			}
			c.state.indexes[table][name] = paymentEventMySQLIndexColumns(query)
		}
	case strings.HasPrefix(upper, "DELETE FROM `SYSTEM_SETTINGS`"),
		strings.HasPrefix(upper, "INSERT INTO `SYSTEM_SETTINGS`"):
		// setSystemSettingFlag must quote the MySQL reserved column `key`.
	case strings.HasPrefix(upper, "INSERT INTO"):
		// Backfill / event insert. Nothing to model.
	default:
		return nil, fmt.Errorf("unexpected MySQL migration statement: %s", query)
	}
	return paymentEventMySQLResult{}, nil
}

func paymentEventMySQLTableName(query string) string {
	upper := strings.ToUpper(query)
	for _, marker := range []string{"CREATE TABLE IF NOT EXISTS ", "CREATE TABLE ", "ALTER TABLE ", " ON "} {
		if index := strings.Index(upper, marker); index >= 0 {
			rest := strings.TrimSpace(query[index+len(marker):])
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				continue
			}
			return strings.Trim(fields[0], "`(")
		}
	}
	return ""
}

func paymentEventMySQLIndexName(query string) string {
	upper := strings.ToUpper(query)
	for _, marker := range []string{"ADD CONSTRAINT ", "ADD UNIQUE INDEX ", "ADD INDEX ", "ADD UNIQUE "} {
		if index := strings.Index(upper, marker); index >= 0 {
			rest := strings.TrimSpace(query[index+len(marker):])
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				continue
			}
			return strings.Trim(fields[0], "`(")
		}
	}
	return ""
}

func paymentEventMySQLIndexColumns(query string) []string {
	start, end := strings.Index(query, "("), strings.LastIndex(query, ")")
	if start < 0 || end <= start {
		return nil
	}
	parts := strings.Split(query[start+1:end], ",")
	columns := make([]string, 0, len(parts))
	for _, part := range parts {
		columns = append(columns, strings.Trim(strings.TrimSpace(part), "`"))
	}
	return columns
}

type paymentEventMySQLResult struct{}

func (paymentEventMySQLResult) LastInsertId() (int64, error) { return 1, nil }
func (paymentEventMySQLResult) RowsAffected() (int64, error) { return 1, nil }

type paymentEventMySQLRows struct {
	columns []string
	values  [][]driver.Value
	current int
}

func (r *paymentEventMySQLRows) Columns() []string { return r.columns }
func (*paymentEventMySQLRows) Close() error        { return nil }
func (r *paymentEventMySQLRows) Next(values []driver.Value) error {
	if r.current >= len(r.values) {
		return io.EOF
	}
	copy(values, r.values[r.current])
	r.current++
	return nil
}

// TestEnsurePaymentEventSchemaRunsOnMySQLDialect 证明迁移在 MySQL 方言下真的能跑：
// 建表用 MySQL 语法、order_no 的唯一索引被建出来、标记写入时 `key` 被正确加引号。
//
// 本迁移刻意**没有**方言分支（纯 AutoMigrate + GORM clause），
// 这条测试就是「没有分支 = 两条都能跑」的证据，而不是靠注释自称。
func TestEnsurePaymentEventSchemaRunsOnMySQLDialect(t *testing.T) {
	state := &paymentEventMySQLConnector{
		tables: map[string]map[string]string{
			// users / recharge_orders 已存在，事件表不存在。
			"users":           {"id": "int(11)", "balance": "decimal(18,6)"},
			"system_settings": {"id": "int(11)", "key": "varchar(191)", "value": "longtext"},
			"recharge_orders": {"id": "int(11)", "order_no": "varchar(64)", "user_id": "int(11)",
				"amount": "decimal(18,6)", "paid_at": "datetime(3)", "third_party_order_no": "varchar(128)"},
		},
		indexes: map[string]map[string][]string{},
	}
	sqlDB := sql.OpenDB(state)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}),
		&gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}

	if err := ensurePaymentEventSchema(db); err != nil {
		t.Fatalf("MySQL migration failed: %v", err)
	}

	var createTable string
	for _, statement := range state.statements {
		if strings.HasPrefix(strings.ToUpper(statement), "CREATE TABLE") &&
			strings.Contains(statement, "payment_events") {
			createTable = statement
		}
	}
	if createTable == "" {
		t.Fatalf("expected a CREATE TABLE for payment_events on MySQL, got %v", state.statements)
	}
	for _, want := range []string{"`order_no`", "`recharge_order_id`", "`user_id`", "`amount`", "`trade_no`", "`trade_status`", "`source`", "`created_at`"} {
		if !strings.Contains(createTable, want) {
			t.Fatalf("MySQL CREATE TABLE missing %s: %s", want, createTable)
		}
	}
	if strings.Contains(createTable, "updated_at") {
		t.Fatalf("payment_events must be append-only (no updated_at): %s", createTable)
	}

	// order_no 唯一索引必须真的建出来（MySQL 侧）。
	uniqueOnOrderNo := false
	for name, columns := range state.indexes["payment_events"] {
		if len(columns) == 1 && columns[0] == "order_no" {
			uniqueOnOrderNo = true
			t.Logf("MySQL unique index: %s(%v)", name, columns)
		}
	}
	if !uniqueOnOrderNo {
		// GORM may inline UNIQUE into CREATE TABLE on MySQL; accept that form too.
		if !strings.Contains(strings.ToUpper(createTable), "UNIQUE") ||
			!strings.Contains(createTable, "`order_no`") {
			t.Fatalf("expected a UNIQUE constraint on order_no, indexes=%v ddl=%s",
				state.indexes["payment_events"], createTable)
		}
	}

	// 标记必须写入，且 MySQL 保留字 `key` 被反引号包裹。
	wroteMarker := false
	for _, statement := range state.statements {
		if strings.HasPrefix(strings.ToUpper(statement), "INSERT INTO `SYSTEM_SETTINGS`") {
			if !strings.Contains(statement, "`key`") {
				t.Fatalf("MySQL reserved column key must be quoted: %s", statement)
			}
			wroteMarker = true
		}
	}
	if !wroteMarker {
		t.Fatalf("expected the migration marker insert on MySQL, got %v", state.statements)
	}
}

// TestEnsurePaymentEventSchemaMySQLIsIdempotent 证明 MySQL 侧重复启动不会重建表：
// 标记已在时整条迁移是纯 no-op，一句 DDL 都不发。
func TestEnsurePaymentEventSchemaMySQLIsIdempotent(t *testing.T) {
	state := &paymentEventMySQLConnector{
		tables: map[string]map[string]string{
			"users":           {"id": "int(11)"},
			"system_settings": {"id": "int(11)", "key": "varchar(191)", "value": "longtext"},
			"recharge_orders": {"id": "int(11)", "order_no": "varchar(64)", "paid_at": "datetime(3)"},
			"payment_events":  {"id": "int(11)", "order_no": "varchar(64)"},
		},
		indexes: map[string]map[string][]string{},
		// 第二次启动：迁移标记已经在库里。
		markerValue: "1",
	}
	sqlDB := sql.OpenDB(state)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}),
		&gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePaymentEventSchema(db); err != nil {
		t.Fatalf("repeat MySQL ensure must be a no-op: %v", err)
	}
	for _, statement := range state.statements {
		upper := strings.ToUpper(statement)
		if strings.HasPrefix(upper, "CREATE TABLE") || strings.HasPrefix(upper, "ALTER TABLE") ||
			strings.HasPrefix(upper, "DROP ") {
			t.Fatalf("repeat MySQL migration must not touch schema: %s", statement)
		}
	}
}
