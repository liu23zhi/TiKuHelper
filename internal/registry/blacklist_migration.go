package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// blacklistSchemaReadyKey 标记「黑名单两张表与索引已就绪」。
	blacklistSchemaReadyKey = "blacklist_schema_ready"
)

// ensureBlacklistSchema 建立黑名单所需的表结构（46-黑名单设计.md §2.3）。
//
// 覆盖：
//   - blacklist_entries（规则主表，唯一索引 (kind,value_hash,match_type)）
//   - blacklist_hits（命中日志，含 hit_count 聚合列）
//
// 纪律（AGENTS.md 第 11 / 17 条）：
//   - 结构性建表/建索引失败 → 返回错误，让启动 FATAL 看见；
//   - 没有数据回填，**不做任何 DROP TABLE**：黑名单是运营数据，删了就没有审计；
//   - 用 system_settings 标记保证只跑一次，失败不写标记，下次启动幂等重跑；
//   - 索引补建不受一次性标记约束（见 applyBlacklistSchema 的注释）；
//   - 幂等：两次运行不报错、不重复建索引。
//
// 与开关解耦：blacklist_enabled 默认 false，表照建。运营打开开关时结构已就位，
// 不会出现「开关打开了但表还没有」的中间态。
func ensureBlacklistSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	// 索引补建放在标记判断**之前**：老库可能已经跑过标记（表建好了），
	// 但后来新增/调整的索引等不到 AutoMigrate。HasIndex 幂等，代价只有一次元数据查询。
	if db.Migrator().HasTable(entity.TableNameBlacklistEntry) {
		if err := ensureBlacklistEntryIndexes(db); err != nil {
			return err
		}
	}
	if db.Migrator().HasTable(entity.TableNameBlacklistHit) {
		if err := ensureBlacklistHitIndexes(db); err != nil {
			return err
		}
	}
	if systemSettingFlagIsSet(db, blacklistSchemaReadyKey) {
		return nil
	}
	applied, err := safeEnsureBlacklistSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		// 极端空库首次启动（连 system_settings 都没有）：什么都不建，也绝不写标记，
		// 否则 system_settings 建出来之后本迁移会被永远跳过。
		return nil
	}
	if err := setSystemSettingFlag(db, blacklistSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入黑名单迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureBlacklistSchema 在外层兜一层 recover：脏数据不能让服务反复崩溃。
//
// 返回值 applied 表示「本次真的建了结构」；只有 applied 为 true 才应写标记。
func safeEnsureBlacklistSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyBlacklistSchema(db)
}

// applyBlacklistSchema 建表 + 建索引。SQLite 与 MySQL 两套分支都要能跑。
//
// 为什么不用纯 AutoMigrate 收工：GORM 的 AutoMigrate 在 SQLite 上对
// 复合唯一索引的创建行为随版本变化（历史上有过只在表新建时建索引、老表不补的情况），
// 而这条唯一索引是「同一模式不重复拉黑」的唯一保障。因此这里显式建一次：
// 两种方言各自拼本方言合法的 DDL，且都用 HasIndex 前置判断保持幂等。
func applyBlacklistSchema(db *gorm.DB) (bool, error) {
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		return false, fmt.Errorf("create blacklist_entries fail: %w", err)
	}
	if err := db.AutoMigrate(&entity.BlacklistHit{}); err != nil {
		return false, fmt.Errorf("create blacklist_hits fail: %w", err)
	}
	if err := ensureBlacklistEntryIndexes(db); err != nil {
		return false, err
	}
	if err := ensureBlacklistHitIndexes(db); err != nil {
		return false, err
	}
	return true, nil
}

// ensureBlacklistEntryIndexes 保证规则表的唯一索引与筛选索引存在。
//
// 唯一索引 (kind, value_hash, match_type)：同一模式不重复拉黑。
// 索引 (kind, enabled)：管理页/缓存加载按「类型 + 是否启用」筛。
func ensureBlacklistEntryIndexes(db *gorm.DB) error {
	indexes := []blacklistIndexSpec{
		{
			name:    "uk_blacklist_entry_pattern",
			table:   entity.TableNameBlacklistEntry,
			columns: []string{"kind", "value_hash", "match_type"},
			unique:  true,
		},
		{
			name:    "idx_blacklist_entry_kind_enabled",
			table:   entity.TableNameBlacklistEntry,
			columns: []string{"kind", "enabled"},
		},
	}
	return ensureBlacklistIndexes(db, indexes)
}

// ensureBlacklistHitIndexes 保证命中日志的查询索引存在。
//
// idx_blacklist_hit_entry (entry_id, created_at)：按规则回看命中（聚合窗口查询走它）。
// idx_blacklist_hit_created (created_at)：清理任务按时间删（BL-6 接入）。
func ensureBlacklistHitIndexes(db *gorm.DB) error {
	indexes := []blacklistIndexSpec{
		{
			name:    "idx_blacklist_hit_entry",
			table:   entity.TableNameBlacklistHit,
			columns: []string{"entry_id", "created_at"},
		},
		{
			name:    "idx_blacklist_hit_created",
			table:   entity.TableNameBlacklistHit,
			columns: []string{"created_at"},
		},
	}
	return ensureBlacklistIndexes(db, indexes)
}

type blacklistIndexSpec struct {
	name    string
	table   string
	columns []string
	unique  bool
}

// ensureBlacklistIndexes 逐个补建缺失的索引，SQLite / MySQL 分别出 DDL。
//
// 幂等策略分方言：SQLite 支持 CREATE INDEX IF NOT EXISTS，可以直接靠 DDL 幂等；
// MySQL **不支持** IF NOT EXISTS（会报 1064），必须先用 information_schema 判断，
// 这也是这里不能只写一句 sqlite 风格 DDL 的原因。
func ensureBlacklistIndexes(db *gorm.DB, specs []blacklistIndexSpec) error {
	if db == nil {
		return nil
	}
	dialect := db.Dialector.Name()
	for _, spec := range specs {
		if db.Migrator().HasIndex(spec.table, spec.name) {
			continue
		}
		statement := blacklistIndexDDL(dialect, spec)
		if statement == "" {
			return fmt.Errorf("不支持的数据库方言 %q：无法为 %s 建索引 %s", dialect, spec.table, spec.name)
		}
		if err := db.Exec(statement).Error; err != nil {
			// 并发启动的两个实例可能同时判定「索引不存在」，其中一个会撞 already exists。
			// 这种情况下再核一次元数据：索引确实在，就算成功（不掩盖其它错误）。
			if db.Migrator().HasIndex(spec.table, spec.name) {
				continue
			}
			return fmt.Errorf("create index %s on %s fail: %w", spec.name, spec.table, err)
		}
		logger.SysLog(fmt.Sprintf("已为 %s 补上索引 %s", spec.table, spec.name))
	}
	return nil
}

// blacklistIndexDDL 按方言生成建索引语句；未知方言返回空串（由调用方报错）。
//
// 抽成纯函数是为了能直接断言两套分支的语句，不必真的连一个 MySQL 服务器
// （与 internal/registry/billing_record_indexes_mysql_test.go 的思路一致）。
//
// 标识符统一用反引号引用：SQLite 与 MySQL 都接受反引号（与
// ensureBillingRecordIndexes 的既有写法一致），因此两套分支的差异只在
// "IF NOT EXISTS" 这一句上 —— MySQL 的 CREATE INDEX 不支持它。
func blacklistIndexDDL(dialect string, spec blacklistIndexSpec) string {
	kind := "INDEX"
	if spec.unique {
		kind = "UNIQUE INDEX"
	}
	quotedColumns := make([]string, len(spec.columns))
	for i, column := range spec.columns {
		quotedColumns[i] = quoteBillingIdentifier(column)
	}
	columns := strings.Join(quotedColumns, ", ")
	name := quoteBillingIdentifier(spec.name)
	table := quoteBillingIdentifier(spec.table)

	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "sqlite":
		return fmt.Sprintf("CREATE %s IF NOT EXISTS %s ON %s (%s)", kind, name, table, columns)
	case "mysql":
		// MySQL 没有 IF NOT EXISTS；幂等由 ensureBlacklistIndexes 的 HasIndex 前置判断保证。
		return fmt.Sprintf("CREATE %s %s ON %s (%s)", kind, name, table, columns)
	default:
		return ""
	}
}
