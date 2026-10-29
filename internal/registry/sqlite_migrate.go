package registry

import (
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// 为什么 SQLite 上必须关掉 AutoMigrate 的「改列」动作（2026-10-16 启动慢事故）
//
// GORM 的 AutoMigrate 对已有表会逐列比对「实体声明」与「数据库实际」，
// 一旦判定不一致就调用 AlterColumn，而 SQLite 驱动（glebarez/sqlite）的
// AlterColumn 代价是**整表 copy-rename**（CREATE __temp → INSERT SELECT → DROP → RENAME）。
//
// 实测有两类列永远比对不相等，于是每次启动都重建，且结果永远收敛不了：
//
//  1. 显示宽度 vs 位宽：实体声明 `type:int(11)` / `tinyint(4)` / `decimal(18,8)`，
//     驱动把括号里的 11 / 4 / 18 当作 length，而 GORM 对 int 字段记录的 Size 是
//     Go 的位宽（32/64），两者恒不相等；
//  2. 复合唯一索引：`uniqueIndex:hash_course_idx` 这种复合唯一索引在 SQLite 里是
//     独立 `CREATE UNIQUE INDEX`，驱动解析 DDL 后把这两列标成 unique=true，
//     而 schema.Field.Unique 为 false，同样恒不相等。
//
// 线上表现：一次启动 32 次 copy-rename、146 条重建语句、约 1.46s（logs 6 万行重建 3 次、
// billing_records 4406 行重建 6 次、tiku 3 次…），数据量越大越慢。
//
// 因此这里只拦掉「改列」：建表、补列（AddColumn）、建索引、建约束全部保留，
// 结构性变更继续由各 ensure*Schema 显式迁移负责 —— 这也是本项目既有的约定
// （见 AGENTS.md 第 26 条）。
type sqliteQuietMigrator struct {
	gorm.Migrator
}

// MigrateColumn 直接放行：不再比对列定义，也就不会触发整表重建。
func (m sqliteQuietMigrator) MigrateColumn(value interface{}, field *schema.Field, columnType gorm.ColumnType) error {
	return nil
}

// sqliteQuietDialector 只替换 Migrator()，其余行为完全交给原 Dialector。
type sqliteQuietDialector struct {
	gorm.Dialector
}

func (d sqliteQuietDialector) Migrator(db *gorm.DB) gorm.Migrator {
	return sqliteQuietMigrator{Migrator: d.Dialector.Migrator(db)}
}

// Embedding gorm.Dialector hides optional interfaces. Forward savepoints so
// account updates keep their nested transaction rollback semantics.
func (d sqliteQuietDialector) SavePoint(tx *gorm.DB, name string) error {
	if driver, ok := d.Dialector.(gorm.SavePointerDialectorInterface); ok {
		return driver.SavePoint(tx, name)
	}
	return gorm.ErrUnsupportedDriver
}

func (d sqliteQuietDialector) RollbackTo(tx *gorm.DB, name string) error {
	if driver, ok := d.Dialector.(gorm.SavePointerDialectorInterface); ok {
		return driver.RollbackTo(tx, name)
	}
	return gorm.ErrUnsupportedDriver
}
