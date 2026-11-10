package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

const sensitiveWordSchemaMigrationKey = "sensitive_word_schema_v1"

// sensitiveWordColumns 是 logs 表上敏感词打标需要的四列。
//
// 顺序无关，HasColumn 幂等；名字与 entity.LogSensitive 的 column tag 必须一致。
var sensitiveWordColumns = []struct {
	field  string
	sqlite string
	mysql  string
}{
	// 只打标记不过滤：Flag 为 true 表示检测命中，答案原文保持不动。
	{field: "SensitiveFlag", sqlite: "sensitive_flag boolean not null default 0", mysql: "sensitive_flag tinyint(1) not null default 0 comment '命中敏感词（只打标记不过滤）'"},
	// 命中明细按检测对象分组存 JSON，便于区分题目命中与答案命中。
	{field: "SensitiveWords", sqlite: "sensitive_words text", mysql: "sensitive_words longtext comment '命中明细（按 question/answer 分组的 JSON）'"},
	// 检测时间留空表示「从未检测」（开关关闭或历史行）；前端据此区分「未检测」与「检测通过」。
	{field: "SensitiveCheckedAt", sqlite: "sensitive_checked_at datetime", mysql: "sensitive_checked_at datetime comment '检测完成时间'"},
	// 检测失败原因只作排障取证，绝不影响答题结果。
	{field: "SensitiveCheckError", sqlite: "sensitive_check_error varchar(255)", mysql: "sensitive_check_error varchar(255) comment '检测失败原因'"},
}

// ensureSensitiveWordSchema 给 logs 表补上敏感词打标的四列。
//
// 纪律与其它 ensure* 迁移一致：
//   - 结构性建列失败返回错误，让启动 FATAL 看见；
//   - 用 system_settings 标记保证只跑一次，失败不写标记，下次启动自动重试；
//   - 外层 recover 兜底，脏数据不能让服务反复崩溃；
//   - 只建列不回填：历史行没有检测结果，保持空值即「从未检测」，
//     不回填成 false（那会把「没检测」伪装成「检测通过」）。
//
// 无回填 ⇒ 无大表扫描，不存在「逐行扫大表」的性能风险。
func ensureSensitiveWordSchema(db *gorm.DB) (err error) {
	if db == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("敏感词打标字段迁移异常: %v", recovered)
		}
	}()
	if systemSettingFlagIsSet(db, sensitiveWordSchemaMigrationKey) {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameLog) {
		// logs 还没建出来（极端空库首次启动）：交给 AutoMigrate(Log)，
		// 这里直接返回，下次启动再补列。
		return nil
	}
	for _, column := range sensitiveWordColumns {
		if db.Migrator().HasColumn(entity.TableNameLog, columnNameOf(column.field)) {
			continue
		}
		if err := addSensitiveWordColumn(db, column.field, column.sqlite, column.mysql); err != nil {
			return err
		}
	}
	return setSystemSettingFlag(db, sensitiveWordSchemaMigrationKey)
}

// sensitiveWordCacheSchemaMigrationKey 是缓存表迁移的一次性标记。
const sensitiveWordCacheSchemaMigrationKey = "sensitive_word_cache_schema_ready"

// ensureSensitiveWordCacheSchema 建出「按文本哈希缓存检测结论」的表。
//
// 纪律与 ensureSensitiveWordSchema 一致：
//   - 结构性建表失败返回错误，让启动 FATAL 看见；
//   - 用 system_settings 标记保证只跑一次，失败不写标记，下次启动自动重试；
//   - 外层 recover 兜底，脏数据不能让服务反复崩溃；
//   - **不做 DROP TABLE**：缓存是纯加速结构，表有问题就停用（调用方 fail-open
//     直接外呼），删表既无必要也会丢掉已付费买来的结论。
//
// 无回填 ⇒ 无大表扫描，不存在「逐行扫大表」的性能风险。
func ensureSensitiveWordCacheSchema(db *gorm.DB) (err error) {
	if db == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("敏感词缓存表迁移异常: %v", recovered)
		}
	}()
	if systemSettingFlagIsSet(db, sensitiveWordCacheSchemaMigrationKey) {
		return nil
	}
	// ⚠️ 判定与写入用**同一条**语句：HasTable 与 AutoMigrate 走的是不同代码路径，
	// 用「HasTable 为真」推断「AutoMigrate 不会发 DDL」并不可靠。而 AutoMigrate 每发一条
	// ALTER TABLE，MySQL 就会隐式提交一次（DDL 不可回滚），高频插入的缓存表可能撞 1205
	// 死锁；撞了就会写不进去 → 永远读不到缓存 → 反复外呼，正是本功能要消灭的浪费。
	//
	// SQLite 的缓存表是 AutoMigrate 自动建的，标记缺失时**不看标记**（有行就等于建好了）；
	// MySQL 明确要求标记（见 sensitiveWordCacheSchemaReady），这样标记被误删时会重跑一次
	// AutoMigrate 把列补齐，而不是跳过。
	if sensitiveWordCacheSchemaReady(db) {
		return setSystemSettingFlag(db, sensitiveWordCacheSchemaMigrationKey)
	}
	if err := db.AutoMigrate(&entity.SensitiveWordCache{}); err != nil {
		// 不写标记：下次启动重跑，幂等。
		return fmt.Errorf("建敏感词缓存表失败: %w", err)
	}
	return setSystemSettingFlag(db, sensitiveWordCacheSchemaMigrationKey)
}

// sensitiveWordCacheSchemaReady 判断缓存表是否已经建好，决定要不要跑 AutoMigrate。
//
// 两个驱动为什么判据不同：
//   - SQLite：表由 AutoMigrate 自动建，测试库也可能另建。表在、标记丢，跳过即可
//     （SQLite 的 AutoMigrate 对已存在的同构表本就无操作，判断只是为了省一趟反射）。
//   - MySQL：用 system_settings 标记表达「这张表已经是我们建的了」。标记缺失时
//     必须重跑 AutoMigrate 把列补齐（上面那段注释里的 1205 死锁理由）。
func sensitiveWordCacheSchemaReady(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		return db.Migrator().HasTable(entity.TableNameSensitiveWordCache)
	}
	return false
}

// addSensitiveWordColumn 按驱动选择建列语句。
//
// SQLite 走显式 ALTER TABLE（类型写在语句里，避免依赖 GORM 的字段反射）；
// MySQL 走 Migrator().AddColumn，由实体 tag 决定类型与注释。
func addSensitiveWordColumn(db *gorm.DB, field string, sqliteDDL string, mysqlDDL string) error {
	column := columnNameOf(field)
	if strings.EqualFold(db.Dialector.Name(), "sqlite") {
		statement := "ALTER TABLE " + entity.TableNameLog + " ADD COLUMN " + sqliteDDL
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("新增敏感词字段 %s 失败: %w", column, err)
		}
		return nil
	}
	_ = mysqlDDL // MySQL 的列类型/注释由实体 tag 提供，这里只借用字段名。
	if err := db.Migrator().AddColumn(&entity.LogSensitive{}, field); err != nil {
		return fmt.Errorf("新增敏感词字段 %s 失败: %w", column, err)
	}
	return nil
}

// columnNameOf 把 Go 字段名映射成 entity.LogSensitive 声明的列名。
//
// 用显式映射而不是反射：列名是持久化契约，改字段名不该悄悄改列名。
func columnNameOf(field string) string {
	switch field {
	case "SensitiveFlag":
		return "sensitive_flag"
	case "SensitiveWords":
		return "sensitive_words"
	case "SensitiveCheckedAt":
		return "sensitive_checked_at"
	case "SensitiveCheckError":
		return "sensitive_check_error"
	}
	return ""
}
