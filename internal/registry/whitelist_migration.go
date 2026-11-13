package registry

import (
	"fmt"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// whitelistSchemaReadyKey 标记「白名单表与索引已就绪」。
	whitelistSchemaReadyKey = "whitelist_schema_v1"
)

// EnsureWhitelistSchema 建立白名单所需的表结构（task_doc/123，规则模仿黑名单）。
//
// 覆盖 whitelist_entries 一张表 + 两级索引（唯一 (kind,value_hash,match_type)、
// 筛选 (kind,enabled)）。
//
// 导出（首字母大写）是为了让 cmd 侧的装配与测试都能直接驱动；四个纪律与
// ensureBlacklistSchema 逐条对齐：
//
//   - 结构性建表/建索引失败 → 返回错误，让启动 FATAL 看见；
//   - 没有数据回填，**不做任何 DROP TABLE**：白名单同样属运营数据，删了就没有审计；
//   - 用 system_settings 标记（whitelist_schema_v1）保证只跑一次，失败不写标记，
//     下次启动幂等重跑；
//   - 索引补建不受一次性标记约束（老库可能已跑过标记，但后来新增的索引等不到
//     AutoMigrate）；幂等：两次运行不报错、不重复建索引。
//
// 与黑名单不同，白名单**没有总开关**（见 entity.WhitelistEntry 的注释），所以这里
// 不存在「开关关着但表要照建」的解耦问题：表建好即随时可用。
func EnsureWhitelistSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	// 索引补建放在标记判断**之前**（与 ensureBlacklistSchema 同序）：HasIndex 幂等，
	// 代价只有一次元数据查询。
	if db.Migrator().HasTable(entity.TableNameWhitelistEntry) {
		if err := ensureWhitelistEntryIndexes(db); err != nil {
			return err
		}
	}
	if systemSettingFlagIsSet(db, whitelistSchemaReadyKey) {
		return nil
	}
	applied, err := safeEnsureWhitelistSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		// 极端空库首次启动（连 system_settings 都没有）：什么都不建，也绝不写标记，
		// 否则 system_settings 建出来之后本迁移会被永远跳过。
		return nil
	}
	if err := setSystemSettingFlag(db, whitelistSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入白名单迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureWhitelistSchema 在外层兜一层 recover：脏数据不能让服务反复崩溃。
//
// 返回值 applied 表示「本次真的建了结构」；只有 applied 为 true 才应写标记。
func safeEnsureWhitelistSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyWhitelistSchema(db)
}

// applyWhitelistSchema 建表 + 建索引。SQLite 与 MySQL 两套分支都要能跑。
//
// 为什么不用纯 AutoMigrate 收工：与黑名单同因 —— GORM 的 AutoMigrate 在 SQLite 上
// 对复合唯一索引的创建行为随版本变化（历史上出现过只在表新建时建索引、老表不补），
// 而这条唯一索引是「同一模式不重复放行」的唯一保障。因此显式补建一次，
// 复用 blacklist_migration.go 里已有的 ensureBlacklistIndexes（方言分支已实现、
// 已幂等），不复制第二份「按方言出 DDL」的逻辑。
func applyWhitelistSchema(db *gorm.DB) (bool, error) {
	if err := db.AutoMigrate(&entity.WhitelistEntry{}); err != nil {
		return false, fmt.Errorf("create whitelist_entries fail: %w", err)
	}
	if err := ensureWhitelistEntryIndexes(db); err != nil {
		return false, err
	}
	return true, nil
}

// ensureWhitelistEntryIndexes 保证白名单表的唯一索引与筛选索引存在。
//
// 唯一索引 (kind, value_hash, match_type)：同一模式不重复加白名单。
// 索引 (kind, enabled)：缓存加载与管理页按「类型 + 是否启用」筛。
func ensureWhitelistEntryIndexes(db *gorm.DB) error {
	indexes := []blacklistIndexSpec{
		{
			name:    "uk_whitelist_entry_pattern",
			table:   entity.TableNameWhitelistEntry,
			columns: []string{"kind", "value_hash", "match_type"},
			unique:  true,
		},
		{
			name:    "idx_whitelist_entry_kind_enabled",
			table:   entity.TableNameWhitelistEntry,
			columns: []string{"kind", "enabled"},
		},
	}
	return ensureBlacklistIndexes(db, indexes)
}
