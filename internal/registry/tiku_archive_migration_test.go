package registry

import (
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// legacyTikuArchiveRow 复刻「加软删除之前」的 tiku 表结构。
//
// 用它建表而不是直接 AutoMigrate(entity.Tiku)：后者会自带 deleted_at 列，
// 那样测的只是「列本来就在」，测不出补列逻辑本身（等于假钉子）。
type legacyTikuArchiveRow struct {
	ID         int32  `gorm:"primaryKey;autoIncrement"`
	Question   string `gorm:"column:question;type:longtext;not null"`
	Type       int32  `gorm:"column:type;type:int(11);not null"`
	Options    string `gorm:"column:options;type:longtext;not null"`
	Answer     string `gorm:"column:answer;type:longtext;not null"`
	Hash       string `gorm:"column:hash;type:char(32);not null"`
	CourseName string `gorm:"column:course_name;type:varchar(255)"`
	Extra      string `gorm:"column:extra;type:text"`
	SourceName string `gorm:"column:source_name;type:varchar(255)"`
}

func (*legacyTikuArchiveRow) TableName() string { return entity.TableNameTiku }

// newTikuArchiveMigrationDB 建一个只含旧列（无 deleted_at）的题库表。
func newTikuArchiveMigrationDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:tiku-archive-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&legacyTikuArchiveRow{}, &entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate legacy schema: %v", err)
	}
	return db
}

// TestEnsureTikuArchiveSchemaCreatesColumnsAndIndex 是 Q77-1 迁移的钉子。
//
// 三件事必须成立：
//  1. 四列被补上（deleted_at / archived_reason / archived_at / archived_by_user_id）；
//  2. idx_tikus_deleted_at 索引被建出来 —— 软删除会给**每条**题库查询带上
//     deleted_at IS NULL，没索引就是全表扫描（红线 4）；
//  3. 一次性 system_settings 标记写入且重复执行不重复建。
//
// ⚠️ 变异验证目标：注释掉 ensureTikuArchiveDeletedAtIndex 里的 CREATE INDEX
// 后，第 2 条必须变红（HasIndex=false）。
func TestEnsureTikuArchiveSchemaCreatesColumnsAndIndex(t *testing.T) {
	db := newTikuArchiveMigrationDB(t)

	legacy := legacyTikuArchiveRow{
		Question:   "软删除迁移不能动历史行",
		Type:       0,
		Options:    `["A","B"]`,
		Answer:     `["A"]`,
		Hash:       "0123456789abcdef0123456789abcdef",
		CourseName: "合成迁移样例",
		Extra:      `{"tag":"keep"}`,
		SourceName: "AI 搜索",
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	// 跑两遍：第二遍必须是空操作（幂等）。
	for pass := 0; pass < 2; pass++ {
		if err := ensureTikuArchiveSchema(db); err != nil {
			t.Fatalf("ensureTikuArchiveSchema pass %d: %v", pass+1, err)
		}
	}

	for _, column := range []string{"deleted_at", "archived_reason", "archived_at", "archived_by_user_id"} {
		if !db.Migrator().HasColumn(entity.TableNameTiku, column) {
			t.Errorf("missing tiku column %q after ensureTikuArchiveSchema", column)
		}
	}

	if !db.Migrator().HasIndex(entity.TableNameTiku, entity.TikuArchiveDeletedAtIndex) {
		t.Errorf("missing index %q on tiku(deleted_at): soft-delete queries would full-scan the table",
			entity.TikuArchiveDeletedAtIndex)
	}

	var marker entity.SystemSetting
	if err := db.Where(map[string]any{"key": entity.TikuArchiveSchemaVersionKey}).First(&marker).Error; err != nil {
		t.Fatalf("missing one-shot marker %q: %v", entity.TikuArchiveSchemaVersionKey, err)
	}
	if strings.TrimSpace(marker.Value) != "1" {
		t.Fatalf("marker %q value=%q, want 1", entity.TikuArchiveSchemaVersionKey, marker.Value)
	}
	var markerRows int64
	if err := db.Model(&entity.SystemSetting{}).
		Where(map[string]any{"key": entity.TikuArchiveSchemaVersionKey}).Count(&markerRows).Error; err != nil {
		t.Fatalf("count marker rows: %v", err)
	}
	if markerRows != 1 {
		t.Fatalf("marker rows=%d, want 1 (idempotent rerun must not duplicate)", markerRows)
	}

	// 历史行必须原样保留，且默认（未归档）状态的 deleted_at 为 NULL。
	var question string
	var archived int64
	if err := db.Raw("SELECT question FROM tiku WHERE id = ?", legacy.ID).Row().Scan(&question); err != nil {
		t.Fatalf("read legacy row: %v", err)
	}
	if question != legacy.Question {
		t.Fatalf("legacy question=%q, want %q", question, legacy.Question)
	}
	if err := db.Raw("SELECT COUNT(*) FROM tiku WHERE id = ? AND deleted_at IS NULL", legacy.ID).Scan(&archived).Error; err != nil {
		t.Fatalf("inspect deleted_at: %v", err)
	}
	if archived != 1 {
		t.Fatalf("legacy row must stay non-archived after migration, matched=%d", archived)
	}
}
