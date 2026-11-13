package registry

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 公告迁移测试（N4，task_doc/121 §2 的 8 条规范 + §6 的 5 条共同验收）。
//
// 这组用例的核心不是「表建出来了」（那是最低门槛），而是**标记语义**：
//   - 标记写入后二次启动必须**不重跑**（121 §6 第 3 条）；
//   - 标记写入后人为删表再启动 → **表不会重建**（121 §6 第 4 条，
//     这是「正确行为」：标记说已完成，我们不跟标记对着干）；
//   - 建表失败时**绝不写标记**（121 §2.1 的反直觉细节）；
//   - 索引补建**放在标记判断之前**（121 §2.2，老库否则永远补不到新索引）。
//
// 前两条只有把 systemSettingFlagIsSet 短路成 `if false` 才会变红 ——
// 那正是本任务的变异验证形态（见报告）。

// announcementTestDBSeq 保证每个用例拿到独立的内存库。
//
// `file:xxx?mode=memory&cache=shared` 在同一进程内同名库是**共享**的；
// 表驱动用例的 t.Name() 完全相同，不加序号会互相污染（本仓已踩过）。
var announcementTestDBSeq atomic.Int64

// newAnnouncementMigrationTestDB 建一个只含 system_settings 的空库。
//
// 刻意**不预建 announcements**：本组用例要验证「空库首启能建出来」。
func newAnnouncementMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := "file:announcement-migration-" + name + "-" + itoaAtomic(announcementTestDBSeq.Add(1)) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
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

// itoaAtomic 是一个不引入 strconv 的小工具（本文件只需要正整数转字符串）。
func itoaAtomic(value int64) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// 空库首启：表 + 两条索引都建出来，标记写进去。
func TestEnsureAnnouncementSchemaCreatesTableAndIndexes(t *testing.T) {
	db := newAnnouncementMigrationTestDB(t)

	if db.Migrator().HasTable(entity.TableNameAnnouncement) {
		t.Fatal("announcements 不该在迁移前存在（否则本用例测不到建表）")
	}
	if err := EnsureAnnouncementSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameAnnouncement) {
		t.Fatalf("expected table %s", entity.TableNameAnnouncement)
	}
	// 112 §4.3 定义的全部列都要在（少一列 = 功能静默缺字段）。
	for _, column := range []string{
		"id", "kind", "title", "content", "severity", "enabled",
		"starts_at", "ends_at", "created_by", "created_at", "updated_at",
	} {
		if !db.Migrator().HasColumn(entity.TableNameAnnouncement, column) {
			t.Fatalf("expected column announcements.%s", column)
		}
	}
	if !db.Migrator().HasIndex(entity.TableNameAnnouncement, "idx_announcement_kind_enabled") {
		t.Fatal("expected index idx_announcement_kind_enabled")
	}
	if !db.Migrator().HasIndex(entity.TableNameAnnouncement, "idx_announcement_window") {
		t.Fatal("expected index idx_announcement_window")
	}
	if !systemSettingFlagIsSet(db, announcementsSchemaV1Key) {
		t.Fatal("expected the migration flag to be written")
	}
}

// 标记生效：二次启动**不重跑**（121 §6 第 3 条）。
//
// 这是本任务变异验证的目标用例：把 systemSettingFlagIsSet 短路成 `if false` 后，
// EnsureAnnouncementSchema 会每次都走建表分支，于是下面的
// 「第二次不应再 AutoMigrate」断言变红。
func TestEnsureAnnouncementSchemaSecondRunSkipsWhenFlagSet(t *testing.T) {
	db := newAnnouncementMigrationTestDB(t)
	if err := EnsureAnnouncementSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !systemSettingFlagIsSet(db, announcementsSchemaV1Key) {
		t.Fatal("标记未写入，后续断言无意义")
	}

	// 删掉表但保留标记：如果标记生效，第二次运行时不该重建表。
	// 这是 121 §6 第 4 条明确称为**正确行为**的场景 —— 标记说「已完成」，
	// 迁移就该信它；表被外力删掉属于运维事故，不由启动迁移猜。
	if err := db.Migrator().DropTable(entity.TableNameAnnouncement); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if err := EnsureAnnouncementSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameAnnouncement) {
		t.Fatal("标记已写 + 表被外部删掉时，第二次运行**不该**重建表（121 §6 第 4 条）" +
			"—— 出现这张表说明 FLAG 判断被绕过（例如短路成 if false）")
	}
}

// 建表失败时**绝不写标记**（121 §2.1 的反直觉细节）。
//
// 造「建表失败」的手法：在同一个库上放一个**同名视图**。SQLite 的
// `CREATE TABLE announcements` 会因 `there is already an object named announcements`
// 失败，而 HasTable 对视图返回 false（它查的是 table 类型），
// 因此迁移会真的走到建表分支并失败 —— 不需要 mock 或假驱动。
//
// 若这条用例写错（例如用 DropTable 之后测），就会得到「标记在但表没有」的
// 静默 100% 失效状态：功能永远建不出来，且没有任何报错。
func TestEnsureAnnouncementSchemaDoesNotWriteFlagWhenCreateFails(t *testing.T) {
	db := newAnnouncementMigrationTestDB(t)

	if err := db.Exec("CREATE VIEW " + entity.TableNameAnnouncement + " AS SELECT 1 AS id").Error; err != nil {
		t.Fatalf("create blocking view: %v", err)
	}
	if err := EnsureAnnouncementSchema(db); err == nil {
		t.Fatal("建表被同名对象挡住时必须返回 error（结构性失败要让人看见）")
	}
	if systemSettingFlagIsSet(db, announcementsSchemaV1Key) {
		t.Fatal("建表失败时**绝不写标记**（121 §2.1）：否则这张表将永远建不出来且无任何报错")
	}

	// 清掉障碍后重跑必须能建成功 —— 证明上一步的失败没有留下任何持久副作用。
	if err := db.Exec("DROP VIEW " + entity.TableNameAnnouncement).Error; err != nil {
		t.Fatalf("drop blocking view: %v", err)
	}
	if err := EnsureAnnouncementSchema(db); err != nil {
		t.Fatalf("rerun after clearing blocker: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameAnnouncement) {
		t.Fatal("障碍清除后应当建表成功")
	}
	if !systemSettingFlagIsSet(db, announcementsSchemaV1Key) {
		t.Fatal("障碍清除后应当写入标记")
	}
}

// 索引补建在标记判断**之前**（121 §2.2）。
//
// 场景：老库已经跑过标记（表建好），但索引是后来新增的。
// 若索引只在 FLAG 之后建，这类库永远补不到索引 —— 表现只是「查询慢」，不报错。
//
// 构造：先完整迁移一次 → 手工删掉索引 → 标记保持 → 再跑一次。
func TestEnsureAnnouncementSchemaBackfillsIndexesAfterFlagSet(t *testing.T) {
	db := newAnnouncementMigrationTestDB(t)
	if err := EnsureAnnouncementSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !systemSettingFlagIsSet(db, announcementsSchemaV1Key) {
		t.Fatal("标记未写入，后续断言无意义")
	}
	for _, name := range []string{"idx_announcement_kind_enabled", "idx_announcement_window"} {
		if err := db.Migrator().DropIndex(entity.TableNameAnnouncement, name); err != nil {
			t.Fatalf("drop index %s: %v", name, err)
		}
		if db.Migrator().HasIndex(entity.TableNameAnnouncement, name) {
			t.Fatalf("索引 %s 未真的删掉，用例前置不成立", name)
		}
	}

	if err := EnsureAnnouncementSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	for _, name := range []string{"idx_announcement_kind_enabled", "idx_announcement_window"} {
		if !db.Migrator().HasIndex(entity.TableNameAnnouncement, name) {
			t.Fatalf("标记已写时仍必须补建索引 %s（121 §2.2：索引补建放在标记判断之前）", name)
		}
	}
}

// 两方言的索引 DDL 必须各自合法（MySQL 不支持 IF NOT EXISTS）。
//
// 与 billing_record_indexes_mysql_test.go 同一思路：纯函数断言，不连真 MySQL。
func TestAnnouncementIndexDDLPerDialect(t *testing.T) {
	spec := announcementIndexSpec{
		name:    "idx_announcement_window",
		table:   entity.TableNameAnnouncement,
		columns: []string{"enabled", "starts_at", "ends_at"},
	}
	sqliteDDL := announcementIndexDDL("SQLite", spec)
	if !strings.Contains(sqliteDDL, "IF NOT EXISTS") {
		t.Fatalf("SQLite DDL 必须带 IF NOT EXISTS（DDL 层幂等）：%s", sqliteDDL)
	}
	if !strings.Contains(sqliteDDL, "`enabled`, `starts_at`, `ends_at`") {
		t.Fatalf("SQLite DDL 列顺序不对：%s", sqliteDDL)
	}
	mysqlDDL := announcementIndexDDL("mysql", spec)
	if strings.Contains(mysqlDDL, "IF NOT EXISTS") {
		t.Fatalf("MySQL 的 CREATE INDEX 没有 IF NOT EXISTS（会报 1064）：%s", mysqlDDL)
	}
	if mysqlDDL == "" {
		t.Fatal("mysql DDL 不该为空")
	}
	if unknown := announcementIndexDDL("postgres", spec); unknown != "" {
		t.Fatalf("未知方言必须返回空串让调用方报错，实际 %q", unknown)
	}
}

// nil 库必须直接返回 nil 而不是 panic（121 §2 规范 2：28/29 个迁移都这么写）。
//
// 未知方言的报错分支无法在没有第二个真驱动的情况下构造（本仓 MySQL 测试
// 也是进程内假驱动），因此那条路径由纯函数 TestAnnouncementIndexDDLPerDialect
// 的「未知方言返回空串 → 调用方报错」覆盖。
func TestEnsureAnnouncementIndexesNilDB(t *testing.T) {
	if err := ensureAnnouncementIndexes(nil); err != nil {
		t.Fatalf("nil db 应当直接返回 nil（28/29 个迁移的写法）：%v", err)
	}
}

// 空库首启（连 system_settings 都没有）：表会建出来，但**绝不写标记**。
//
// 这是从 blacklist_migration.go 的注释里读出的一处不一致，实测为：
// 那份注释写「什么都不建，也绝不写标记」，但它的 applyBlacklistSchema 只要
// AutoMigrate 成功就返回 applied=true，表其实照样被建出来（注释与代码不符）。
//
// 本迁移选择**照建**，理由：
//   - 「先跳过、等 system_settings 建好再建表」会让首次部署在新库上空转一整个
//     启动周期，而公告读不到只是 fail-open 降级（service.ActiveIncidentsOrEmpty）；
//   - 建表是幂等的，重跑一次 AutoMigrate 代价可忽略。
//
// 不可让的那条不变：**标记绝不写**。否则 system_settings 建出来之后
// 本迁移会被永远跳过（121 §2.1 的反直觉细节）。
func TestEnsureAnnouncementSchemaWithoutSystemSettingsWritesNoFlag(t *testing.T) {
	dsn := "file:announcement-migration-nosettings-" + itoaAtomic(announcementTestDBSeq.Add(1)) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameSystemSetting) {
		t.Fatal("用例前置：本库不该有 system_settings")
	}
	if err := EnsureAnnouncementSchema(db); err != nil {
		t.Fatalf("无 system_settings 时不该报错（只是写不了标记）：%v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameAnnouncement) {
		t.Fatal("表应当被建出来（建表与标记写入是两件事）")
	}
	if db.Migrator().HasTable(entity.TableNameSystemSetting) {
		var count int64
		if err := db.Table(entity.TableNameSystemSetting).Count(&count).Error; err != nil {
			t.Fatalf("system_settings 不该被本迁移创建出来：%v", err)
		}
		if count != 0 {
			t.Fatal("本迁移不该往 system_settings 写任何行（连表都该由主流程建）")
		}
	}

	// 补上 system_settings 后重跑：这次标记必须落下，且第二次不再重跑。
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := EnsureAnnouncementSchema(db); err != nil {
		t.Fatalf("rerun with system_settings: %v", err)
	}
	if !systemSettingFlagIsSet(db, announcementsSchemaV1Key) {
		t.Fatal("system_settings 就位后必须补写标记（否则每次启动都重跑）")
	}
}

// nil 库直接返回 nil（121 §2 规范 2）。
func TestEnsureAnnouncementSchemaNilDB(t *testing.T) {
	if err := EnsureAnnouncementSchema(nil); err != nil {
		t.Fatalf("nil db 应当返回 nil：%v", err)
	}
}

// 迁移是幂等的：连续三次运行都不报错（121 §2「幂等」）。
func TestEnsureAnnouncementSchemaIsIdempotent(t *testing.T) {
	db := newAnnouncementMigrationTestDB(t)
	for i := 0; i < 3; i++ {
		if err := EnsureAnnouncementSchema(db); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if !db.Migrator().HasTable(entity.TableNameAnnouncement) {
		t.Fatal("三次运行后表必须存在")
	}
}

// 公告表**没有**任何哈希列（不像 blacklist_entries 的 value_hash）。
//
// 这条断言不是凑数：BL 系列表的唯一索引靠 value_hash，而公告不按内容去重
// （同一标题允许发两条 —— 运营可能先发一条、改完再发一条）。
// 若将来有人「照着黑名单抄」加了 value_hash + 唯一索引，
// 这条用例会立刻变红，提醒他确认「公告到底该不该去重」。
func TestAnnouncementSchemaHasNoHashColumn(t *testing.T) {
	db := newAnnouncementMigrationTestDB(t)
	if err := EnsureAnnouncementSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, column := range []string{"value_hash", "sha256", "hash"} {
		if db.Migrator().HasColumn(entity.TableNameAnnouncement, column) {
			t.Fatalf("公告表不该有哈希列 %s（112 §4.3 未定义）", column)
		}
	}
}
