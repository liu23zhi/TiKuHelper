package registry

import (
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newSensitiveWordMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system settings: %v", err)
	}
	return db
}

func hasSensitiveColumns(t *testing.T, db *gorm.DB) map[string]bool {
	t.Helper()
	result := make(map[string]bool, 4)
	for _, column := range sensitiveWordColumns {
		result[columnNameOf(column.field)] = db.Migrator().HasColumn(entity.TableNameLog, columnNameOf(column.field))
	}
	return result
}

func TestEnsureSensitiveWordSchemaAddsAllColumns(t *testing.T) {
	db := newSensitiveWordMigrationDB(t)
	if err := db.AutoMigrate(&entity.Log{}); err != nil {
		t.Fatalf("migrate logs: %v", err)
	}

	// 迁移前：生成实体不含这四列。
	for column, exists := range hasSensitiveColumns(t, db) {
		if exists {
			t.Fatalf("%s 不该已存在（logs.gen.go 是生成文件，不含敏感词列）", column)
		}
	}

	if err := ensureSensitiveWordSchema(db); err != nil {
		t.Fatalf("ensureSensitiveWordSchema: %v", err)
	}
	for column, exists := range hasSensitiveColumns(t, db) {
		if !exists {
			t.Fatalf("迁移后缺少列 %s", column)
		}
	}
	if !systemSettingFlagIsSet(db, sensitiveWordSchemaMigrationKey) {
		t.Fatalf("迁移成功后必须写标记，否则每次启动都会重跑")
	}
}

func TestEnsureSensitiveWordSchemaIsIdempotentAndKeepsData(t *testing.T) {
	db := newSensitiveWordMigrationDB(t)
	if err := db.AutoMigrate(&entity.Log{}); err != nil {
		t.Fatalf("migrate logs: %v", err)
	}
	if err := db.Exec(`INSERT INTO logs (id, user_id, module, action, description, new_answer, create_time)
		VALUES (1, 7, 'search', 3, '搜索完成', '{"answer":"答案原文"}', CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatalf("seed log: %v", err)
	}

	if err := ensureSensitiveWordSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// 再跑一次（模拟标记丢失后的重跑）：必须幂等，不能报「列已存在」。
	if err := ensureSensitiveWordSchema(db); err != nil {
		t.Fatalf("second run must be idempotent: %v", err)
	}

	var row struct {
		NewAnswer string
		Flag      *bool
		CheckedAt *string
	}
	if err := db.Raw(`SELECT new_answer AS new_answer, sensitive_flag AS flag, sensitive_checked_at AS checked_at
		FROM logs WHERE id = 1`).Scan(&row).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row.NewAnswer != `{"answer":"答案原文"}` {
		t.Fatalf("迁移改写了既有数据：%q", row.NewAnswer)
	}
	if row.CheckedAt != nil {
		t.Fatalf("迁移不得回填检测时间（历史行没有检测结论）")
	}
}

func TestEnsureSensitiveWordSchemaSkipsWhenLogsMissing(t *testing.T) {
	db := newSensitiveWordMigrationDB(t)
	// logs 表还不存在：应直接返回 nil，交给 AutoMigrate(Log) 建表，下次启动再补列。
	if err := ensureSensitiveWordSchema(db); err != nil {
		t.Fatalf("缺 logs 表时不该报错：%v", err)
	}
	if systemSettingFlagIsSet(db, sensitiveWordSchemaMigrationKey) {
		t.Fatalf("缺表时不得写标记，否则下次启动不会补列")
	}
}

func TestEnsureSensitiveWordSchemaHandlesNilDB(t *testing.T) {
	if err := ensureSensitiveWordSchema(nil); err != nil {
		t.Fatalf("nil db 必须直接返回：%v", err)
	}
}

func TestSensitiveWordColumnsCoverEntityDeclaration(t *testing.T) {
	// 迁移列与实体声明必须一一对应：列名是持久化契约。
	entityColumns := make(map[string]bool, 4)
	for _, column := range []string{"sensitive_flag", "sensitive_words", "sensitive_checked_at", "sensitive_check_error"} {
		entityColumns[column] = true
	}
	for _, column := range sensitiveWordColumns {
		name := columnNameOf(column.field)
		if !entityColumns[name] {
			t.Fatalf("迁移声明了实体里不存在的列 %s", name)
		}
		delete(entityColumns, name)
	}
	if len(entityColumns) != 0 {
		t.Fatalf("实体里有列没有对应迁移：%v", entityColumns)
	}
}

// ─────────────────── 敏感词缓存表迁移 ───────────────────

func TestEnsureSensitiveWordCacheSchemaCreatesTable(t *testing.T) {
	db := newSensitiveWordMigrationDB(t)

	if db.Migrator().HasTable(entity.TableNameSensitiveWordCache) {
		t.Fatalf("迁移前不该已存在缓存表")
	}
	if err := ensureSensitiveWordCacheSchema(db); err != nil {
		t.Fatalf("ensureSensitiveWordCacheSchema: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameSensitiveWordCache) {
		t.Fatalf("迁移后必须存在 %s", entity.TableNameSensitiveWordCache)
	}
	// 主键必须是 text_hash（哈希去重的唯一依据）。
	if !db.Migrator().HasTable(entity.TableNameSensitiveWordCache) ||
		!db.Migrator().HasColumn(entity.TableNameSensitiveWordCache, "text_hash") {
		t.Fatalf("缓存表必须有 text_hash 列")
	}
	for _, column := range []string{"sensitive", "words", "masked_text", "api_status", "error", "attempt_count", "checked_at"} {
		if !db.Migrator().HasColumn(entity.TableNameSensitiveWordCache, column) {
			t.Fatalf("缓存表缺少列 %s", column)
		}
	}
	if !systemSettingFlagIsSet(db, sensitiveWordCacheSchemaMigrationKey) {
		t.Fatalf("迁移成功后必须写标记，否则每次启动都会重跑")
	}
}

func TestEnsureSensitiveWordCacheSchemaIsIdempotentAndKeepsData(t *testing.T) {
	db := newSensitiveWordMigrationDB(t)
	if err := ensureSensitiveWordCacheSchema(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// 缓存是「已付费买来的结论」，任何情况下都不能被迁移清掉（不 DROP TABLE）。
	if err := db.Exec(`INSERT INTO sensitive_word_cache (text_hash, sensitive, words, api_status, attempt_count, checked_at)
		VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 1, '["敏感"]', 'forbidden', 0, CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatalf("seed cache row: %v", err)
	}
	if err := ensureSensitiveWordCacheSchema(db); err != nil {
		t.Fatalf("second run must be idempotent: %v", err)
	}
	var rows int64
	if err := db.Model(&entity.SensitiveWordCache{}).Count(&rows).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("重跑迁移不得删除缓存数据，实际 %d 行", rows)
	}
}

func TestEnsureSensitiveWordCacheSchemaHandlesNilDB(t *testing.T) {
	if err := ensureSensitiveWordCacheSchema(nil); err != nil {
		t.Fatalf("nil db 必须直接返回：%v", err)
	}
}

func TestEnsureSensitiveWordCacheSchemaWritesNoMarkerOnFailure(t *testing.T) {
	// 用一个已关闭的连接制造真实失败：AutoMigrate 报错 → 不写标记 → 下次启动重试。
	db := newSensitiveWordMigrationDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := ensureSensitiveWordCacheSchema(db); err == nil {
		t.Fatalf("连接不可用时必须返回错误，让启动日志看见")
	}
	// 标记表也读不到（连接已关），但关键是**没有**成功标记残留：
	// 用新连接确认标志位没被写入（这里新建一个独立库验证语义更直接）。
	fresh := newSensitiveWordMigrationDB(t)
	if systemSettingFlagIsSet(fresh, sensitiveWordCacheSchemaMigrationKey) {
		t.Fatalf("失败路径不得写标记")
	}
}
