package registry

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// task_doc/76 §3.2.2 / task_doc/77：用户归档字段迁移。

func openUserArchiveTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// 建出最小的 users 表（只有迁移关心的列之外的主键即可）。
	//
	// ⚠️ 这里是 **createLegacyUsersTable 的正当例外**，不要「统一改成 helper」：
	// 本文件测的正是 ensureUserArchiveSchema 补 deleted_at 这件事，
	// 表里预置 deleted_at 就等于把被测行为做完了（测试恒绿、测不出任何东西）。
	// 其它 schema 测试预置 deleted_at 是因为它属于「机制列」而非被测产物。
	//
	// 代价：这里的查询一律用 raw SQL / Migrator，不能用 entity.User 走 GORM
	// （软删除条件会撞上还不存在的 deleted_at）。见下方断言。
	if err := db.Exec("CREATE TABLE " + entity.TableNameUser + " (id integer primary key, username varchar(255))").Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	// 迁移标记依赖 system_settings（systemSettingFlagIsSet / setSystemSettingFlag）。
	// 用真实的实体建表，而不是手写最小列 —— 手写列会随实体演进（如新增 remark）
	// 静默失配，表现为「测试里 migrate 突然报 no column named …」。
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("create system_settings: %v", err)
	}
	return db
}

// TestEnsureUserArchiveSchemaAddsColumnsAndIndex 验证四列 + 索引都建出来。
func TestEnsureUserArchiveSchemaAddsColumnsAndIndex(t *testing.T) {
	db := openUserArchiveTestDB(t)

	if err := ensureUserArchiveSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, column := range []string{"deleted_at", "archived_reason", "archived_at", "archived_by_user_id"} {
		if !db.Migrator().HasColumn(entity.TableNameUser, column) {
			t.Fatalf("column %s missing after migration", column)
		}
	}
	// deleted_at 必须建索引：软删除机制会在每条查询里带这个条件。
	if !db.Migrator().HasIndex(entity.TableNameUser, "idx_users_deleted_at") {
		t.Fatal("users.deleted_at index missing: every soft-delete query would full-scan")
	}
}

// TestEnsureUserArchiveSchemaIsIdempotent 迁移必须能重复跑。
func TestEnsureUserArchiveSchemaIsIdempotent(t *testing.T) {
	db := openUserArchiveTestDB(t)
	if err := ensureUserArchiveSchema(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// 清掉标记，强制重跑一遍（模拟「标记被误删」）。
	if err := db.Exec("DELETE FROM system_settings WHERE key = ?", userArchiveSchemaMigrationKey).Error; err != nil {
		// system_settings 可能还没建 —— 那说明标记判定本身会跳过，重跑无害。
		t.Logf("skip marker reset: %v", err)
	}
	if err := ensureUserArchiveSchema(db); err != nil {
		t.Fatalf("second migrate must not fail: %v", err)
	}
}

// TestUserArchiveSoftDeleteHidesRowFromDefaultQuery 是最关键的一条：
// 归档后，默认查询（不带 Unscoped）必须看不到这一行。
//
// 这条守的是「用 gorm.DeletedAt 而不是自造列」这个决定的实际收益 ——
// 如果哪天有人把 UserArchive 的 DeletedAt 改成普通 *time.Time，
// 这条测试会红，提醒他所有既有查询都要手补过滤条件。
func TestUserArchiveSoftDeleteHidesRowFromDefaultQuery(t *testing.T) {
	db := openUserArchiveTestDB(t)
	if err := ensureUserArchiveSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Exec("INSERT INTO " + entity.TableNameUser + " (id, username) VALUES (1, 'actor'), (2, 'target')").Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 归档 id=2。
	if err := db.Model(&entity.UserArchive{}).Where("id = ?", 2).Updates(map[string]any{
		"deleted_at":          "2026-11-11 12:00:00",
		"archived_reason":     entity.UserArchivedReasonAdminDelete,
		"archived_at":         "2026-11-11 12:00:00",
		"archived_by_user_id": 1,
	}).Error; err != nil {
		t.Fatalf("archive: %v", err)
	}

	var visible []entity.UserArchive
	if err := db.Where("id = ?", 2).Find(&visible).Error; err != nil {
		t.Fatalf("default query: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("archived user must be hidden from default queries, got %d rows", len(visible))
	}

	// Unscoped 必须能看到 —— 归档管理页依赖这个。
	var unscoped []entity.UserArchive
	if err := db.Unscoped().Where("id = ?", 2).Find(&unscoped).Error; err != nil {
		t.Fatalf("unscoped query: %v", err)
	}
	if len(unscoped) != 1 {
		t.Fatalf("archived user must be visible via Unscoped, got %d rows", len(unscoped))
	}
	if unscoped[0].ArchivedReason != entity.UserArchivedReasonAdminDelete {
		t.Fatalf("archived reason lost: %+v", unscoped[0])
	}
	if unscoped[0].ArchivedByUserID != 1 {
		t.Fatalf("archived operator lost: %+v", unscoped[0])
	}

	// 未归档的那一行仍然可见。
	var stillVisible []entity.UserArchive
	if err := db.Where("id = ?", 1).Find(&stillVisible).Error; err != nil {
		t.Fatalf("query actor: %v", err)
	}
	if len(stillVisible) != 1 {
		t.Fatal("active user must remain visible")
	}
}
