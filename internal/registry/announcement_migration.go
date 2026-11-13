package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// announcementsSchemaV1Key 标记「公告表与索引已就绪」（121 §4.5 指定的键名）。
	announcementsSchemaV1Key = "announcements_schema_v1"
)

// EnsureAnnouncementSchema 建立公告系统所需的表结构（N4，112 §4.3 / 113 / 121 §4.5）。
//
// 覆盖：announcements（公告主表）。
//
// 纪律（121 §2 的 8 条规范，逐条对齐）：
//  1. 函数名 ensure 前缀 + Schema 后缀 → 本函数是导出形式（captain 在 db.go 里接线，
//     导出名按任务书约定为 EnsureAnnouncementSchema），内部实现仍然遵守本仓约定；
//  2. 开头 db == nil 直接返回（28/29 个迁移都这么写）；
//  3. 纯新表，无前置表依赖（121 §4.5「前置表：无」）；
//  4. **结构性失败返回 error**，由外层决定 FATAL/SysLog（见下面「失败策略」）；
//  5. 无数据回填（公告从上线起算，121 §4.5「数据回填：无」）；
//  6. 标记写失败不算迁移失败：结构已建好，下次启动重跑是幂等的；
//  7. 外层 recover 兜 panic；
//  8. **绝不 DROP TABLE**：公告是运营内容，删了就没有发布记录。
//
// FLAG 型（标记键 announcements_schema_v1），不是列检测型：
// 121 决策 1 的判据是「有没有数据回填」—— 本表无回填，按该判据本可走列检测型；
// 但任务书与 121 §4.5 都明确指定 FLAG 型 + 该标记键，故照办。两者都不出错，
// 选 FLAG 的额外代价只是首次多一次 settings 读写。
//
// ⚠️ 失败策略与本仓默认不同（121 §4.5 说 announcements 可以 fail-open、用 SysLog）：
// 本函数**照样返回 error**，由调用方（db.go）决定要不要 FATAL。理由是
// 「结构缺失」与「查询不到公告」的处置必须分离 —— 建表失败时若静默继续，
// 公告功能 100% 失效且日志里只有一行；而运行期读公告失败才是真的可以 fail-open
// （见 service.ActiveIncidents / ActiveIncidentsOrEmpty）。
//
// 幂等：两次运行不报错、不重复建索引、不重复建表。
func EnsureAnnouncementSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	// 索引补建放在标记判断**之前**（121 §2.2，最容易漏的一条）：
	// 老库可能已经跑过标记（表建好了），但后来新增/调整的索引等不到 AutoMigrate。
	// 若只把索引放在 FLAG 判断之后，已经写过标记的库永远不会补新索引，
	// 而索引缺失的表现只是「查询慢」，不报错、几乎不可发现。
	// HasIndex 幂等，代价只有一次元数据查询。
	if db.Migrator().HasTable(entity.TableNameAnnouncement) {
		if err := ensureAnnouncementIndexes(db); err != nil {
			return err
		}
	}
	if systemSettingFlagIsSet(db, announcementsSchemaV1Key) {
		return nil
	}
	applied, err := safeEnsureAnnouncementSchema(db)
	if err != nil {
		return err
	}
	if !applied {
		// 走到这里只有一种情况：建表被 panic 打断（safeEnsureAnnouncementSchema 兜住了）。
		// 结构没建成就绝不写标记 —— 否则这张表将永远建不出来且没有任何报错
		// （121 §2.1 的反直觉细节：标记说「已完成」，迁移就再也不会重试）。
		return nil
	}
	if err := setSystemSettingFlag(db, announcementsSchemaV1Key); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入公告迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureAnnouncementSchema 在外层兜一层 recover：脏元数据不能让服务反复崩溃。
//
// 返回值 applied 表示「本次真的建了结构」；只有 applied 为 true 才应写标记。
func safeEnsureAnnouncementSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyAnnouncementSchema(db)
}

// applyAnnouncementSchema 建表 + 建索引。SQLite 与 MySQL 两套分支都要能跑。
func applyAnnouncementSchema(db *gorm.DB) (bool, error) {
	if err := db.AutoMigrate(&entity.Announcement{}); err != nil {
		return false, fmt.Errorf("create announcements fail: %w", err)
	}
	if err := ensureAnnouncementIndexes(db); err != nil {
		return false, err
	}
	return true, nil
}

// announcementIndexSpec 是一条要补建的索引。字段与 blacklistIndexSpec 同构，
// 但不复用后者：那个类型与其构造函数在名字上绑定了黑名单，跨表借用会让人
// 以为公告索引与黑名单共享生命周期。（真正的收敛做法是把
// 「spec + 分方言 DDL」抽成不带业务名的公共 helper，但那要改
// blacklist_migration.go，属本任务禁改的既有文件，故留作后续。）
type announcementIndexSpec struct {
	name    string
	table   string
	columns []string
	unique  bool
}

// ensureAnnouncementIndexes 保证公告表的两条查询索引存在。
//
//	idx_announcement_kind_enabled (kind, enabled)  —— 管理页按「类型 + 是否启用」筛
//	idx_announcement_window (enabled, starts_at, ends_at) —— 搜题响应按时间窗取生效公告
//
// 第二条是热路径：搜题每次都要查「当前生效的 incident」，没有它就会全表扫。
func ensureAnnouncementIndexes(db *gorm.DB) error {
	specs := []announcementIndexSpec{
		{
			name:    "idx_announcement_kind_enabled",
			table:   entity.TableNameAnnouncement,
			columns: []string{"kind", "enabled"},
		},
		{
			name:    "idx_announcement_window",
			table:   entity.TableNameAnnouncement,
			columns: []string{"enabled", "starts_at", "ends_at"},
		},
	}
	if db == nil {
		return nil
	}
	dialect := db.Dialector.Name()
	for _, spec := range specs {
		if db.Migrator().HasIndex(spec.table, spec.name) {
			continue
		}
		statement := announcementIndexDDL(dialect, spec)
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

// announcementIndexDDL 按方言生成建索引语句；未知方言返回空串（由调用方报错）。
//
// 抽成纯函数是为了能直接断言两套分支的语句，不必真的连一个 MySQL 服务器
// （与 internal/registry/billing_record_indexes_mysql_test.go 的思路一致）。
//
// 差异只在 "IF NOT EXISTS" 一句：SQLite 支持，MySQL 的 CREATE INDEX 不支持
// （幂等由调用方的 HasIndex 前置判断保证）。标识符统一用反引号，两方言都接受。
func announcementIndexDDL(dialect string, spec announcementIndexSpec) string {
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
		return fmt.Sprintf("CREATE %s %s ON %s (%s)", kind, name, table, columns)
	default:
		return ""
	}
}
