package registry

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newWhitelistMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:whitelist-mig-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	return db
}

// EnsureWhitelistSchema 必须建出表 + 两条索引，并写标记。
func TestEnsureWhitelistSchemaCreatesTableAndIndexes(t *testing.T) {
	db := newWhitelistMigrationTestDB(t)
	if err := EnsureWhitelistSchema(db); err != nil {
		t.Fatalf("EnsureWhitelistSchema: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameWhitelistEntry) {
		t.Fatalf("缺少表 %s", entity.TableNameWhitelistEntry)
	}
	for _, index := range []string{"uk_whitelist_entry_pattern", "idx_whitelist_entry_kind_enabled"} {
		if !db.Migrator().HasIndex(entity.TableNameWhitelistEntry, index) {
			t.Fatalf("缺少索引 %s", index)
		}
	}
	// 标记键必须是 whitelist_schema_v1（与黑名单的 blacklist_schema_ready 区分开）。
	if !systemSettingFlagIsSet(db, "whitelist_schema_v1") {
		t.Fatalf("未写入标记 whitelist_schema_v1")
	}
	if systemSettingFlagIsSet(db, "blacklist_schema_ready") {
		t.Fatal("白名单迁移不得写黑名单的标记键")
	}
}

// 唯一索引 (kind, value_hash, match_type) 必须真的挡住重复模式。
func TestWhitelistSchemaUniqueIndexRejectsDuplicatePattern(t *testing.T) {
	db := newWhitelistMigrationTestDB(t)
	if err := EnsureWhitelistSchema(db); err != nil {
		t.Fatalf("EnsureWhitelistSchema: %v", err)
	}
	first := entity.WhitelistEntry{
		Kind: "ip", Pattern: "203.0.113.7", MatchType: "exact",
		ValueHash: "abc", ValueMasked: "203.0.113.*", Reason: "第一条", Enabled: true,
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first: %v", err)
	}
	duplicate := first
	duplicate.ID = 0
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("同一 (kind,value_hash,match_type) 不应重复入库")
	}
	// 换 match_type 是另一条规则（索引包含该列）。
	other := first
	other.ID = 0
	other.MatchType = "cidr"
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("不同 match_type 应可入库：%v", err)
	}
}

// 已跑过标记的老库：新索引必须能补建（不受一次性标记约束）。
func TestEnsureWhitelistSchemaBackfillsIndexesOnAlreadyMarkedDB(t *testing.T) {
	db := newWhitelistMigrationTestDB(t)
	if err := EnsureWhitelistSchema(db); err != nil {
		t.Fatalf("EnsureWhitelistSchema: %v", err)
	}
	// 删掉一条索引，模拟「老库跑过标记、但后来新增的索引等不到 AutoMigrate」。
	if err := db.Migrator().DropIndex(entity.TableNameWhitelistEntry, "idx_whitelist_entry_kind_enabled"); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if db.Migrator().HasIndex(entity.TableNameWhitelistEntry, "idx_whitelist_entry_kind_enabled") {
		t.Fatal("前置条件：索引应已删除")
	}
	// 标记仍在 → 迁移会走「只补索引」分支。
	if err := EnsureWhitelistSchema(db); err != nil {
		t.Fatalf("EnsureWhitelistSchema (backfill): %v", err)
	}
	if !db.Migrator().HasIndex(entity.TableNameWhitelistEntry, "idx_whitelist_entry_kind_enabled") {
		t.Fatal("标记已存在时仍必须补建缺失索引")
	}
}

// 纪律：**不做任何 DROP TABLE / 不删数据**。两次运行幂等、不报错、行数不变。
func TestEnsureWhitelistSchemaIsIdempotentAndNeverDropsData(t *testing.T) {
	db := newWhitelistMigrationTestDB(t)
	if err := EnsureWhitelistSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	entry := entity.WhitelistEntry{
		Kind: "ip", Pattern: "203.0.113.7", MatchType: "exact",
		ValueHash: "def", ValueMasked: "203.0.113.*", Reason: "保留我", Enabled: true,
	}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatalf("create entry: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := EnsureWhitelistSchema(db); err != nil {
			t.Fatalf("run #%d 不应报错（幂等）：%v", i+2, err)
		}
	}
	var count int64
	if err := db.Model(&entity.WhitelistEntry{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复迁移不得删数据，got %d 行", count)
	}
}

// nil DB 必须安全返回（启动早期可能还没有 DB）。
func TestEnsureWhitelistSchemaNilDBIsNoop(t *testing.T) {
	if err := EnsureWhitelistSchema(nil); err != nil {
		t.Fatalf("nil DB 不应报错：%v", err)
	}
}

// 索引 DDL 两套方言：SQLite 带 IF NOT EXISTS，MySQL 不带（它不支持该子句）。
//
// 复用 blacklistIndexDDL 的实现，因此这里断言的是「白名单索引名 + 同一套方言规则」。
func TestWhitelistIndexDDLBothDialects(t *testing.T) {
	spec := blacklistIndexSpec{
		name:    "uk_whitelist_entry_pattern",
		table:   entity.TableNameWhitelistEntry,
		columns: []string{"kind", "value_hash", "match_type"},
		unique:  true,
	}
	sqliteDDL := blacklistIndexDDL("sqlite", spec)
	if !strings.Contains(sqliteDDL, "IF NOT EXISTS") {
		t.Fatalf("SQLite DDL 必须带 IF NOT EXISTS：%s", sqliteDDL)
	}
	if !strings.Contains(sqliteDDL, "UNIQUE INDEX") {
		t.Fatalf("唯一索引 DDL 必须含 UNIQUE INDEX：%s", sqliteDDL)
	}
	if !strings.Contains(sqliteDDL, entity.TableNameWhitelistEntry) {
		t.Fatalf("DDL 必须指向白名单表：%s", sqliteDDL)
	}

	mysqlDDL := blacklistIndexDDL("mysql", spec)
	if strings.Contains(mysqlDDL, "IF NOT EXISTS") {
		t.Fatalf("MySQL DDL 不得带 IF NOT EXISTS（不支持，会报 1064）：%s", mysqlDDL)
	}
	if !strings.Contains(mysqlDDL, "UNIQUE INDEX") {
		t.Fatalf("MySQL 唯一索引 DDL 必须含 UNIQUE INDEX：%s", mysqlDDL)
	}

	if got := blacklistIndexDDL("postgres", spec); got != "" {
		t.Fatalf("未知方言应返回空串，got %q", got)
	}
}
