package registry

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// newClientKindTestDB 建一个内存 SQLite 库。
//
// 用独立 DSN 名（含 t.Name()）避免子测试之间共享 cache=shared 的同一实例。
func newClientKindTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:client-kind-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

// clientKindColumnInfo 是 PRAGMA table_info 的一行。
//
// ⚠️ 每个字段都必须写显式 column tag：gorm 默认把 `NotNull` 映射成
// `not_null`，而 PRAGMA 出的列名是 `notnull`。漏了 tag 时 Scan 不报错、
// 只是永远落零值 —— 断言「notnull=1」会变成一个永远失败的假红，
// 更容易误判成「迁移没写 NOT NULL」。
type clientKindColumnInfo struct {
	Name       string  `gorm:"column:name"`
	Type       string  `gorm:"column:type"`
	NotNull    int     `gorm:"column:notnull"`
	DefaultVal *string `gorm:"column:dflt_value"`
}

// readClientKindColumn 读某表 client_kind 列的物理定义；不存在返回 nil。
//
// 用 PRAGMA 而不是 HasColumn：本测试要断言的是**列约束**（NOT NULL / DEFAULT），
// HasColumn 只能回答「在不在」，无法证明「有默认值」这条硬要求。
func readClientKindColumn(t *testing.T, db *gorm.DB, table string) *clientKindColumnInfo {
	t.Helper()
	var columns []clientKindColumnInfo
	if err := db.Raw("PRAGMA table_info(" + table + ")").Scan(&columns).Error; err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	for i := range columns {
		if columns[i].Name == ClientKindColumn {
			return &columns[i]
		}
	}
	return nil
}

// clientKindDefault 把 PRAGMA 的 dflt_value 归一成不带引号的字面量。
//
// SQLite 原样存 DDL 里的默认值表达式，字符串默认值带单引号（`'unknown'`）。
// 不剥引号直接比较会把「默认值写对了」判成失败。
func clientKindDefault(column *clientKindColumnInfo) string {
	if column.DefaultVal == nil {
		return ""
	}
	return strings.Trim(*column.DefaultVal, "'\"")
}

// 空库首启：两张表由 AutoMigrate 建出后，迁移必须把两列都补上，
// 且列定义必须是 varchar(32) / NOT NULL / DEFAULT 'unknown'。
func TestEnsureClientKindSchemaAddsBothColumnsOnFreshDatabase(t *testing.T) {
	db := newClientKindTestDB(t)

	// 模拟首次启动：实体建表（生成实体里没有 client_kind 字段，这正是要验的前提）。
	if err := db.AutoMigrate(&entity.Log{}, &entity.AccessToken{}); err != nil {
		t.Fatalf("auto migrate baseline tables: %v", err)
	}
	for _, table := range []string{entity.TableNameLog, entity.TableNameAccessToken} {
		if readClientKindColumn(t, db, table) != nil {
			t.Fatalf("test setup invalid: %s already has %s", table, ClientKindColumn)
		}
	}

	if err := EnsureClientKindSchema(db); err != nil {
		t.Fatalf("ensure client kind schema: %v", err)
	}

	for _, table := range []string{entity.TableNameLog, entity.TableNameAccessToken} {
		column := readClientKindColumn(t, db, table)
		if column == nil {
			t.Fatalf("expected %s.%s to be added", table, ClientKindColumn)
		}
		if !strings.EqualFold(column.Type, "varchar(32)") {
			t.Fatalf("%s.%s type = %q, want varchar(32)", table, ClientKindColumn, column.Type)
		}
		if column.NotNull != 1 {
			t.Fatalf("%s.%s must be NOT NULL (got notnull=%d)", table, ClientKindColumn, column.NotNull)
		}
		// ⚠️ 期望值必须写**字面量**，不能写 ClientKindUnknown：
		// 常量一旦被改成别的字面量，DDL 里的 DEFAULT 会跟着变，
		// 用常量比对等于「拿改动后的值跟自己比」→ 永远相等 → 全绿。
		// 这正是第 4 类假钉子（默认值安全项薄断言）的形态。
		if got := clientKindDefault(column); got != "unknown" {
			t.Fatalf("%s.%s default = %q, want \"unknown\"", table, ClientKindColumn, got)
		}
	}
}

// 幂等：二次执行不报错，且不会多出第二列（列数恒为 1）。
func TestEnsureClientKindSchemaIsIdempotent(t *testing.T) {
	db := newClientKindTestDB(t)
	if err := db.AutoMigrate(&entity.Log{}, &entity.AccessToken{}); err != nil {
		t.Fatalf("auto migrate baseline tables: %v", err)
	}

	if err := EnsureClientKindSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := EnsureClientKindSchema(db); err != nil {
		t.Fatalf("second run must be a no-op, got: %v", err)
	}
	if err := EnsureClientKindSchema(db); err != nil {
		t.Fatalf("third run must be a no-op, got: %v", err)
	}

	for _, table := range []string{entity.TableNameLog, entity.TableNameAccessToken} {
		var columns []clientKindColumnInfo
		if err := db.Raw("PRAGMA table_info(" + table + ")").Scan(&columns).Error; err != nil {
			t.Fatalf("pragma table_info(%s): %v", table, err)
		}
		count := 0
		for _, column := range columns {
			if column.Name == ClientKindColumn {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%s has %d %s columns, want exactly 1", table, count, ClientKindColumn)
		}
	}
}

// 老库升级：已有数据行必须在加列后自动落 'unknown'（靠 DEFAULT，不靠 UPDATE 回填）。
//
// 这条是「NOT NULL DEFAULT」的判别性用例：把默认值去掉后，存量行会变成 NULL，
// 本用例立刻变红。
func TestEnsureClientKindSchemaBackfillsExistingRowsWithUnknown(t *testing.T) {
	db := newClientKindTestDB(t)
	if err := db.AutoMigrate(&entity.Log{}, &entity.AccessToken{}); err != nil {
		t.Fatalf("auto migrate baseline tables: %v", err)
	}

	// 加列之前写入存量行 —— 用的 SQL 故意不包含 client_kind，
	// 正是「旧代码 INSERT 不带该列」的真实形态。
	if err := db.Exec(`INSERT INTO logs (qid, user_id, action, create_time) VALUES (11, 7, 1, '2026-01-02 03:04:05')`).Error; err != nil {
		t.Fatalf("insert legacy log: %v", err)
	}
	if err := db.Exec(`INSERT INTO access_tokens (user_id, name, token_hash, token_prefix, status) VALUES (7, 'legacy', 'hash-legacy', 'tk_legacy', 'enabled')`).Error; err != nil {
		t.Fatalf("insert legacy token: %v", err)
	}

	if err := EnsureClientKindSchema(db); err != nil {
		t.Fatalf("ensure client kind schema: %v", err)
	}

	var logKinds []string
	if err := db.Table(entity.TableNameLog).Select(ClientKindColumn).Scan(&logKinds).Error; err != nil {
		t.Fatalf("read logs.%s: %v", ClientKindColumn, err)
	}
	// 期望值一律写字面量 "unknown"（不用 ClientKindUnknown 常量）：
	// 常量被改成别的值时若仍拿常量比，存量行会跟着变、断言永远相等 → 假货。
	if len(logKinds) != 1 || logKinds[0] != "unknown" {
		t.Fatalf("logs.%s of the legacy row = %v, want [\"unknown\"]", ClientKindColumn, logKinds)
	}

	var tokenKinds []string
	if err := db.Table(entity.TableNameAccessToken).Select(ClientKindColumn).Scan(&tokenKinds).Error; err != nil {
		t.Fatalf("read access_tokens.%s: %v", ClientKindColumn, err)
	}
	if len(tokenKinds) != 1 || tokenKinds[0] != "unknown" {
		t.Fatalf("access_tokens.%s of the legacy row = %v, want [\"unknown\"]", ClientKindColumn, tokenKinds)
	}

	// 存量行之后，老代码继续按原样 INSERT（不带 client_kind）也必须成功并落默认值。
	if err := db.Exec(`INSERT INTO logs (qid, user_id, action, create_time) VALUES (12, 7, 1, '2026-01-02 03:04:06')`).Error; err != nil {
		t.Fatalf("legacy insert without %s must still succeed: %v", ClientKindColumn, err)
	}
	var newest string
	if err := db.Table(entity.TableNameLog).Where("qid = ?", 12).Select(ClientKindColumn).Scan(&newest).Error; err != nil {
		t.Fatalf("read new row: %v", err)
	}
	if newest != "unknown" {
		t.Fatalf("new row without explicit %s = %q, want \"unknown\"", ClientKindColumn, newest)
	}
}

// 表还不存在时不得报错、不得凭空建列（极端空库首启路径）。
//
// 这里同时锁定「下一次启动会补齐」：表建出来后再跑一次迁移必须加列成功。
func TestEnsureClientKindSchemaSkipsMissingTablesButRecoversLater(t *testing.T) {
	db := newClientKindTestDB(t)

	if err := EnsureClientKindSchema(db); err != nil {
		t.Fatalf("migration on a database without logs/access_tokens must not fail: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameLog) || db.Migrator().HasTable(entity.TableNameAccessToken) {
		t.Fatal("migration must not create tables")
	}
	if err := EnsureClientKindSchema(nil); err != nil {
		t.Fatalf("nil db must be a no-op: %v", err)
	}

	if err := db.AutoMigrate(&entity.Log{}, &entity.AccessToken{}); err != nil {
		t.Fatalf("auto migrate baseline tables: %v", err)
	}
	if err := EnsureClientKindSchema(db); err != nil {
		t.Fatalf("migration after tables appeared: %v", err)
	}
	for _, table := range []string{entity.TableNameLog, entity.TableNameAccessToken} {
		if readClientKindColumn(t, db, table) == nil {
			t.Fatalf("expected %s.%s to be added on the later run", table, ClientKindColumn)
		}
	}
}

// 只加一列，不动任何既有列与数据（结构迁移的最小侵入契约）。
func TestEnsureClientKindSchemaPreservesExistingColumns(t *testing.T) {
	db := newClientKindTestDB(t)
	if err := db.AutoMigrate(&entity.Log{}, &entity.AccessToken{}); err != nil {
		t.Fatalf("auto migrate baseline tables: %v", err)
	}
	before := map[string]int{}
	for _, table := range []string{entity.TableNameLog, entity.TableNameAccessToken} {
		var columns []clientKindColumnInfo
		if err := db.Raw("PRAGMA table_info(" + table + ")").Scan(&columns).Error; err != nil {
			t.Fatalf("pragma table_info(%s): %v", table, err)
		}
		before[table] = len(columns)
	}

	if err := EnsureClientKindSchema(db); err != nil {
		t.Fatalf("ensure client kind schema: %v", err)
	}

	for _, table := range []string{entity.TableNameLog, entity.TableNameAccessToken} {
		var columns []clientKindColumnInfo
		if err := db.Raw("PRAGMA table_info(" + table + ")").Scan(&columns).Error; err != nil {
			t.Fatalf("pragma table_info(%s): %v", table, err)
		}
		if len(columns) != before[table]+1 {
			t.Fatalf("%s column count = %d, want %d (exactly one added)", table, len(columns), before[table]+1)
		}
	}
}

// MySQL 方言分支必须把列建成 varchar(32) NOT NULL DEFAULT 'unknown'。
//
// 断言点选在 gorm 真正拼进 `ALTER TABLE ? ADD ? ?` 的那段列定义
// （Migrator().FullDataTypeOf），而不是去截 DryRun 的 SQL 串：
//   - DryRun 下 RawExec 直接 return（callbacks/raw.go: `if db.Error == nil && !db.DryRun`），
//     既没有 Trace 也没有回调，截不到语句只能证明「测试没跑起来」；
//   - 而 AddColumn 的 DDL 就是 `m.DB.Exec("ALTER TABLE ? ADD ? ?", table, column, FullDataTypeOf(f))`，
//     所以「列定义正确」等价于「发给 MySQL 的 ALTER 正确」。
//
// 这条用例的意义：SQLite 分支是手写裸 DDL，MySQL 分支靠影子结构的 tag ——
// 两者的约束必须一致，否则只在 MySQL 上丢掉默认值（正是 121 决策 5 警告的故障）。
func TestClientKindMySQLColumnDefinitionKeepsNotNullDefault(t *testing.T) {
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user:password@tcp(127.0.0.1:3306)/test",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run mysql: %v", err)
	}

	for _, target := range clientKindTargets() {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(target.proto); err != nil {
			t.Fatalf("%s: parse shadow proto: %v", target.table, err)
		}
		// 影子结构必须钉在自己的物理表上，否则 AddColumn 会落到 gorm 的默认复数表名。
		if stmt.Table != target.table {
			t.Fatalf("%s: shadow proto resolved to table %q, want %q", target.table, stmt.Table, target.table)
		}
		// 字段名必须能查到 —— 查不到时 AddColumn 直接返回 "failed to look up field"。
		field := stmt.Schema.LookUpField("ClientKind")
		if field == nil {
			t.Fatalf("%s: shadow proto has no ClientKind field", target.table)
		}
		if field.DBName != ClientKindColumn {
			t.Fatalf("%s: field DBName = %q, want %q", target.table, field.DBName, ClientKindColumn)
		}

		definition := strings.ToLower(db.Migrator().FullDataTypeOf(field).SQL)
		if !strings.Contains(definition, "varchar(32)") {
			t.Fatalf("%s: column type must be varchar(32), got %q", target.table, definition)
		}
		if !strings.Contains(definition, "not null") {
			t.Fatalf("%s: column must be NOT NULL, got %q", target.table, definition)
		}
		// 期望值写死字面量 "default 'unknown'"：这里同时钉两件事 ——
		//   ① 默认值的**字面值**必须是 unknown（常量漂移时必红）；
		//   ② 默认值必须**带单引号**。
		// 第 ② 条来自实测：gorm 对 `default:unknown` 走 reflect.String 分支
		// （DefaultValueInterface="unknown"），拼出 `DEFAULT 'unknown'`；
		// 而 `default:1` 走数字分支（DefaultValueInterface=int64(1)），拼 `DEFAULT 1`。
		// 两条分支不同，不能靠推断 —— 用常量拼期望串会同时失去这两条判别力。
		if !strings.Contains(definition, "default 'unknown'") {
			t.Fatalf("%s: column must default to 'unknown' (quoted literal), got %q", target.table, definition)
		}
	}
}

// 常量不得与 service 包的三值漂移。
//
// registry 不能 import service（service 已反向 import registry/dbqueue，会成环），
// 所以字面值各写一份；这条用例是防漂移的钉子。
func TestClientKindConstantsMatchServiceValues(t *testing.T) {
	if ClientKindColumn != "client_kind" {
		t.Fatalf("column name = %q, want client_kind", ClientKindColumn)
	}
	if ClientKindExamClientValue != "exam_client" {
		t.Fatalf("exam client value = %q, want exam_client", ClientKindExamClientValue)
	}
	if ClientKindUnknown != "unknown" {
		t.Fatalf("unknown value = %q, want unknown", ClientKindUnknown)
	}
}
