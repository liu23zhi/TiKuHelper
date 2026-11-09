package registry

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
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

// blacklistTestHashHex 是测试侧的独立实现（不 import service，避免 registry→service 的反向依赖）。
// 它与 service.BlacklistPatternHashHex 用的是同一个 sha256，因此算出来的 value_hash 一致。
func blacklistTestHashHex(pattern string) string {
	sum := sha256.Sum256([]byte(pattern))
	return hex.EncodeToString(sum[:])
}

func newBlacklistMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:blacklist-migration-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

// 空库首次启动：两张表 + 索引都要建出来，标记写进去，第二次幂等。
func TestEnsureBlacklistSchemaCreatesTablesAndIndexes(t *testing.T) {
	db := newBlacklistMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}

	if err := ensureBlacklistSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, table := range []string{entity.TableNameBlacklistEntry, entity.TableNameBlacklistHit} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table %s", table)
		}
	}
	// 唯一索引 (kind, value_hash, match_type) 必须存在。
	if !db.Migrator().HasIndex(entity.TableNameBlacklistEntry, "uk_blacklist_entry_pattern") {
		t.Fatal("expected unique index uk_blacklist_entry_pattern")
	}
	if !db.Migrator().HasIndex(entity.TableNameBlacklistEntry, "idx_blacklist_entry_kind_enabled") {
		t.Fatal("expected index idx_blacklist_entry_kind_enabled")
	}
	if !db.Migrator().HasIndex(entity.TableNameBlacklistHit, "idx_blacklist_hit_entry") {
		t.Fatal("expected index idx_blacklist_hit_entry")
	}
	if !db.Migrator().HasIndex(entity.TableNameBlacklistHit, "idx_blacklist_hit_created") {
		t.Fatal("expected index idx_blacklist_hit_created")
	}
	// 命中日志必须有 hit_count 聚合列（§2.2）。
	if !db.Migrator().HasColumn(entity.TableNameBlacklistHit, "hit_count") {
		t.Fatal("expected blacklist_hits.hit_count")
	}
	// 标记写入后第二次直接返回（幂等，不重复建表）。
	if !systemSettingFlagIsSet(db, blacklistSchemaReadyKey) {
		t.Fatal("expected the migration flag to be written")
	}
	if err := ensureBlacklistSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// 唯一索引必须真的挡住重复规则：同一 (kind, value_hash, match_type) 只能有一条。
func TestBlacklistSchemaUniqueIndexRejectsDuplicatePattern(t *testing.T) {
	db := newBlacklistMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := ensureBlacklistSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	first := entity.BlacklistEntry{
		Kind: "ip", Pattern: "203.0.113.7",
		ValueHash: blacklistTestHashHex("203.0.113.7"), MatchType: "exact",
		Reason: "重复拉黑测试", Enabled: true,
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first: %v", err)
	}

	duplicate := first
	duplicate.ID = 0
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate (kind, value_hash, match_type) must be rejected")
	}

	// 换 match_type 是新行：同一个网段的 exact 与 cidr 是两条不同规则。
	cidrRow := first
	cidrRow.ID = 0
	cidrRow.Pattern = "203.0.113.0/24"
	cidrRow.ValueHash = blacklistTestHashHex("203.0.113.0/24")
	cidrRow.MatchType = "cidr"
	if err := db.Create(&cidrRow).Error; err != nil {
		t.Fatalf("different match_type must be allowed: %v", err)
	}

	// 换 kind 也是新行。
	phoneRow := first
	phoneRow.ID = 0
	phoneRow.Kind = "phone"
	if err := db.Create(&phoneRow).Error; err != nil {
		t.Fatalf("different kind must be allowed: %v", err)
	}
}

// 老库已经跑过标记（表在、索引缺）：索引补建必须发生在标记判断之前。
func TestEnsureBlacklistSchemaBackfillsIndexesOnAlreadyMarkedDB(t *testing.T) {
	db := newBlacklistMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.BlacklistEntry{}, &entity.BlacklistHit{}); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	// 模拟「AutoMigrate 建了表但没建自定义索引」的老库。
	for _, index := range []string{
		"uk_blacklist_entry_pattern", "idx_blacklist_entry_kind_enabled",
	} {
		if db.Migrator().HasIndex(entity.TableNameBlacklistEntry, index) {
			if err := db.Migrator().DropIndex(entity.TableNameBlacklistEntry, index); err != nil {
				t.Fatalf("drop %s: %v", index, err)
			}
		}
	}
	for _, index := range []string{"idx_blacklist_hit_entry", "idx_blacklist_hit_created"} {
		if db.Migrator().HasIndex(entity.TableNameBlacklistHit, index) {
			if err := db.Migrator().DropIndex(entity.TableNameBlacklistHit, index); err != nil {
				t.Fatalf("drop %s: %v", index, err)
			}
		}
	}
	// 标记已经写过 —— 若索引补建放在标记判断之后，这里就永远补不上。
	if err := setSystemSettingFlag(db, blacklistSchemaReadyKey); err != nil {
		t.Fatalf("set flag: %v", err)
	}

	if err := ensureBlacklistSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, index := range []string{"uk_blacklist_entry_pattern", "idx_blacklist_entry_kind_enabled"} {
		if !db.Migrator().HasIndex(entity.TableNameBlacklistEntry, index) {
			t.Fatalf("expected %s to be backfilled on an already-marked DB", index)
		}
	}
	for _, index := range []string{"idx_blacklist_hit_entry", "idx_blacklist_hit_created"} {
		if !db.Migrator().HasIndex(entity.TableNameBlacklistHit, index) {
			t.Fatalf("expected %s to be backfilled on an already-marked DB", index)
		}
	}
}

// 迁移绝不 DROP TABLE：跑两次之后表与数据都还在。
func TestEnsureBlacklistSchemaNeverDropsTablesOrData(t *testing.T) {
	db := newBlacklistMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := ensureBlacklistSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	entry := entity.BlacklistEntry{
		Kind: "ip", Pattern: "203.0.113.7",
		ValueHash: blacklistTestHashHex("203.0.113.7"), MatchType: "exact", Reason: "保留测试", Enabled: true,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if err := db.Create(&entity.BlacklistHit{EntryID: entry.ID, Kind: "ip", ClientIP: "203.0.113.7", Action: "blocked", HitCount: 3}).Error; err != nil {
		t.Fatalf("create hit: %v", err)
	}

	// 强行清掉标记，逼迁移完整重跑一遍（相当于「标记丢失」）。
	if err := db.Where(map[string]any{"key": blacklistSchemaReadyKey}).Delete(&entity.SystemSetting{}).Error; err != nil {
		t.Fatalf("clear flag: %v", err)
	}
	if err := ensureBlacklistSchema(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}

	var entryCount, hitCount int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&entryCount).Error; err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if err := db.Model(&entity.BlacklistHit{}).Count(&hitCount).Error; err != nil {
		t.Fatalf("count hits: %v", err)
	}
	if entryCount != 1 || hitCount != 1 {
		t.Fatalf("re-running the migration must not drop data: entries=%d hits=%d", entryCount, hitCount)
	}
}

// 两套方言的 DDL 必须分别正确：SQLite 带 IF NOT EXISTS，MySQL 不带（它不支持）。
func TestBlacklistIndexDDLBothDialects(t *testing.T) {
	spec := blacklistIndexSpec{
		name: "uk_blacklist_entry_pattern", table: entity.TableNameBlacklistEntry,
		columns: []string{"kind", "value_hash", "match_type"}, unique: true,
	}
	sqliteDDL := blacklistIndexDDL("sqlite", spec)
	wantSQLite := "CREATE UNIQUE INDEX IF NOT EXISTS `uk_blacklist_entry_pattern` ON `blacklist_entries` (`kind`, `value_hash`, `match_type`)"
	if sqliteDDL != wantSQLite {
		t.Fatalf("sqlite DDL = %q, want %q", sqliteDDL, wantSQLite)
	}
	mysqlDDL := blacklistIndexDDL("mysql", spec)
	wantMySQL := "CREATE UNIQUE INDEX `uk_blacklist_entry_pattern` ON `blacklist_entries` (`kind`, `value_hash`, `match_type`)"
	if mysqlDDL != wantMySQL {
		t.Fatalf("mysql DDL = %q, want %q", mysqlDDL, wantMySQL)
	}
	if strings.Contains(mysqlDDL, "IF NOT EXISTS") {
		t.Fatal("MySQL DDL must not use IF NOT EXISTS (MySQL rejects it)")
	}
	// 未知方言返回空串，由调用方报错，而不是瞎猜一句 SQL 出去。
	if got := blacklistIndexDDL("postgres", spec); got != "" {
		t.Fatalf("unknown dialect DDL = %q, want empty", got)
	}
	// 非唯一索引不带 UNIQUE。
	plain := blacklistIndexSpec{name: "idx_blacklist_hit_created", table: entity.TableNameBlacklistHit, columns: []string{"created_at"}}
	if got := blacklistIndexDDL("mysql", plain); strings.Contains(got, "UNIQUE") {
		t.Fatalf("non-unique index DDL must not contain UNIQUE: %s", got)
	}
}

// ---------------------------------------------------------------------------
// MySQL 分支：用进程内假连接器跑真实 MySQL 方言与真实迁移 SQL
// （与 billing_record_indexes_mysql_test.go 同款手法，不连真库）
// ---------------------------------------------------------------------------

type blacklistMySQLConnector struct {
	indexes    map[string][]string
	statements []string
	tables     map[string]bool
}

func (c *blacklistMySQLConnector) Connect(context.Context) (driver.Conn, error) {
	return &blacklistMySQLConn{state: c}, nil
}

func (c *blacklistMySQLConnector) Driver() driver.Driver { return blacklistMySQLDriver{} }

type blacklistMySQLDriver struct{}

func (blacklistMySQLDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("this test driver must only be opened through its local connector")
}

type blacklistMySQLConn struct{ state *blacklistMySQLConnector }

func (*blacklistMySQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statements are not part of this migration")
}
func (*blacklistMySQLConn) Close() error              { return nil }
func (*blacklistMySQLConn) Begin() (driver.Tx, error) { return blacklistMySQLTx{}, nil }

type blacklistMySQLTx struct{}

func (blacklistMySQLTx) Commit() error   { return nil }
func (blacklistMySQLTx) Rollback() error { return nil }

func (c *blacklistMySQLConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.statements = append(c.state.statements, query)
	upper := strings.ToUpper(query)
	switch {
	case upper == "SELECT DATABASE()", strings.Contains(upper, "FROM INFORMATION_SCHEMA.SCHEMATA"):
		return &blacklistMySQLRows{columns: []string{"database"}, values: [][]driver.Value{{"blacklist_test"}}}, nil
	case strings.Contains(upper, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES"):
		count := int64(0)
		if len(args) >= 2 {
			if c.state.tables[fmt.Sprint(args[1].Value)] {
				count = 1
			}
		}
		return &blacklistMySQLRows{columns: []string{"count(*)"}, values: [][]driver.Value{{count}}}, nil
	case strings.Contains(upper, "SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS"):
		count := int64(0)
		if len(args) >= 3 {
			if _, exists := c.state.indexes[fmt.Sprint(args[2].Value)]; exists {
				count = 1
			}
		}
		return &blacklistMySQLRows{columns: []string{"count(*)"}, values: [][]driver.Value{{count}}}, nil
	case strings.Contains(upper, "SELECT SEQ_IN_INDEX, COLUMN_NAME FROM INFORMATION_SCHEMA.STATISTICS"):
		rows := &blacklistMySQLRows{columns: []string{"SEQ_IN_INDEX", "COLUMN_NAME"}}
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

func (c *blacklistMySQLConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
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
		// 探测查询，无副作用。
	case strings.HasPrefix(query, "DELETE FROM `system_settings` WHERE `key` = "),
		strings.HasPrefix(query, "INSERT INTO `system_settings` "):
		// GORM 必须给 MySQL 保留字 key 加反引号；这里不模拟存储，只验证 SQL 可执行。
	default:
		return nil, fmt.Errorf("unexpected MySQL migration statement: %s", query)
	}
	return blacklistMySQLResult{}, nil
}

type blacklistMySQLResult struct{}

func (blacklistMySQLResult) LastInsertId() (int64, error) { return 1, nil }
func (blacklistMySQLResult) RowsAffected() (int64, error) { return 1, nil }

type blacklistMySQLRows struct {
	columns []string
	values  [][]driver.Value
	current int
}

func (r *blacklistMySQLRows) Columns() []string { return r.columns }
func (*blacklistMySQLRows) Close() error        { return nil }
func (r *blacklistMySQLRows) Next(values []driver.Value) error {
	if r.current >= len(r.values) {
		return io.EOF
	}
	copy(values, r.values[r.current])
	r.current++
	return nil
}

func TestBlacklistMigrationMySQLBranch(t *testing.T) {
	state := &blacklistMySQLConnector{
		indexes: map[string][]string{},
		tables: map[string]bool{
			entity.TableNameBlacklistEntry: true,
			entity.TableNameBlacklistHit:   true,
			entity.TableNameSystemSetting:  true,
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

	// 只跑「补索引」这一步：AutoMigrate 会去查 information_schema 的列定义，
	// 那部分不是本测试要验证的（SQLite 用例已经覆盖建表）。
	if err := ensureBlacklistEntryIndexes(db); err != nil {
		t.Fatalf("MySQL entry indexes: %v", err)
	}
	if err := ensureBlacklistHitIndexes(db); err != nil {
		t.Fatalf("MySQL hit indexes: %v", err)
	}

	var ddl []string
	for _, statement := range state.statements {
		if strings.HasPrefix(statement, "CREATE ") || strings.HasPrefix(statement, "DROP ") || strings.HasPrefix(statement, "ALTER ") {
			ddl = append(ddl, statement)
		}
	}
	want := []string{
		"CREATE UNIQUE INDEX `uk_blacklist_entry_pattern` ON `blacklist_entries` (`kind`, `value_hash`, `match_type`)",
		"CREATE INDEX `idx_blacklist_entry_kind_enabled` ON `blacklist_entries` (`kind`, `enabled`)",
		"CREATE INDEX `idx_blacklist_hit_entry` ON `blacklist_hits` (`entry_id`, `created_at`)",
		"CREATE INDEX `idx_blacklist_hit_created` ON `blacklist_hits` (`created_at`)",
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
	if err := ensureBlacklistEntryIndexes(db); err != nil {
		t.Fatalf("repeat MySQL entry indexes: %v", err)
	}
	if err := ensureBlacklistHitIndexes(db); err != nil {
		t.Fatalf("repeat MySQL hit indexes: %v", err)
	}
	for _, statement := range state.statements {
		if strings.HasPrefix(statement, "CREATE ") || strings.HasPrefix(statement, "DROP ") || strings.HasPrefix(statement, "ALTER ") {
			t.Fatalf("repeat MySQL migration must reuse index metadata: %s", statement)
		}
	}
}
