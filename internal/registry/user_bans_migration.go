package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// userBansSchemaReadyKey 标记「user_bans 表与索引已就绪」。
	//
	// 键名带 _v1 与 risk_rules_schema_v1 / risk_warnings_schema_v1 同款：
	// 将来若需要重建结构（例如加唯一约束），换 _v2 键即可让迁移重跑一次，
	// 而不必去删老键。
	userBansSchemaReadyKey = "user_bans_schema_v1"
)

// EnsureUserBansSchema 建立账号封禁记录表（user_bans）与索引。
//
// 依据：
//   - task_doc/121 §4.8 + 121 决策 6（封禁判定双读：users.status 与 user_bans 任一命中）
//   - task_doc/97 §0（本期建表清单）
//   - task_doc/46-黑名单设计.md §5（封禁语义与「到期不删行」口径）
//
// 覆盖：
//   - user_bans（封禁记录主表）
//   - idx_user_ban_user_expires (user_id, expires_at)：判定「某用户是否有未过期封禁」的专用路径
//   - idx_user_ban_banned_at (banned_at)：管理页按封禁时间倒序 + 将来按时间清理
//
// 纪律（照 internal/registry/blacklist_migration.go 的四条，逐条对应）：
//
//	a) `db == nil` 直接返回 nil —— 本函数是导出函数，测试/工具可能以 nil 调用，
//	   不能让「数据库还没就绪」变成空指针崩溃。
//	b) **索引补建放在标记判断之前**：老库可能已经跑过标记（表建好了），
//	   但后来新增/调整的索引等不到 AutoMigrate。HasIndex 幂等，代价只有一次元数据查询。
//	c) **建表失败不写标记**（121 §2.1）：先写标记再建表、或建表失败也写标记，
//	   那张表就永远不会被创建，而且没有任何报错（标记说"已完成"）。
//	   本函数把错误原样返回，由 db.go 的 runStartupMigration 外层 FATAL 看见。
//	d) **绝不 DROP TABLE / DROP COLUMN**：封禁记录是审计数据 ——
//	   「谁在什么时候因为什么封了谁」删了就无法复盘，也无法向用户解释。
//
// 无数据回填：users.status='banned' 是**既有状态**，不回填成 user_bans 行。
// 理由是回填无法凭空造出 reason / banned_by / banned_at 三个必填语义
// （强制填 '存量数据' 会让审计表里出现一批假记录，比缺记录更糟）。
// 存量封禁靠双读判定继续生效 —— 这正是 121 决策 6 要求双读的原因。
//
// 与开关解耦：没有「封禁总开关」，表照建。运营第一次封禁时结构已就位。
func EnsureUserBansSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	// (b) 索引补建放在标记判断之前。
	if db.Migrator().HasTable(entity.TableNameUserBan) {
		if err := ensureUserBanIndexes(db); err != nil {
			return err
		}
	}
	if systemSettingFlagIsSet(db, userBansSchemaReadyKey) {
		return nil
	}
	applied, err := safeEnsureUserBansSchema(db)
	if err != nil {
		// (c) 结构性失败：不写标记，下次启动重跑（幂等），错误交给外层 FATAL。
		return err
	}
	if !applied {
		// 极端空库首次启动（连 system_settings 都没有）：什么都不建，也绝不写标记，
		// 否则 system_settings 建出来之后本迁移会被永远跳过。
		return nil
	}
	if err := setSystemSettingFlag(db, userBansSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：结构已经建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入封禁记录迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureUserBansSchema 在外层兜一层 recover：脏 schema 不能让服务反复崩溃。
//
// 返回值 applied 表示「本次真的建了结构」；只有 applied 为 true 才应写标记。
func safeEnsureUserBansSchema(db *gorm.DB) (applied bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			applied, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyUserBansSchema(db)
}

// applyUserBansSchema 建表 + 建索引。SQLite 与 MySQL 两套分支都要能跑。
//
// 为什么建表用 AutoMigrate、建索引却显式出 DDL（与 blacklist 完全同款理由）：
// 这两条索引的列集是**判定路径的形状**（user_id + expires_at 必须同在一个索引里），
// 而 GORM 的 AutoMigrate 对「老表补建复合索引」的行为随版本变化过；
// 显式一次 + HasIndex 前置判断可以保证老库一定补得上。
func applyUserBansSchema(db *gorm.DB) (bool, error) {
	// 先确认 system_settings 存在：不存在就没有标记的落点，建表也白建（会被永远跳过）。
	if !db.Migrator().HasTable(entity.TableNameSystemSetting) {
		return false, nil
	}
	// 记录建表前是否已存在：AutoMigrate 对已存在的表也返回 nil，
	// 因此不能靠它的返回值判断「这次真的建了表」。
	existedBefore := db.Migrator().HasTable(entity.TableNameUserBan)
	if err := db.AutoMigrate(&entity.UserBan{}); err != nil {
		// 并发启动的两个实例可能同时判定「表不存在」，其中一个会撞 already exists。
		// 与 ensureUserBanIndexSpecs 的错误分支同一手法：再核一次元数据，
		// **表确实在**就算成功，否则照原样返回错误。
		//
		// ⚠️ 这不违反「建表失败不写标记」：真正的建表失败（权限、磁盘、语法）
		// 之后 HasTable 一定是 false，错误照样上报、标记照样不写。
		// 只有「别人刚好建好了」这一种情况会被吸收。
		if !db.Migrator().HasTable(entity.TableNameUserBan) {
			return false, fmt.Errorf("create user_bans fail: %w", err)
		}
		logger.SysLog(fmt.Sprintf("%s 已由并发启动的实例建好，跳过建表", entity.TableNameUserBan))
	} else if !existedBefore {
		// 「已建表」这条日志是**行为级判据的锚点**：验收要求「二次启动不重跑，
		// 日志里没有第二次『已建表』」。因此它必须只在**本次真的建了表**时打，
		// 且文案固定 —— 改文案会让判据失效，需要同步改测试。
		logger.SysLog(fmt.Sprintf("已建表 %s（账号封禁记录）", entity.TableNameUserBan))
	}
	if err := ensureUserBanIndexes(db); err != nil {
		return false, err
	}
	return true, nil
}

// ensureUserBanIndexes 保证封禁记录的两条查询索引存在。
//
//	idx_user_ban_user_expires (user_id, expires_at)
//	    判定路径专用：`WHERE user_id = ? AND (expires_at IS NULL OR expires_at > ?)`。
//	    缺它时每次准入判定都全表扫 —— 而准入判定在每一条受保护请求上都会跑，
//	    这是最容易变成压测瓶颈的地方。
//	idx_user_ban_banned_at (banned_at)
//	    管理页按封禁时间倒序翻页；以及将来按保留期清理（按时间删）走它。
func ensureUserBanIndexes(db *gorm.DB) error {
	return ensureUserBanIndexSpecs(db, []userBanIndexSpec{
		{
			name:    "idx_user_ban_user_expires",
			table:   entity.TableNameUserBan,
			columns: []string{"user_id", "expires_at"},
		},
		{
			name:    "idx_user_ban_banned_at",
			table:   entity.TableNameUserBan,
			columns: []string{"banned_at"},
		},
	})
}

// userBanIndexSpec 是一条待补建的索引定义。
//
// 与 blacklistIndexSpec / riskIndexSpec 结构相同但独立声明，不跨文件复用：
// 三张表的索引集将来会各自演化（黑名单还要加 kind 维度、风控还要加 subject），
// 共用一个类型会让「改一处影响三张表」，而这类迁移的失败模式是静默的。
type userBanIndexSpec struct {
	name    string
	table   string
	columns []string
	unique  bool
}

// ensureUserBanIndexSpecs 逐个补建缺失的索引，SQLite / MySQL 分别出 DDL。
//
// 幂等策略分方言：SQLite 支持 CREATE INDEX IF NOT EXISTS，可以直接靠 DDL 幂等；
// MySQL **不支持** IF NOT EXISTS（会报 1064），必须先用 HasIndex 判断 ——
// 这也是这里不能只写一句 sqlite 风格 DDL 的原因。
func ensureUserBanIndexSpecs(db *gorm.DB, specs []userBanIndexSpec) error {
	if db == nil {
		return nil
	}
	dialect := db.Dialector.Name()
	for _, spec := range specs {
		if db.Migrator().HasIndex(spec.table, spec.name) {
			continue
		}
		statement := userBanIndexDDL(dialect, spec)
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

// userBanIndexDDL 按方言生成建索引语句；未知方言返回空串（由调用方报错）。
//
// 抽成纯函数是为了能直接断言两套分支的语句，不必真的连一个 MySQL 服务器
// （与 blacklistIndexDDL / riskIndexDDL 的思路一致）。
//
// 标识符统一用反引号引用：SQLite 与 MySQL 都接受反引号（复用同包的
// quoteBillingIdentifier），因此两套分支的差异只在 "IF NOT EXISTS" 这一句上。
func userBanIndexDDL(dialect string, spec userBanIndexSpec) string {
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
		// MySQL 没有 IF NOT EXISTS；幂等由 ensureUserBanIndexSpecs 的 HasIndex 前置判断保证。
		return fmt.Sprintf("CREATE %s %s ON %s (%s)", kind, name, table, columns)
	default:
		return ""
	}
}
