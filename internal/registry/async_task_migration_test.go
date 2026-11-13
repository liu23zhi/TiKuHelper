package registry

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newAsyncTaskMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:async-task-migration-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

// 空库首次启动：表 + 索引都要建出来，标记写进去（= async_task_schema_v1），第二次幂等。
func TestEnsureAsyncTaskSchemaCreatesTableAndIndexes(t *testing.T) {
	db := newAsyncTaskMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}

	if err := EnsureAsyncTaskSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameAsyncTask) {
		t.Fatalf("expected table %s", entity.TableNameAsyncTask)
	}
	// 任务书 ⑥ 点名的三个索引：type / status / expires_at。
	for _, index := range []string{"idx_async_task_type", "idx_async_task_status", "idx_async_task_expires_at"} {
		if !db.Migrator().HasIndex(entity.TableNameAsyncTask, index) {
			t.Fatalf("expected index %s", index)
		}
	}
	// 对外按 taskId 查单条，必须唯一。
	if !db.Migrator().HasIndex(entity.TableNameAsyncTask, "uk_async_task_task_id") {
		t.Fatal("expected unique index uk_async_task_task_id")
	}
	// 标记键必须是 async_task_schema_v1（121 §4.10 / 任务书 ⑥）。
	if !systemSettingFlagIsSet(db, "async_task_schema_v1") {
		t.Fatal("expected the migration flag async_task_schema_v1 to be written")
	}
	// 幂等：第二次不报错、不重复建。
	if err := EnsureAsyncTaskSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// 建表失败时**绝不能写标记**（121 §2.1）。
//
// 这个用例的构造方式：先写标记、再删表，然后跑一次迁移 ——
// 由于标记已存在，迁移会直接 return nil 而**不会重建表**。
// 这正是「标记说完成、表却不存在」的真实形态，用它证明
// 「有标记就不再建表」这条纪律确实成立（不是靠读代码猜）。
func TestEnsureAsyncTaskSchemaSkipsWhenFlagAlreadyPresent(t *testing.T) {
	db := newAsyncTaskMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := setSystemSettingFlag(db, "async_task_schema_v1"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	// 表不存在 + 标记存在（模拟「标记写了但建表没成功」的历史事故）。
	if db.Migrator().HasTable(entity.TableNameAsyncTask) {
		t.Fatal("precondition: table must not exist yet")
	}
	if err := EnsureAsyncTaskSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 行为断言：标记存在时迁移不建表 —— 这是 FLAG 型的既定语义
	// （121 §2.1 的反面：所以「建表失败也写标记」才是不可接受的）。
	if db.Migrator().HasTable(entity.TableNameAsyncTask) {
		t.Fatal("marked migration must not rebuild the table; the flag is authoritative")
	}
	// 清掉标记后必须能补建（证明「标记丢了会重跑」这条恢复路径是真的）。
	if err := db.Where(map[string]any{"key": "async_task_schema_v1"}).Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear flag: %v", err)
	}
	if err := EnsureAsyncTaskSchema(db); err != nil {
		t.Fatalf("re-run after clearing flag: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameAsyncTask) {
		t.Fatal("clearing the flag must let the migration rebuild the table")
	}
}

// 老库已跑过标记、但索引缺失：索引补建必须发生在标记判断**之前**（121 §2.2）。
func TestEnsureAsyncTaskSchemaBackfillsIndexesOnAlreadyMarkedDB(t *testing.T) {
	db := newAsyncTaskMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.AsyncTask{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	indexes := []string{
		"uk_async_task_task_id", "idx_async_task_type",
		"idx_async_task_status", "idx_async_task_expires_at",
	}
	for _, index := range indexes {
		if db.Migrator().HasIndex(entity.TableNameAsyncTask, index) {
			if err := db.Migrator().DropIndex(entity.TableNameAsyncTask, index); err != nil {
				t.Fatalf("drop %s: %v", index, err)
			}
		}
	}
	// 标记已经写过 —— 若索引补建放在标记判断之后，这里就永远补不上。
	if err := setSystemSettingFlag(db, "async_task_schema_v1"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if err := EnsureAsyncTaskSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, index := range indexes {
		if !db.Migrator().HasIndex(entity.TableNameAsyncTask, index) {
			t.Fatalf("expected %s to be backfilled on an already-marked DB", index)
		}
	}
}

// 迁移绝不 DROP TABLE：跑两次之后数据还在（121 §1.2 规范 8）。
func TestEnsureAsyncTaskSchemaNeverDropsData(t *testing.T) {
	db := newAsyncTaskMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := EnsureAsyncTaskSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	task := entity.AsyncTask{
		TaskID: "exp-0123456789abcdef", Type: entity.AsyncTaskTypeExport,
		Resource: "users", Format: "csv", Status: entity.AsyncTaskStatusSucceeded, Masked: true,
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	// 强行清掉标记，逼迁移完整重跑一遍（相当于「标记丢失」）。
	if err := db.Where(map[string]any{"key": "async_task_schema_v1"}).Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear flag: %v", err)
	}
	if err := EnsureAsyncTaskSchema(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	var count int64
	if err := db.Model(&entity.AsyncTask{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("re-running the migration must not drop data: tasks=%d", count)
	}
}

// task_id 唯一索引必须真的挡住重复（否则两条同 ID 的任务会互相覆盖）。
func TestAsyncTaskUniqueTaskIDRejectsDuplicate(t *testing.T) {
	db := newAsyncTaskMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := EnsureAsyncTaskSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	first := entity.AsyncTask{
		TaskID: "exp-aaaaaaaaaaaaaaaa", Type: entity.AsyncTaskTypeExport,
		Format: "csv", Status: entity.AsyncTaskStatusPending,
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first: %v", err)
	}
	duplicate := first
	duplicate.ID = 0
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate task_id must be rejected")
	}
}

// 两套方言的 DDL 必须分别正确：SQLite 带 IF NOT EXISTS，MySQL 不带（它不支持 1064）。
func TestAsyncTaskIndexDDLBothDialects(t *testing.T) {
	spec := asyncTaskIndexSpec{
		name: "uk_async_task_task_id", table: entity.TableNameAsyncTask,
		columns: []string{"task_id"}, unique: true,
	}
	wantSQLite := "CREATE UNIQUE INDEX IF NOT EXISTS `uk_async_task_task_id` ON `async_tasks` (`task_id`)"
	if got := asyncTaskIndexDDL("sqlite", spec); got != wantSQLite {
		t.Fatalf("sqlite DDL = %q, want %q", got, wantSQLite)
	}
	wantMySQL := "CREATE UNIQUE INDEX `uk_async_task_task_id` ON `async_tasks` (`task_id`)"
	mysqlDDL := asyncTaskIndexDDL("mysql", spec)
	if mysqlDDL != wantMySQL {
		t.Fatalf("mysql DDL = %q, want %q", mysqlDDL, wantMySQL)
	}
	if strings.Contains(mysqlDDL, "IF NOT EXISTS") {
		t.Fatal("MySQL DDL must not use IF NOT EXISTS (MySQL rejects it)")
	}
	if got := asyncTaskIndexDDL("postgres", spec); got != "" {
		t.Fatalf("unknown dialect DDL = %q, want empty", got)
	}
	plain := asyncTaskIndexSpec{name: "idx_async_task_status", table: entity.TableNameAsyncTask, columns: []string{"status"}}
	if got := asyncTaskIndexDDL("mysql", plain); strings.Contains(got, "UNIQUE") {
		t.Fatalf("non-unique index DDL must not contain UNIQUE: %s", got)
	}
}

// ---------------------------------------------------------------------------
// MySQL 分支：进程内假连接器跑真实 MySQL 方言与真实迁移 SQL（不连真库）
// ---------------------------------------------------------------------------

type asyncTaskMySQLConnector struct {
	indexes    map[string][]string
	statements []string
	tables     map[string]bool
}

func (c *asyncTaskMySQLConnector) Connect(context.Context) (driver.Conn, error) {
	return &asyncTaskMySQLConn{state: c}, nil
}

func (c *asyncTaskMySQLConnector) Driver() driver.Driver { return asyncTaskMySQLDriver{} }

type asyncTaskMySQLDriver struct{}

func (asyncTaskMySQLDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("this test driver must only be opened through its local connector")
}

type asyncTaskMySQLConn struct{ state *asyncTaskMySQLConnector }

func (*asyncTaskMySQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statements are not part of this migration")
}
func (*asyncTaskMySQLConn) Close() error              { return nil }
func (*asyncTaskMySQLConn) Begin() (driver.Tx, error) { return asyncTaskMySQLTx{}, nil }

type asyncTaskMySQLTx struct{}

func (asyncTaskMySQLTx) Commit() error   { return nil }
func (asyncTaskMySQLTx) Rollback() error { return nil }

func (c *asyncTaskMySQLConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.statements = append(c.state.statements, query)
	upper := strings.ToUpper(query)
	switch {
	case upper == "SELECT DATABASE()", strings.Contains(upper, "FROM INFORMATION_SCHEMA.SCHEMATA"):
		return &asyncTaskMySQLRows{columns: []string{"database"}, values: [][]driver.Value{{"async_task_test"}}}, nil
	case strings.Contains(upper, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES"):
		count := int64(0)
		if len(args) >= 2 {
			if c.state.tables[fmt.Sprint(args[1].Value)] {
				count = 1
			}
		}
		return &asyncTaskMySQLRows{columns: []string{"count(*)"}, values: [][]driver.Value{{count}}}, nil
	case strings.Contains(upper, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS"):
		count := int64(0)
		if len(args) >= 3 {
			if _, exists := c.state.indexes[fmt.Sprint(args[2].Value)]; exists {
				count = 1
			}
		}
		return &asyncTaskMySQLRows{columns: []string{"count(*)"}, values: [][]driver.Value{{count}}}, nil
	case strings.Contains(upper, "SELECT SEQ_IN_INDEX, COLUMN_NAME FROM INFORMATION_SCHEMA.STATISTICS"):
		rows := &asyncTaskMySQLRows{columns: []string{"SEQ_IN_INDEX", "COLUMN_NAME"}}
		if len(args) >= 2 {
			for i, column := range c.state.indexes[fmt.Sprint(args[1].Value)] {
				rows.values = append(rows.values, []driver.Value{int64(i + 1), column})
			}
		}
		return rows, nil
	default:
		return nil, fmt.Errorf("unexpected MySQL migration query: %s", query)
	}
}

func (c *asyncTaskMySQLConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.state.statements = append(c.state.statements, query)
	switch {
	case strings.HasPrefix(query, "CREATE UNIQUE INDEX "), strings.HasPrefix(query, "CREATE INDEX "):
		trimmed := strings.TrimPrefix(strings.TrimPrefix(query, "CREATE UNIQUE INDEX "), "CREATE INDEX ")
		parts := strings.SplitN(trimmed, " ON ", 2)
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
	case strings.HasPrefix(query, "SELECT 1"):
	case strings.HasPrefix(query, "DELETE FROM `system_settings` WHERE `key` = "),
		strings.HasPrefix(query, "INSERT INTO `system_settings` "):
	default:
		return nil, fmt.Errorf("unexpected MySQL migration statement: %s", query)
	}
	return asyncTaskMySQLResult{}, nil
}

type asyncTaskMySQLResult struct{}

func (asyncTaskMySQLResult) LastInsertId() (int64, error) { return 1, nil }
func (asyncTaskMySQLResult) RowsAffected() (int64, error) { return 1, nil }

type asyncTaskMySQLRows struct {
	columns []string
	values  [][]driver.Value
	current int
}

func (r *asyncTaskMySQLRows) Columns() []string { return r.columns }
func (*asyncTaskMySQLRows) Close() error        { return nil }
func (r *asyncTaskMySQLRows) Next(values []driver.Value) error {
	if r.current >= len(r.values) {
		return io.EOF
	}
	copy(values, r.values[r.current])
	r.current++
	return nil
}

func TestAsyncTaskMigrationMySQLBranch(t *testing.T) {
	state := &asyncTaskMySQLConnector{
		indexes: map[string][]string{},
		tables: map[string]bool{
			entity.TableNameAsyncTask:     true,
			entity.TableNameSystemSetting: true,
		},
	}
	sqlDB := sql.OpenDB(state)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}

	// 只跑「补索引」这一步（AutoMigrate 会查 information_schema 的列定义，不是本用例的对象）。
	if err := ensureAsyncTaskIndexes(db); err != nil {
		t.Fatalf("MySQL indexes: %v", err)
	}
	var ddl []string
	for _, statement := range state.statements {
		if strings.HasPrefix(statement, "CREATE ") || strings.HasPrefix(statement, "DROP ") || strings.HasPrefix(statement, "ALTER ") {
			ddl = append(ddl, statement)
		}
	}
	want := []string{
		"CREATE UNIQUE INDEX `uk_async_task_task_id` ON `async_tasks` (`task_id`)",
		"CREATE INDEX `idx_async_task_type` ON `async_tasks` (`type`)",
		"CREATE INDEX `idx_async_task_status` ON `async_tasks` (`status`)",
		"CREATE INDEX `idx_async_task_expires_at` ON `async_tasks` (`expires_at`)",
	}
	if len(ddl) != len(want) {
		t.Fatalf("DDL count = %d, want %d: %v", len(ddl), len(want), ddl)
	}
	for i := range want {
		if ddl[i] != want[i] {
			t.Fatalf("DDL[%d] = %q, want %q", i, ddl[i], want[i])
		}
	}

	// 幂等：第二次必须复用元数据，不产生任何 DDL。
	state.statements = nil
	if err := ensureAsyncTaskIndexes(db); err != nil {
		t.Fatalf("repeat MySQL indexes: %v", err)
	}
	for _, statement := range state.statements {
		if strings.HasPrefix(statement, "CREATE ") || strings.HasPrefix(statement, "DROP ") || strings.HasPrefix(statement, "ALTER ") {
			t.Fatalf("repeat MySQL migration must reuse index metadata: %s", statement)
		}
	}
}
