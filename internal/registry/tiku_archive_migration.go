package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// tikuArchiveColumn（列名 -> 显式 ALTER 用的列类型）按固定顺序声明。
//
// 用显式 ALTER 而不是只依赖 AutoMigrate：题库表在旧库上可能由更早的版本建出来，
// AutoMigrate 在某些 SQLite 包装（sqliteQuietDialector）下会跳过逐列改写，
// 而缺列会让**每一条** dao.Tiku 查询直接报错（题库整体不可用），必须自己兜住。
var tikuArchiveColumns = []struct {
	Name string
	Type string
}{
	{Name: "deleted_at", Type: "datetime"},
	{Name: "archived_reason", Type: "varchar(32)"},
	{Name: "archived_at", Type: "datetime"},
	{Name: "archived_by_user_id", Type: "integer"},
}

// ensureTikuArchiveSchema 为题库表补齐软删除（归档）所需的四列与 deleted_at 索引。
//
// 口径来源：45-用户待确认问题清单.md §0.26.2 / 77-数据不可真删除与操作日志覆盖.md §3.1 §11.3。
//
// 三条纪律：
//  1. 只补列、不删列、不改既有列类型 —— 题库是核心业务数据；
//  2. HasColumn / HasIndex 双幂等，任何驱动上重复执行都是空操作；
//  3. 结构性失败返回 error 由调用方 FATAL 阻断启动：缺 deleted_at 列时
//     dao.Tiku 的每条查询都会失败，带病启动比直接退出更难排查。
func ensureTikuArchiveSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameTiku) {
		// 全新库由 AutoMigrate 建表（含 deleted_at），这里无事可做。
		return nil
	}

	if err := ensureTikuArchiveColumns(db); err != nil {
		return err
	}
	// ⚠️ 索引必须单独建：gorm.DeletedAt 字段上没有 index 标签，
	// AutoMigrate 不会创建它，而软删除条件会命中每一条题库查询。
	if err := ensureTikuArchiveDeletedAtIndex(db); err != nil {
		return err
	}

	if systemSettingFlagIsSet(db, entity.TikuArchiveSchemaVersionKey) {
		return nil
	}
	if err := setSystemSettingFlag(db, entity.TikuArchiveSchemaVersionKey); err != nil {
		// 标记写失败不影响正确性：下次启动会重新走一遍幂等的列/索引检查。
		logger.SysLog(fmt.Sprintf("写入题库归档迁移标记失败（下次启动会重新校验，不影响使用）：%v", err))
		return nil
	}
	logger.SysLog("题库软删除（归档）列与 idx_tikus_deleted_at 索引已就绪")
	return nil
}

// ensureTikuArchiveColumns 逐个补列，已存在则跳过。
func ensureTikuArchiveColumns(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	for _, column := range tikuArchiveColumns {
		if db.Migrator().HasColumn(entity.TableNameTiku, column.Name) {
			continue
		}
		statement := "ALTER TABLE " + entity.TableNameTiku + " ADD COLUMN " + column.Name + " " + column.Type
		if err := db.Exec(statement).Error; err != nil {
			// 并发启动的另一个实例可能刚好补上了同名列：复查一次再决定是否是真失败。
			if db.Migrator().HasColumn(entity.TableNameTiku, column.Name) {
				continue
			}
			return fmt.Errorf("补 %s.%s 列失败: %w", entity.TableNameTiku, column.Name, err)
		}
	}
	return nil
}

// ensureTikuArchiveDeletedAtIndex 建 deleted_at 索引（幂等）。
//
// 索引名用 entity.TikuArchiveDeletedAtIndex，与 AutoMigrate 可能生成的默认名无关：
// 这里刻意不让 GORM 建（字段上无 index 标签），只由本函数建，测试也据此断言。
func ensureTikuArchiveDeletedAtIndex(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	if db.Migrator().HasIndex(entity.TableNameTiku, entity.TikuArchiveDeletedAtIndex) {
		return nil
	}
	// 不同库对索引名的长度限制不同，但 idx_tikus_deleted_at（20 字符）在两处都安全。
	statement := "CREATE INDEX " + entity.TikuArchiveDeletedAtIndex + " ON " + entity.TableNameTiku + " (deleted_at)"
	if err := db.Exec(statement).Error; err != nil {
		if isIndexAlreadyExistsError(err) || db.Migrator().HasIndex(entity.TableNameTiku, entity.TikuArchiveDeletedAtIndex) {
			return nil
		}
		return fmt.Errorf("建 %s 索引失败: %w", entity.TikuArchiveDeletedAtIndex, err)
	}
	return nil
}

// isIndexAlreadyExistsError 判断「索引已存在」类错误。
//
// 覆盖 SQLite（index ... already exists / 1555）与 MySQL（Duplicate key name / 1061）。
// GORM 只在开启 TranslateError 时返回 ErrDuplicatedKey，本项目没开，因此按文本判断。
func isIndexAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"already exists", "duplicate key name", "index_exists"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
