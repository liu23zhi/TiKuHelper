package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const userArchiveSchemaMigrationKey = "user_archive_schema_v1"

// userArchiveColumns 是 users 表上归档需要的四列（task_doc/76 §3.2.2）。
//
// 顺序无关，HasColumn 幂等；名字与 entity.UserArchive 的 column tag 必须一致。
//
// ⚠️ deleted_at 必须建**索引**：归档管理页按它筛选（`deleted_at IS NOT NULL`
// 或 IS NULL），GORM 的软删除机制也会在每条查询里带上这个条件 ——
// 没有索引时用户列表每次全表扫描。
var userArchiveColumns = []struct {
	field  string
	sqlite string
}{
	// 软删除标记。用 datetime 而非 boolean：要能回答「什么时候归档的」。
	{field: "DeletedAt", sqlite: "deleted_at datetime"},
	// 归档原因：admin_delete / user_closure。两者合规义务不同，不能合并。
	{field: "ArchivedReason", sqlite: "archived_reason varchar(32) not null default ''"},
	// 归档时点。与 deleted_at 同值，但业务展示用这个
	// （deleted_at 是 GORM 机制字段，不该直接暴露给前端）。
	{field: "ArchivedAt", sqlite: "archived_at datetime"},
	// 操作者：管理员删除时是管理员 ID，用户自助注销时是本人 ID。
	{field: "ArchivedByUserID", sqlite: "archived_by_user_id integer not null default 0"},
}

// userArchiveColumnName 把 Go 字段名映射成列名。
//
// 用显式映射而不是反射：列名是持久化契约，改字段名不该悄悄改列名。
func userArchiveColumnName(field string) string {
	switch field {
	case "DeletedAt":
		return "deleted_at"
	case "ArchivedReason":
		return "archived_reason"
	case "ArchivedAt":
		return "archived_at"
	case "ArchivedByUserID":
		return "archived_by_user_id"
	}
	return ""
}

// ensureUserArchiveSchema 给 users 表补上归档四列，并为 deleted_at 建索引。
//
// 纪律与其它 ensure* 迁移一致：
//   - 结构性建列失败返回错误，让启动 FATAL 看见；
//   - 用 system_settings 标记保证只跑一次，失败不写标记，下次启动自动重试；
//   - 外层 recover 兜底，脏数据不能让服务反复崩溃；
//   - 只建列不回填：历史行 deleted_at 为 NULL 即「未归档」，
//     这是正确的默认值，不需要 UPDATE 全表。
//
// 无回填 ⇒ 无大表扫描，不存在「逐行扫大表」的性能风险。
func ensureUserArchiveSchema(db *gorm.DB) (err error) {
	if db == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("用户归档字段迁移异常: %v", recovered)
		}
	}()
	if systemSettingFlagIsSet(db, userArchiveSchemaMigrationKey) {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		// users 还没建出来（极端空库首次启动）：交给 AutoMigrate(User)，
		// 这里直接返回，下次启动再补列。
		return nil
	}
	for _, column := range userArchiveColumns {
		if db.Migrator().HasColumn(entity.TableNameUser, userArchiveColumnName(column.field)) {
			continue
		}
		if err := addUserArchiveColumn(db, column.field, column.sqlite); err != nil {
			return err
		}
	}
	// 索引单独建：AddColumn 不会建索引，而软删除查询每次都会带 deleted_at 条件。
	if err := ensureUserArchiveDeletedAtIndex(db); err != nil {
		return err
	}
	return setSystemSettingFlag(db, userArchiveSchemaMigrationKey)
}

func addUserArchiveColumn(db *gorm.DB, field string, sqliteDDL string) error {
	column := userArchiveColumnName(field)
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		statement := "ALTER TABLE " + entity.TableNameUser + " ADD COLUMN " + sqliteDDL
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("新增用户归档字段 %s 失败: %w", column, err)
		}
		return nil
	}
	// MySQL：列类型由 entity.UserArchive 的 tag 提供，这里只借用字段名。
	if err := db.Migrator().AddColumn(&entity.UserArchive{}, field); err != nil {
		return fmt.Errorf("新增用户归档字段 %s 失败: %w", column, err)
	}
	return nil
}

// ensureUserArchiveDeletedAtIndex 为 users.deleted_at 建索引。
//
// HasIndex 判定用 GORM 的命名（idx_users_deleted_at）；
// 已存在则跳过，保证幂等。
func ensureUserArchiveDeletedAtIndex(db *gorm.DB) error {
	if db.Migrator().HasIndex(entity.TableNameUser, "idx_users_deleted_at") {
		return nil
	}
	if err := db.Exec("CREATE INDEX idx_users_deleted_at ON " + entity.TableNameUser + " (deleted_at)").Error; err != nil {
		// 索引建失败不算迁移失败：功能仍可用，只是用户列表会慢。
		// 不回滚、不阻断启动 —— 用日志让人知道。
		return fmt.Errorf("建立 users.deleted_at 索引失败（功能可用，但查询会慢）: %w", err)
	}
	return nil
}
