package registry

import (
	"fmt"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

const (
	// riskRulesSchemaReadyKey 标记「risk_rules 表与种子已就绪」。
	riskRulesSchemaReadyKey = "risk_rules_schema_v1"
	// riskWarningsSchemaReadyKey 标记「risk_warnings 表与索引已就绪」。
	riskWarningsSchemaReadyKey = "risk_warnings_schema_v1"
)

// EnsureRiskSchema 建立风控告警所需的两张表 + 索引 + 3 条欠款规则种子。
//
// 依据：
//   - 96-风控管理仅告警与用户画像-设计.md（C1 只告警不拦截 / C2 独立 webhook）
//   - 118-决策记录第十二轮-欠款与风控告警.md §2.4（3 条欠款规则）
//   - 119-风控系统溯源报告.md §4.2（命名以 96 号为准）
//   - 121-决策记录第十四轮-数据库迁移体系.md §2 + §4.6 + §4.7（迁移纪律）
//
// 三件事、三处**不同的**失败语义（照 121 §4.7 与 user_level_migration.go:59 的分离写法）：
//
//  1. 建表失败 → 返回 error（风控是安全相关，结构性故障必须让外层 FATAL 看见）；
//  2. 索引补建失败 → 返回 error（与黑名单一致：索引缺失的表现是「查询慢」，不会报错）；
//  3. 种子写入失败 → **只 SysLog**（表已建好，管理员可在后台补；不能让启动失败）。
//
// ⚠️ 四条最容易写错的纪律：
//
//	a) **建表失败绝不写标记**（121 §2.1）。先写标记再建表、或建表失败也写标记，
//	   那张表就**永远不会被创建**，而且没有任何报错（标记说"已完成"）。
//	b) **索引补建放在标记判断之前**（121 §2.2）。老库可能已经跑过标记（表建好了），
//	   但后来新增的索引等不到 AutoMigrate；只在标记之后补索引，老库永远补不上。
//	c) 种子判定用 COUNT 而不是 First（与 seedDefaultUserLevels 一致）。
//	d) **绝不 DROP TABLE / DROP COLUMN**：告警记录是审计数据，删了无法复盘。
//
// 与开关解耦：风控 webhook 未配置时表照建。运营配好 webhook 时结构已就位，
// 不会出现「开关打开了但表还没有」的中间态。
func EnsureRiskSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	// (b) 索引补建放在两个标记判断之前。
	if db.Migrator().HasTable(entity.TableNameRiskRule) {
		if err := ensureRiskRuleIndexes(db); err != nil {
			return err
		}
	}
	if db.Migrator().HasTable(entity.TableNameRiskWarning) {
		if err := ensureRiskWarningIndexes(db); err != nil {
			return err
		}
	}

	if err := ensureRiskRulesSchema(db); err != nil {
		return err
	}
	if err := ensureRiskWarningsSchema(db); err != nil {
		return err
	}
	return nil
}

// ensureRiskRulesSchema 建 risk_rules 并写种子。
//
// 建表失败返回 error 且不写标记；种子失败只 SysLog。
func ensureRiskRulesSchema(db *gorm.DB) error {
	if systemSettingFlagIsSet(db, riskRulesSchemaReadyKey) {
		return nil
	}
	created, err := safeEnsureRiskRulesSchema(db)
	if err != nil {
		// 结构性失败：不写标记，下次启动重跑（幂等），并把错误交给外层。
		return err
	}
	if !created {
		// 极端空库首次启动（连 system_settings 都没有）：什么都不建，也绝不写标记，
		// 否则 system_settings 建出来之后本迁移会被永远跳过（121 §2.1）。
		return nil
	}
	// 种子在标记之前写入：写失败只记日志，标记照写（表已建好，管理员可后台补）。
	// 反过来「种子失败就不写标记」会让每次启动都重跑建表，而 AutoMigrate 幂等，
	// 代价只是一次元数据查询 —— 两种都可行；这里选前者，与 user_level_migration.go
	// 的 seedDefaultUserLevels(db) 位置一致。
	seedDefaultRiskRules(db)
	if err := setSystemSettingFlag(db, riskRulesSchemaReadyKey); err != nil {
		// 标记写失败不算迁移失败：结构已建好，下次启动重跑是幂等的。
		logger.SysLog(fmt.Sprintf("写入风控规则迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// ensureRiskWarningsSchema 建 risk_warnings。
func ensureRiskWarningsSchema(db *gorm.DB) error {
	if systemSettingFlagIsSet(db, riskWarningsSchemaReadyKey) {
		return nil
	}
	created, err := safeEnsureRiskWarningsSchema(db)
	if err != nil {
		return err
	}
	if !created {
		return nil
	}
	if err := setSystemSettingFlag(db, riskWarningsSchemaReadyKey); err != nil {
		logger.SysLog(fmt.Sprintf("写入风控告警迁移标记失败（下次启动会重跑，幂等）：%v", err))
	}
	return nil
}

// safeEnsureRiskRulesSchema 兜一层 recover：脏数据不能让服务反复崩溃。
//
// 返回值 created 表示「本次真的建了表」；只有 created 为 true 才应写标记。
func safeEnsureRiskRulesSchema(db *gorm.DB) (created bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			created, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyRiskRulesSchema(db)
}

func applyRiskRulesSchema(db *gorm.DB) (bool, error) {
	// 先确认 system_settings 存在：不存在就没有标记的落点，建表也白建（会被永远跳过）。
	if !db.Migrator().HasTable(entity.TableNameSystemSetting) {
		return false, nil
	}
	if err := db.AutoMigrate(&entity.RiskRule{}); err != nil {
		return false, fmt.Errorf("create risk_rules fail: %w", err)
	}
	if err := ensureRiskRuleIndexes(db); err != nil {
		return false, err
	}
	return true, nil
}

func safeEnsureRiskWarningsSchema(db *gorm.DB) (created bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			created, err = false, fmt.Errorf("panic: %v", recovered)
		}
	}()
	return applyRiskWarningsSchema(db)
}

func applyRiskWarningsSchema(db *gorm.DB) (bool, error) {
	if !db.Migrator().HasTable(entity.TableNameSystemSetting) {
		return false, nil
	}
	if err := db.AutoMigrate(&entity.RiskWarning{}); err != nil {
		return false, fmt.Errorf("create risk_warnings fail: %w", err)
	}
	if err := ensureRiskWarningIndexes(db); err != nil {
		return false, err
	}
	return true, nil
}

// seedDefaultRiskRules 只在空表时写入全部代码默认规则（欠款 3 条 + 行为规则）。
//
// 幂等判定用 COUNT：它准确回答「有没有人配过」这个原始问题，
// 而 First 会因任何残留行改变行为。失败只记日志 —— 种子不是结构性依赖。
//
// ⚠️ 阈值在这里只写**代码默认值**。
// 96 §2.2 要求「默认值写在代码里，不在 DB 里」：DB 缺 key ≠ 阈值 0。
// 服务层读阈值时以系统设置优先、回落 RiskRule 行、再回落代码默认值，
// 三层兜底保证「阈值被人删空」不会静默退化成 0（否则每条记录都会告警）。
//
// ⚠️ 遍历 entity.DefaultRiskRules()（欠款 + 行为）而不是只遍历欠款那一个切片：
// 漏掉一条 = 那条规则在 risk_rules 里没有行，运营在后台看不到也改不了它
// （判定仍会按代码默认值跑，但「看不到却生效」是最难排查的形态）。
// seedDefaultRiskRules 把**缺失的**代码默认规则补进 risk_rules。
//
// ⚠️ 2026-11-14 修正：原实现是 `if count > 0 { return }` —— 只要表里有任意一行就整段跳过。
// 后果很严重且**完全静默**：
//
//	老实例（哪怕只有 3 条 debt_* 规则）永远拿不到后来新增的行为规则。
//	而判定侧是 `if rule, ok := enabledRules[RuleID]; ok` —— 缺行 ⇒ ok=false ⇒ 规则直接跳过，
//	不报错、不告警、日志里什么都没有。14 条行为规则写成「已落地」，实际一条都不生效。
//
// 实测（开发库）：代码定义 17 条，库里只有 3 条 debt_*，缺失 14 条全部是行为规则。
//
// 正确策略是**按 rule_id 增量补齐**：
//   - 已存在的行**不动**（管理员可能调过 enabled / threshold，覆盖等于抹掉他的配置）
//   - 只插入缺的（新规则上线时自动补齐）
//   - 幂等：重复启动不会重复插入（rule_id 有唯一索引 + 先查后插在同一次遍历内）
func seedDefaultRiskRules(db *gorm.DB) {
	existing := make(map[string]struct{}, 32)
	var ruleIDs []string
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RiskRule{}).
		Pluck("rule_id", &ruleIDs).Error; err != nil {
		logger.SysLog(fmt.Sprintf("读取风控规则 rule_id 失败（跳过种子数据）：%v", err))
		return
	}
	for _, id := range ruleIDs {
		existing[id] = struct{}{}
	}

	rows := make([]entity.RiskRule, 0, len(entity.DefaultRiskRules()))
	for _, item := range entity.DefaultRiskRules() {
		if _, ok := existing[item.RuleID]; ok {
			// 已存在：**不改**。管理员可能已经调过 enabled/threshold。
			continue
		}
		rows = append(rows, entity.RiskRule{
			RuleID:          item.RuleID,
			Name:            item.Name,
			Severity:        item.Severity,
			Enabled:         true,
			Threshold:       item.Threshold,
			ThrottleSeconds: item.ThrottleSeconds,
			Description:     item.Description,
		})
	}
	if len(rows) == 0 {
		return
	}
	if err := db.Create(&rows).Error; err != nil {
		logger.SysLog(fmt.Sprintf("写入风控规则种子失败（管理员可在后台补）：%v", err))
		return
	}
	logger.SysLog(fmt.Sprintf("已补齐 %d 条缺失的风控规则种子（共 %d 条代码默认规则）",
		len(rows), len(entity.DefaultRiskRules())))
}

// ensureRiskRuleIndexes 保证 risk_rules 的 rule_id 唯一索引存在。
//
// 唯一索引是「一条规则只有一行」的唯一保障：阈值读取按 rule_id 落地，
// 出现两行同 rule_id 会让判定取到随机一行（GORM 无 ORDER BY 时结果不确定）。
func ensureRiskRuleIndexes(db *gorm.DB) error {
	return ensureRiskIndexes(db, []riskIndexSpec{
		{
			name:    "uk_risk_rule_rule_id",
			table:   entity.TableNameRiskRule,
			columns: []string{"rule_id"},
			unique:  true,
		},
	})
}

// ensureRiskWarningIndexes 保证告警记录的三个查询索引存在。
//

// - idx_risk_warning_rule_created (rule_id, created_at)：按规则回看告警（列表页默认视图）
// - idx_risk_warning_subject (rule_id, subject_key, created_at)：窗口节流的查询路径
// - idx_risk_warning_created (created_at)：清理任务/时间范围筛选
func ensureRiskWarningIndexes(db *gorm.DB) error {
	return ensureRiskIndexes(db, []riskIndexSpec{

		{
			name:    "idx_risk_warning_rule_created",
			table:   entity.TableNameRiskWarning,
			columns: []string{"rule_id", "created_at"},
		},
		{
			name:    "idx_risk_warning_subject",
			table:   entity.TableNameRiskWarning,
			columns: []string{"rule_id", "subject_key", "created_at"},
		},
		{
			name:    "idx_risk_warning_created",
			table:   entity.TableNameRiskWarning,
			columns: []string{"created_at"},
		},
	})
}

type riskIndexSpec struct {
	name    string
	table   string
	columns []string
	unique  bool
}

// ensureRiskIndexes 逐个补建缺失的索引，SQLite / MySQL 分别出 DDL。
//
// 幂等策略分方言：SQLite 支持 CREATE INDEX IF NOT EXISTS，可以直接靠 DDL 幂等；
// MySQL **不支持** IF NOT EXISTS（会报 1064），必须先用 HasIndex 判断 ——
// 这也是这里不能只写一句 sqlite 风格 DDL 的原因（与 ensureBlacklistIndexes 同构）。
func ensureRiskIndexes(db *gorm.DB, specs []riskIndexSpec) error {
	if db == nil {
		return nil
	}
	dialect := db.Dialector.Name()
	for _, spec := range specs {
		if db.Migrator().HasIndex(spec.table, spec.name) {
			continue
		}
		statement := riskIndexDDL(dialect, spec)
		if statement == "" {
			return fmt.Errorf("不支持的数据库方言 %q：无法为 %s 建索引 %s", dialect, spec.table, spec.name)
		}
		if err := db.Exec(statement).Error; err != nil {
			// 并发启动的两个实例可能同时判定「索引不存在」，其中一个撞 already exists。
			// 再核一次元数据：索引确实在就算成功（不掩盖其它错误）。
			if db.Migrator().HasIndex(spec.table, spec.name) {
				continue
			}
			return fmt.Errorf("create index %s on %s fail: %w", spec.name, spec.table, err)
		}
		logger.SysLog(fmt.Sprintf("已为 %s 补上索引 %s", spec.table, spec.name))
	}
	return nil
}

// riskIndexDDL 按方言生成建索引语句；未知方言返回空串（由调用方报错）。
//
// 抽成纯函数是为了能直接断言两套分支的语句，不必真的连 MySQL
// （与 blacklistIndexDDL 的思路一致）。
func riskIndexDDL(dialect string, spec riskIndexSpec) string {
	kind := "INDEX"
	if spec.unique {
		kind = "UNIQUE INDEX"
	}
	quotedColumns := make([]string, len(spec.columns))
	for i, column := range spec.columns {
		quotedColumns[i] = quoteBillingIdentifier(column)
	}
	name := quoteBillingIdentifier(spec.name)
	table := quoteBillingIdentifier(spec.table)
	columns := strings.Join(quotedColumns, ", ")
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "sqlite":
		return fmt.Sprintf("CREATE %s IF NOT EXISTS %s ON %s (%s)", kind, name, table, columns)
	case "mysql":
		return fmt.Sprintf("CREATE %s %s ON %s (%s)", kind, name, table, columns)
	default:
		return ""
	}
}
