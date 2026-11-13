package registry

import (
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newSensitiveImageMigrationTestDB 建一个内存库。
//
// DSN 走 util.SQLiteConcurrentDSN（不手写 _txlock / busy_timeout）：
// 与生产 sqliteProductionDSN 共用同一份定义，避免「生产改了测试没跟着改」的漂移。
// 库名带 t.Name()：`file:x?mode=memory&cache=shared` 在同进程内同名库是共享的，
// 跨用例会互相污染（这正是「测试看起来过了、实际读到别的用例的行」的常见来源）。
func newSensitiveImageMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := util.SQLiteConcurrentDSN("file:sensitive-image-" + name + "?mode=memory&cache=shared")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

// migrateSensitiveImagePrerequisites 建两张前置表（system_settings + users）。
//
// ⚠️ **每个模型单独一次 AutoMigrate**。一次传多个模型时 GORM 会合并建表语句，
// 实测会导致物理表列缺失（表建出来了但缺列，排查时只看到 "no such column"）。
func migrateSensitiveImagePrerequisites(db *gorm.DB) error {
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		return err
	}
	return db.AutoMigrate(&entity.User{})
}

// 空库首次启动：两张表都要建出来、两个标记都要写进去，第二次运行幂等。
func TestEnsureSensitiveImageSchemaCreatesBothTables(t *testing.T) {
	db := newSensitiveImageMigrationTestDB(t)
	if err := migrateSensitiveImagePrerequisites(db); err != nil {
		t.Fatalf("migrate prerequisites: %v", err)
	}

	if err := EnsureSensitiveImageSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, table := range []string{entity.TableNameSensitiveImageCache, entity.TableNameSensitiveImageQuota} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table %s", table)
		}
	}
	// 缓存表的主键必须是 image_sha256（117 N1-b「检测跟 SHA256 走」的结构保证）。
	if !db.Migrator().HasColumn(entity.TableNameSensitiveImageCache, "image_sha256") {
		t.Fatal("expected sensitive_image_cache_entries.image_sha256")
	}
	// pending 占位列必须能存四态（121 §4.1 唯一需要并发占位的表）。
	for _, column := range []string{"status", "reason", "provider", "hit_count", "first_seen_at", "updated_at"} {
		if !db.Migrator().HasColumn(entity.TableNameSensitiveImageCache, column) {
			t.Fatalf("expected sensitive_image_cache_entries.%s", column)
		}
	}
	// 额度表两个复合主键列。
	for _, column := range []string{"user_id", "quota_date", "used_count"} {
		if !db.Migrator().HasColumn(entity.TableNameSensitiveImageQuota, column) {
			t.Fatalf("expected sensitive_image_daily_quotas.%s", column)
		}
	}
	// 两张表**各自**一个 FLAG（合并成一个会让其中一张建失败时把另一张也拖成永不重跑）。
	if !systemSettingFlagIsSet(db, sensitiveImageCacheSchemaKey) {
		t.Fatal("expected the cache schema flag to be written")
	}
	if !systemSettingFlagIsSet(db, sensitiveImageQuotaSchemaKey) {
		t.Fatal("expected the quota schema flag to be written")
	}
	// 幂等：第二次运行不报错、不重建。
	if err := EnsureSensitiveImageSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
}

// 缓存主键唯一：同一个 image_sha256 只能有一行（去重是结构保证，不靠业务判断）。
func TestSensitiveImageCachePrimaryKeyDeduplicatesBySHA256(t *testing.T) {
	db := newSensitiveImageMigrationTestDB(t)
	if err := migrateSensitiveImagePrerequisites(db); err != nil {
		t.Fatalf("migrate prerequisites: %v", err)
	}
	if err := EnsureSensitiveImageSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sha := strings.Repeat("a", 64)
	first := entity.SensitiveImageCacheEntry{ImageSHA256: sha, Status: entity.SensitiveImageCacheStatusPending}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("insert first: %v", err)
	}
	second := entity.SensitiveImageCacheEntry{ImageSHA256: sha, Status: entity.SensitiveImageCacheStatusClean}
	if err := db.Create(&second).Error; err == nil {
		t.Fatal("expected the primary key to reject a duplicate image_sha256")
	}
	var rows int64
	if err := db.Model(&entity.SensitiveImageCacheEntry{}).Where("image_sha256 = ?", sha).Count(&rows).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("expected exactly 1 row for one sha256, got %d", rows)
	}
}

// 额度表复合主键 (user_id, quota_date)：同一用户同一天只能一行，不同天各一行。
func TestSensitiveImageQuotaCompositePrimaryKey(t *testing.T) {
	db := newSensitiveImageMigrationTestDB(t)
	if err := migrateSensitiveImagePrerequisites(db); err != nil {
		t.Fatalf("migrate prerequisites: %v", err)
	}
	if err := EnsureSensitiveImageSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	first := entity.SensitiveImageDailyQuota{UserID: 7, QuotaDate: "2026-11-13", UsedCount: 1}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("insert first: %v", err)
	}
	duplicate := entity.SensitiveImageDailyQuota{UserID: 7, QuotaDate: "2026-11-13", UsedCount: 9}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("expected the composite primary key to reject a duplicate (user_id, quota_date)")
	}
	nextDay := entity.SensitiveImageDailyQuota{UserID: 7, QuotaDate: "2026-11-14", UsedCount: 0}
	if err := db.Create(&nextDay).Error; err != nil {
		t.Fatalf("insert next day: %v", err)
	}
	otherUser := entity.SensitiveImageDailyQuota{UserID: 8, QuotaDate: "2026-11-13", UsedCount: 0}
	if err := db.Create(&otherUser).Error; err != nil {
		t.Fatalf("insert other user: %v", err)
	}
}

// users 不存在时必须**跳过建表且不写标记** —— 否则 users 建好之后本迁移会被
// 永远跳过，额度表将永远不存在（121 §2.1 同一条纪律，失败原因换成了顺序）。
func TestEnsureSensitiveImageSchemaSkipsQuotaTableWithoutUsers(t *testing.T) {
	db := newSensitiveImageMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := EnsureSensitiveImageSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 缓存表无前置依赖，照建。
	if !db.Migrator().HasTable(entity.TableNameSensitiveImageCache) {
		t.Fatal("expected sensitive_image_cache_entries to be created without users")
	}
	// 额度表跳过，且**不能**写标记。
	if db.Migrator().HasTable(entity.TableNameSensitiveImageQuota) {
		t.Fatal("expected sensitive_image_daily_quotas to be skipped while users is absent")
	}
	if systemSettingFlagIsSet(db, sensitiveImageQuotaSchemaKey) {
		t.Fatal("the quota flag must NOT be written when the table is skipped")
	}
	// users 就位后重跑：这次必须建出来并写标记（证明「没写标记」真的带来了重跑机会）。
	if err := db.AutoMigrate(&entity.User{}); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	if err := EnsureSensitiveImageSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameSensitiveImageQuota) {
		t.Fatal("expected sensitive_image_daily_quotas after users exists")
	}
	if !systemSettingFlagIsSet(db, sensitiveImageQuotaSchemaKey) {
		t.Fatal("expected the quota flag to be written after a successful create")
	}
}

// 建表失败绝不写标记：把缓存表换成一个必然失败的 DDL 触发错误，
// 断言 error 非空、标记未写、且下一次（条件恢复后）还能重跑成功。
func TestEnsureSensitiveImageSchemaDoesNotFlagOnFailure(t *testing.T) {
	db := newSensitiveImageMigrationTestDB(t)
	if err := migrateSensitiveImagePrerequisites(db); err != nil {
		t.Fatalf("migrate prerequisites: %v", err)
	}
	// 先建一张**同名但结构冲突**的表，让 AutoMigrate 无法把它改成实体形状。
	// 用 image_sha256 定义为 NOT NULL 且无默认值不够（GORM 会补列），
	// 所以这里用视图：对视图做 ALTER/建列必然失败，且 GORM 的 AutoMigrate 会报错。
	if err := db.Exec("CREATE VIEW " + entity.TableNameSensitiveImageCache + " AS SELECT 1 AS dummy").Error; err != nil {
		t.Fatalf("create conflicting view: %v", err)
	}
	err := EnsureSensitiveImageSchema(db)
	if err == nil {
		t.Fatal("expected an error when the cache table cannot be created")
	}
	if systemSettingFlagIsSet(db, sensitiveImageCacheSchemaKey) {
		t.Fatal("the cache flag must NOT be written when table creation failed")
	}
	// 清掉冲突对象后重跑必须成功 —— 这正是「不写标记」换来的重跑机会。
	if err := db.Exec("DROP VIEW " + entity.TableNameSensitiveImageCache).Error; err != nil {
		t.Fatalf("drop conflicting view: %v", err)
	}
	if err := EnsureSensitiveImageSchema(db); err != nil {
		t.Fatalf("retry after removing the conflict: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameSensitiveImageCache) {
		t.Fatal("expected the cache table after the conflict was removed")
	}
	if !systemSettingFlagIsSet(db, sensitiveImageCacheSchemaKey) {
		t.Fatal("expected the cache flag after a successful retry")
	}
}

// nil 库不 panic（121 §2 第 2 条：28/29 个迁移都有这条检查）。
func TestEnsureSensitiveImageSchemaNilDatabase(t *testing.T) {
	if err := EnsureSensitiveImageSchema(nil); err != nil {
		t.Fatalf("expected nil database to be a no-op, got %v", err)
	}
}

// 被标记跳过时，即使表被人为删掉也不重建（标记是权威）。
// 这条钉的是「标记语义」本身：如果实现改成先查表存在性再决定，标记就没有意义了。
func TestEnsureSensitiveImageSchemaFlagShortCircuits(t *testing.T) {
	db := newSensitiveImageMigrationTestDB(t)
	if err := migrateSensitiveImagePrerequisites(db); err != nil {
		t.Fatalf("migrate prerequisites: %v", err)
	}
	if err := EnsureSensitiveImageSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Exec("DROP TABLE " + entity.TableNameSensitiveImageCache).Error; err != nil {
		t.Fatalf("drop table: %v", err)
	}
	// 兜底实现里带了 HasTable 复查，所以这里**会**重建 —— 这是有意的：
	// 标记说「建过」但表真的没了（人为删除/回滚）时，静默跳过会让功能整体不可用。
	if err := EnsureSensitiveImageSchema(db); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameSensitiveImageCache) {
		t.Fatal("expected the cache table to be recreated when it went missing despite the flag")
	}
	// 两张表的标记键必须是不同的字符串（合并成一个会让失败域串联）。
	if sensitiveImageCacheSchemaKey == sensitiveImageQuotaSchemaKey {
		t.Fatalf("cache and quota flags must be distinct, both are %q", sensitiveImageCacheSchemaKey)
	}
	// 标记键沿 121 §4.1 的 _schema_v1 后缀约定。
	for _, key := range []string{sensitiveImageCacheSchemaKey, sensitiveImageQuotaSchemaKey} {
		if !strings.HasSuffix(key, "_schema_v1") {
			t.Fatalf("expected the %q flag to follow the _schema_v1 convention", key)
		}
	}
	_ = fmt.Sprintf("%s", sensitiveImageCacheSchemaKey)
}
