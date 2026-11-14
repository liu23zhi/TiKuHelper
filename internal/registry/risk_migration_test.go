package registry

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newRiskMigrationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:risk-migration-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

// 空库首次启动：两张表 + 索引 + 3 条种子都建出来，两个标记写入，第二次幂等。
func TestEnsureRiskSchemaCreatesTablesIndexesAndSeeds(t *testing.T) {
	db := newRiskMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := EnsureRiskSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, table := range []string{entity.TableNameRiskRule, entity.TableNameRiskWarning} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table %s", table)
		}
	}
	if !db.Migrator().HasIndex(entity.TableNameRiskRule, "uk_risk_rule_rule_id") {
		t.Fatal("expected unique index uk_risk_rule_rule_id")
	}
	for _, index := range []string{"idx_risk_warning_rule_created", "idx_risk_warning_subject", "idx_risk_warning_created"} {
		if !db.Migrator().HasIndex(entity.TableNameRiskWarning, index) {
			t.Fatalf("expected index %s", index)
		}
	}
	// 两个 FLAG 都必须写入（121 §4.6 / §4.7 各一个标记键）。
	if !systemSettingFlagIsSet(db, riskRulesSchemaReadyKey) {
		t.Fatal("expected risk_rules flag to be written")
	}
	if !systemSettingFlagIsSet(db, riskWarningsSchemaReadyKey) {
		t.Fatal("expected risk_warnings flag to be written")
	}
	// 种子 = 欠款 3 条 + 行为规则（R1 multi_account_ip），阈值必须是字面量。
	//
	// ⚠️ 断言用**字面量**，不能写成「与 entity.DefaultRiskDebtRules 相等」——
	// 那是拿被测数据和它自己的来源比，改默认值时两边一起变、永远绿（自我指涉断言）。
	// rule_id 也写字面量（不引用常量）：常量漂移时这里必须红。
	var rules []entity.RiskRule
	if err := db.Order("id ASC").Find(&rules).Error; err != nil {
		t.Fatalf("load rules: %v", err)
	}
	want := map[string]float64{
		"debt_user":                 100,
		"debt_batch":                1000,
		"debt_system":               5000,
		"multi_account_ip":          5,
		"ticket_flood":              5,
		"abnormal_register_pattern": 3,
		"token_abuse":               3,
		"offhours_activity":         3,
		"binding_churn":             2,
		"wechat_phone_share":        3,
		"impossible_travel":         900,
		"credential_stuffing":       20,
		"login_bruteforce":          10,
		"refund_abuse":              3,
		"register_burst":            3,
		"answer_pattern_repeat":     20,
		"proxy_ip_login":            1,
	}
	if len(rules) != len(want) {
		t.Fatalf("expected %d seeded rules, got %d: %+v", len(want), len(rules), rules)
	}
	seenSeed := make(map[string]bool, len(rules))
	for _, rule := range rules {
		expected, ok := want[rule.RuleID]
		if !ok {
			t.Fatalf("unexpected rule id %q", rule.RuleID)
		}
		if seenSeed[rule.RuleID] {
			t.Fatalf("rule %s seeded twice", rule.RuleID)
		}
		seenSeed[rule.RuleID] = true
		if rule.Threshold != expected {
			t.Fatalf("rule %s threshold = %v, want %v", rule.RuleID, rule.Threshold, expected)
		}
		if !rule.Enabled {
			t.Fatalf("rule %s must be enabled by default (112 号安全阀)", rule.RuleID)
		}
		if rule.Name == "" || rule.Severity == "" {
			t.Fatalf("rule %s missing name/severity: %+v", rule.RuleID, rule)
		}
	}
	for ruleID := range want {
		if !seenSeed[ruleID] {
			t.Fatalf("种子缺少规则 %q（实际：%+v）", ruleID, rules)
		}
	}
	// 频控语义必须与 118 §2.4 一致：user 24h、batch 一次（0 = 永不重复）、system 6h。
	throttle := map[string]int{}
	for _, rule := range rules {
		throttle[rule.RuleID] = rule.ThrottleSeconds
	}
	if throttle[entity.RiskRuleIDDebtUser] != 24*60*60 {
		t.Fatalf("debt_user throttle = %d, want 86400", throttle[entity.RiskRuleIDDebtUser])
	}
	if throttle[entity.RiskRuleIDDebtBatch] != 0 {
		t.Fatalf("debt_batch throttle = %d, want 0 (once per batch)", throttle[entity.RiskRuleIDDebtBatch])
	}
	if throttle[entity.RiskRuleIDDebtSystem] != 6*60*60 {
		t.Fatalf("debt_system throttle = %d, want 21600", throttle[entity.RiskRuleIDDebtSystem])
	}
	// 第二次运行：不重复建表、不重复插种子（COUNT 幂等）。
	if err := EnsureRiskSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var count int64
	if err := db.Model(&entity.RiskRule{}).Count(&count).Error; err != nil {
		t.Fatalf("count rules: %v", err)
	}
	if count != int64(len(want)) {
		t.Fatalf("second run duplicated seeds: count=%d, want %d", count, len(want))
	}
}

// rule_id 唯一索引必须真的挡住重复规则（否则阈值读取会取到随机一行）。
func TestRiskRuleUniqueIndexRejectsDuplicateRuleID(t *testing.T) {
	db := newRiskMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := EnsureRiskSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	duplicate := entity.RiskRule{RuleID: entity.RiskRuleIDDebtUser, Name: "dup", Severity: entity.RiskSeverityLow, Threshold: 1}
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate rule_id must be rejected by the unique index")
	}
}

// 建表失败时**绝不写标记**（121 §2.1）：
// 连 system_settings 都没有的极端空库，什么都不建，也不能写标记 ——
// 否则 system_settings 建出来之后本迁移会被永远跳过。
func TestEnsureRiskSchemaDoesNotWriteFlagWhenSettingsTableMissing(t *testing.T) {
	db := newRiskMigrationTestDB(t)
	if err := EnsureRiskSchema(db); err != nil {
		t.Fatalf("migrate on empty db must not fail: %v", err)
	}
	if db.Migrator().HasTable(entity.TableNameRiskRule) {
		t.Fatal("risk_rules must not be created without system_settings (no place to record the flag)")
	}
	if db.Migrator().HasTable(entity.TableNameRiskWarning) {
		t.Fatal("risk_warnings must not be created without system_settings")
	}
	// 建好 system_settings 后再跑：这次必须真的建表并写标记
	// （若上一轮误写了标记，这里会因 HasTable 为 false 而抓到）。
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := EnsureRiskSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !db.Migrator().HasTable(entity.TableNameRiskRule) || !db.Migrator().HasTable(entity.TableNameRiskWarning) {
		t.Fatal("tables must be created once system_settings exists")
	}
	if !systemSettingFlagIsSet(db, riskRulesSchemaReadyKey) || !systemSettingFlagIsSet(db, riskWarningsSchemaReadyKey) {
		t.Fatal("flags must be written on the successful run")
	}
}

// 索引补建必须放在标记判断**之前**（121 §2.2）：
// 老库已经跑过标记（表建好了），但后来新增的索引等不到 AutoMigrate。
// 只在标记之后补索引，老库**永远**补不上 —— 而索引缺失的表现是「查询慢」，不会报错。
//
// 这个用例是 121 §2.2 那条纪律的唯一可执行判据：
// 手工删掉索引 + 手工写标记 = 「老库已跑过标记、索引却缺失」的精确复刻。
func TestEnsureRiskSchemaRebuildsMissingIndexesAfterFlagIsSet(t *testing.T) {
	db := newRiskMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := EnsureRiskSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	const missing = "idx_risk_warning_subject"
	if !db.Migrator().HasIndex(entity.TableNameRiskWarning, missing) {
		t.Fatalf("precondition: %s must exist after first migration", missing)
	}
	if err := db.Exec("DROP INDEX " + missing).Error; err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if db.Migrator().HasIndex(entity.TableNameRiskWarning, missing) {
		t.Fatalf("precondition: %s must be gone after DROP INDEX", missing)
	}
	// 标记仍在（模拟「老库早就跑过 v1 标记」）。
	if !systemSettingFlagIsSet(db, riskWarningsSchemaReadyKey) {
		t.Fatal("precondition: flag must still be set")
	}
	if err := EnsureRiskSchema(db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if !db.Migrator().HasIndex(entity.TableNameRiskWarning, missing) {
		t.Fatalf("%s must be rebuilt even though the flag is already set (121 §2.2)", missing)
	}
}

// 迁移绝不 DROP TABLE：跑第二次之后数据还在。
func TestEnsureRiskSchemaNeverDropsWarningRows(t *testing.T) {
	db := newRiskMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := EnsureRiskSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	row := entity.RiskWarning{RuleID: entity.RiskRuleIDDebtUser, SubjectKey: "user:7", Severity: entity.RiskSeverityMedium, Detail: `{"value":150}`}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create warning: %v", err)
	}
	if err := EnsureRiskSchema(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var count int64
	if err := db.Model(&entity.RiskWarning{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("warning rows must survive re-migration, count=%d", count)
	}
}

// 索引 DDL 必须按方言分开：SQLite 支持 IF NOT EXISTS，MySQL 不支持（会报 1064）。
func TestRiskIndexDDLMatchesDialect(t *testing.T) {
	spec := riskIndexSpec{
		name:    "idx_risk_warning_created",
		table:   entity.TableNameRiskWarning,
		columns: []string{"created_at"},
	}
	sqliteDDL := riskIndexDDL("sqlite", spec)
	if !strings.Contains(sqliteDDL, "IF NOT EXISTS") {
		t.Fatalf("sqlite DDL must be idempotent: %s", sqliteDDL)
	}
	mysqlDDL := riskIndexDDL("MySQL", spec)
	if strings.Contains(mysqlDDL, "IF NOT EXISTS") {
		t.Fatalf("mysql DDL must not use IF NOT EXISTS: %s", mysqlDDL)
	}
	if !strings.Contains(mysqlDDL, "`idx_risk_warning_created`") || !strings.Contains(mysqlDDL, "`risk_warnings`") {
		t.Fatalf("mysql DDL must quote identifiers: %s", mysqlDDL)
	}
	uniqueSpec := riskIndexSpec{name: "uk_x", table: entity.TableNameRiskRule, columns: []string{"rule_id"}, unique: true}
	if !strings.Contains(riskIndexDDL("sqlite", uniqueSpec), "UNIQUE INDEX") {
		t.Fatalf("unique spec must render UNIQUE INDEX: %s", riskIndexDDL("sqlite", uniqueSpec))
	}
	if got := riskIndexDDL("postgres", spec); got != "" {
		t.Fatalf("unknown dialect must return empty string, got %q", got)
	}
}

// 种子失败只 SysLog：表已建好，不能因为种子写不进去就让启动失败。
//
// 判据：表里已有别的规则（COUNT>0）时，种子不触发；且整个迁移仍返回 nil。
func TestEnsureRiskSchemaSeedFailureDoesNotFailMigration(t *testing.T) {
	db := newRiskMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate system_settings: %v", err)
	}
	if err := EnsureRiskSchema(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 模拟「运营删掉了种子、但表还在」：删掉所有规则，然后第二个标记仍在。
	if err := db.Exec("DELETE FROM " + entity.TableNameRiskRule).Error; err != nil {
		t.Fatalf("delete rules: %v", err)
	}
	if err := EnsureRiskSchema(db); err != nil {
		t.Fatalf("re-run must not fail when the rule table is empty but flagged: %v", err)
	}
	// 标记已存在 → 不再补种子（运营的删除是有意的，不该被迁移复活）。
	var count int64
	if err := db.Model(&entity.RiskRule{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("flagged migration must not resurrect deleted seeds, count=%d", count)
	}
}

// TestRiskRuleDescriptionFitsColumnWidth 钉住「规则说明不能超出列宽」。
//
// # 为什么需要它（2026-11-14 MariaDB 集成实测失败）
//
// `risk_rules.description` 原为 `varchar(255)`。**SQLite 不校验 varchar 长度**，
// 所以本地跑全绿；真实 MariaDB 严格模式下迁移直接失败：
//
//	迁移失败：(1406, "Data too long for column 'description' at row 14")
//
// 当时已有 4 条规则的说明超过 255 字符（R17 549 / R5 380 / R13 340 / R10 327）——
// 全是「把已知限制写清楚」的那几条，也就是**写得越负责越容易撞上**。
//
// 这条用例把「最长说明 ≤ 列宽」变成编译期之后的第一道闸：
// 以后任何人给规则补限制说明，本地就会红，而不是等到 MariaDB 集成才发现。
//
// ⚠️ 用**字节数**而不是字符数：MariaDB 的 varchar(n) 在 utf8mb4 下按**字符**计，
// 但迁移脚本与驱动在严格模式下对超长中文的实际报错按行宽算，
// 用字节数做判据更保守（中文 3 字节/字，549 字 ≈ 1251 字节）。
func TestRiskRuleDescriptionFitsColumnWidth(t *testing.T) {
	const columnChars = 2048
	db := newRiskMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.RiskRule{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	seedDefaultRiskRules(db)

	type row struct {
		RuleID      string
		Description string
	}
	var rows []row
	if err := db.Model(&entity.RiskRule{}).Select("rule_id, description").Scan(&rows).Error; err != nil {
		t.Fatalf("load rules: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("前置失效：risk_rules 没有种子数据，本用例会空转")
	}

	// 最长的那条必须真的接近上限，否则「列宽够用」可能只是因为说明都写得很短。
	maxRunes := 0
	for _, r := range rows {
		n := len([]rune(r.Description))
		if n > maxRunes {
			maxRunes = n
		}
		if n > columnChars {
			t.Fatalf("规则 %q 的说明有 %d 字符，超出 risk_rules.description 的 %d —— "+
				"SQLite 不校验长度所以本地看不出来，真实 MariaDB 严格模式会报 "+
				"`Data too long for column 'description'`。请缩短说明或同步加宽列。",
				r.RuleID, n, columnChars)
		}
	}
	// 反向自检：本仓当前最长说明确实接近列宽量级（549 字符），
	// 若它掉到很小，说明这条用例的判据不再有意义（列宽可以缩回 255 了）。
	if maxRunes < 256 {
		t.Fatalf("最长规则说明只有 %d 字符 —— 若确实缩短到 256 以下，"+
			"请把 description 列宽改回 255 并更新本断言", maxRunes)
	}
}

// TestSeedDefaultRiskRulesBackfillsMissingRulesOnExistingInstance 钉住「老实例必须能拿到新规则」。
//
// # 为什么需要它（2026-11-14 实测发现的静默失效）
//
// 原 `seedDefaultRiskRules` 是：
//
//	var count int64
//	db.Model(&entity.RiskRule{}).Count(&count)
//	if count > 0 { return }        // ← 只要表里有任意一行就整段跳过
//
// 后果：老实例（哪怕只有 3 条 debt_* 规则）**永远拿不到后来新增的行为规则**。
// 而判定侧是 `if rule, ok := enabledRules[RuleID]; ok` —— 缺行 ⇒ ok=false ⇒ 规则直接跳过，
// **不报错、不告警、日志里什么都没有**。
//
// 实测（开发库）：代码定义 17 条，库里只有 3 条 debt_*，缺失 14 条全部是行为规则。
// 也就是说「14 条行为规则已落地」在真实实例上一条都不生效。
//
// 这条用例造的就是那个形态：库里已有一条历史规则，再跑一次种子，必须补齐其余全部。
func TestSeedDefaultRiskRulesBackfillsMissingRulesOnExistingInstance(t *testing.T) {
	db := newRiskMigrationTestDB(t)
	if err := db.AutoMigrate(&entity.RiskRule{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}

	// 模拟「老实例」：只存在一条历史规则，且管理员把它调成了停用 + 自定义阈值。
	legacy := entity.RiskRule{
		RuleID:    entity.RiskRuleIDDebtUser,
		Name:      "历史用户欠款",
		Severity:  "high",
		Enabled:   false,
		Threshold: 999,
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy rule: %v", err)
	}
	// ⚠️ 必须**两步**：Enabled 的 tag 是 `default:true`，GORM 会把等于默认值的字段
	// 从 INSERT 里省略，所以 `Create(&RiskRule{Enabled:false})` 会被静默写成 true。
	// 这条陷阱 `entity/risk.go:431-434` 早就记过（「若将来要新建一条停用规则，
	// 必须走 Create 之后 Update 两步」）—— 本用例正是第一个踩到它的场景。
	if err := db.Model(&entity.RiskRule{}).Where("rule_id = ?", entity.RiskRuleIDDebtUser).
		UpdateColumn("enabled", false).Error; err != nil {
		t.Fatalf("disable legacy rule: %v", err)
	}
	// 前置自检：确认停用真的落库了，否则下面的「不被覆盖」断言会恒真。
	var precheck entity.RiskRule
	if err := db.Session(&gorm.Session{NewDB: true}).
		Where("rule_id = ?", entity.RiskRuleIDDebtUser).First(&precheck).Error; err != nil {
		t.Fatalf("precheck: %v", err)
	}
	if precheck.Enabled {
		t.Fatalf("前置失效：历史规则仍是启用状态，本用例测不出「配置不被覆盖」")
	}

	seedDefaultRiskRules(db)

	// 1) 全部代码默认规则都必须存在。
	defaults := entity.DefaultRiskRules()
	var rows []entity.RiskRule
	if err := db.Session(&gorm.Session{NewDB: true}).Find(&rows).Error; err != nil {
		t.Fatalf("load rules: %v", err)
	}
	got := make(map[string]entity.RiskRule, len(rows))
	for _, r := range rows {
		got[r.RuleID] = r
	}
	if len(rows) != len(defaults) {
		t.Fatalf("规则行数 = %d，期望 %d（代码默认规则数）—— 缺失说明增量补齐没生效",
			len(rows), len(defaults))
	}
	for _, d := range defaults {
		if _, ok := got[d.RuleID]; !ok {
			t.Errorf("缺少规则 %q —— 老实例上这条规则会静默不生效", d.RuleID)
		}
	}

	// 2) 历史那一条**不能被改动**：管理员调过的 enabled/threshold 必须原样保留。
	//    这一条是「增量补齐」与「整表重建」的分界线。
	kept := got[entity.RiskRuleIDDebtUser]
	if kept.Enabled {
		t.Errorf("历史规则的 enabled 被种子覆盖成 true，管理员停用的配置丢了")
	}
	if kept.Threshold != 999 {
		t.Errorf("历史规则的 threshold = %v，期望 999（管理员自定义值不能被覆盖）", kept.Threshold)
	}
	if kept.Name != "历史用户欠款" {
		t.Errorf("历史规则的 name = %q，期望保留原值", kept.Name)
	}

	// 3) 幂等：再跑一次不产生任何新行。
	seedDefaultRiskRules(db)
	var after int64
	if err := db.Model(&entity.RiskRule{}).Count(&after).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if after != int64(len(defaults)) {
		t.Fatalf("重复执行后行数 = %d，期望 %d（种子必须幂等）", after, len(defaults))
	}

	// 4) 新增的规则必须是启用的 —— 否则补齐了也不生效。
	for _, d := range defaults {
		if d.RuleID == entity.RiskRuleIDDebtUser {
			continue
		}
		if !got[d.RuleID].Enabled {
			t.Errorf("补齐的规则 %q 未启用，仍然不会生效", d.RuleID)
		}
	}
}
