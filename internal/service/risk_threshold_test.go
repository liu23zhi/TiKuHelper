package service

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════════════════════
// 风控阈值的写入路径（96 §2.2「阈值可配」的真实落地）
//
// 在此之前 `risk_rule_<ruleId>_threshold` 没有任何写入路径，而
// `riskRuleThreshold` 的第一层兜底读的就是它们 ⇒ 那一层永远读不到值。
// 下面每条用例都在钉「配置真的接进判定」，而不是「handler 返回了 200」。
// ════════════════════════════════════════════════════════════════════

// 判据：登记表与 entity 的代码默认值必须逐条一致。
//
// 期望值一律写字面量（不引用 riskThresholdSpecs 的常量，也不引用 entity 常量），
// 这样任一侧漂移都会红。
func TestRiskThresholdSpecsMatchEntityDefaults(t *testing.T) {
	// rule_id → 代码默认阈值（字面量，故意不引用被测常量）。
	wantDefaults := map[string]float64{
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
	}
	// entity 侧：每条规则的默认阈值必须等于上面的字面量。
	seen := map[string]bool{}
	for _, rule := range entity.DefaultRiskRules() {
		want, ok := wantDefaults[rule.RuleID]
		if !ok {
			t.Fatalf("entity 里出现了登记表未覆盖的规则 %q（新规则必须同步进 riskThresholdSpecs）", rule.RuleID)
		}
		if rule.Threshold != want {
			t.Fatalf("%s 的代码默认阈值 = %v, want %v（字面量）", rule.RuleID, rule.Threshold, want)
		}
		seen[rule.RuleID] = true
	}
	for ruleID := range wantDefaults {
		if !seen[ruleID] {
			t.Fatalf("规则 %q 在 entity.DefaultRiskRules() 里缺失", ruleID)
		}
	}
	// 登记表侧：每条都要有对应的 <id>_threshold 键，且默认值一致。
	specs := riskThresholdSpecs()
	covered := map[string]bool{}
	for _, spec := range specs {
		if spec.Key == RiskThresholdSettingKeyOffhoursShare {
			// 占比项的默认值是 0.8，与规则的「连续天数」不同，单独断言。
			if spec.DefaultValue != 0.8 {
				t.Fatalf("占比阈值默认值 = %v, want 0.8", spec.DefaultValue)
			}
			covered["offhours_share"] = true
			continue
		}
		wantKey := "risk_rule_" + spec.RuleID + "_threshold"
		if spec.Key != wantKey {
			t.Fatalf("规则 %q 的键 = %q, want %q", spec.RuleID, spec.Key, wantKey)
		}
		if want, ok := wantDefaults[spec.RuleID]; ok && spec.DefaultValue != want {
			t.Fatalf("%s 登记默认值 = %v, want %v", spec.RuleID, spec.DefaultValue, want)
		}
		covered[spec.RuleID] = true
	}
	for ruleID := range wantDefaults {
		if !covered[ruleID] {
			t.Fatalf("规则 %q 没有对应的阈值登记项", ruleID)
		}
	}
	if !covered["offhours_share"] {
		t.Fatalf("R16 的占比阈值未登记")
	}
}

// 判据②：非法值必须被拒绝，且**不落库**。
func TestUpdateRiskThresholdRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value float64
	}{
		{name: "zero", key: "risk_rule_ticket_flood_threshold", value: 0},
		{name: "negative", key: "risk_rule_ticket_flood_threshold", value: -3},
		{name: "zero_debt", key: "risk_rule_debt_user_threshold", value: 0},
		// 占比越界：填 3 会让 `share < 3` 恒真、规则永不触发。
		{name: "ratio_over_one", key: "risk_offhours_share_threshold", value: 3},
		{name: "ratio_negative", key: "risk_offhours_share_threshold", value: -0.5},
		// 连续天数必须正整数。
		{name: "days_zero", key: "risk_rule_offhours_activity_threshold", value: 0},
		{name: "days_fractional", key: "risk_rule_offhours_activity_threshold", value: 2.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newRiskAlertTestDB(t)
			seedRiskRules(t, db)
			// 先记下改前的值，用于断言「没落库」。
			before, err := RiskThresholds(db)
			if err != nil {
				t.Fatalf("load before: %v", err)
			}
			beforeValue := riskThresholdItemValue(t, before, tc.key)

			if _, err := UpdateRiskThresholdWithDB(db, tc.key, tc.value); !errors.Is(err, ErrRiskThresholdInvalid) {
				t.Fatalf("设置 %v 必须返回 ErrRiskThresholdInvalid，实际 %v", tc.value, err)
			}
			// 关键：拒绝之后值必须完全没变（不能「报了错但还是写了」）。
			after, err := RiskThresholds(db)
			if err != nil {
				t.Fatalf("load after: %v", err)
			}
			afterValue := riskThresholdItemValue(t, after, tc.key)
			if afterValue != beforeValue {
				t.Fatalf("非法值不得落库：%v -> %v", beforeValue, afterValue)
			}
		})
	}
}

// 判据：未知键必须拒绝（白名单，不接受任意 system_settings 键）。
func TestUpdateRiskThresholdRejectsUnknownKey(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	if _, err := UpdateRiskThresholdWithDB(db, "some_random_setting_key", 5); !errors.Is(err, ErrRiskThresholdUnknown) {
		t.Fatalf("未知键必须返回 ErrRiskThresholdUnknown，实际 %v", err)
	}
	// 反向对照：已登记键必须能写（证明上面的拒绝不是「什么都拒」）。
	if _, err := UpdateRiskThresholdWithDB(db, "risk_rule_ticket_flood_threshold", 5); err != nil {
		t.Fatalf("已登记键必须可写，实际 %v", err)
	}
}

// 判据①：写入后落库（读回仍是新值），且两处都写。
func TestUpdateRiskThresholdPersistsBothPlaces(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	const key = "risk_rule_ticket_flood_threshold"
	if _, err := UpdateRiskThresholdWithDB(db, key, 2); err != nil {
		t.Fatalf("update: %v", err)
	}

	// 第一层：system_settings 有行且值为 2。
	if got := getSystemSettingStringWithDefault(db, key, ""); got != "2" {
		t.Fatalf("system_settings[%s] = %q, want \"2\"", key, got)
	}
	// 第二层：risk_rules.threshold 列同步为 2（否则设置行被删后会静默跳回旧值）。
	var row entity.RiskRule
	if err := db.Where("rule_id = ?", "ticket_flood").First(&row).Error; err != nil {
		t.Fatalf("load rule: %v", err)
	}
	if row.Threshold != 2 {
		t.Fatalf("risk_rules.threshold = %v, want 2", row.Threshold)
	}
	// 读接口回显的 Value/Source/Configured 都要正确。
	items, err := RiskThresholds(db)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	item := riskThresholdItemByKey(t, items, key)
	if item.Value != 2 {
		t.Fatalf("item.Value = %v, want 2", item.Value)
	}
	if item.Source != RiskThresholdSourceSetting {
		t.Fatalf("item.Source = %q, want %q", item.Source, RiskThresholdSourceSetting)
	}
	if !item.Configured {
		t.Fatalf("item.Configured 必须为 true（system_settings 有行）")
	}
}

// 判据：占比项**不得**污染 risk_rules.threshold（连续天数列）。
//
// 这是我在判据③ 里抓到的真实缺陷：R16 有两个可调项，RuleID 相同但语义不同。
// 若占比项也去写 risk_rules.threshold，「连续天数」会被覆盖成 0.9，
// 而 `consecutiveDays < 1` 会让 R16 **直接短路永不告警**，且没有任何报错。
func TestUpdateOffhoursShareDoesNotTouchConsecutiveDaysColumn(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	// 前置：连续天数默认 3。
	var before entity.RiskRule
	if err := db.Where("rule_id = ?", "offhours_activity").First(&before).Error; err != nil {
		t.Fatalf("load rule: %v", err)
	}
	if before.Threshold != 3 {
		t.Fatalf("前置条件：连续天数默认应为 3，实际 %v", before.Threshold)
	}

	// 改占比。
	if _, err := UpdateRiskThresholdWithDB(db, RiskThresholdSettingKeyOffhoursShare, 0.9); err != nil {
		t.Fatalf("update share: %v", err)
	}
	// 连续天数列必须**完全没变**。
	var after entity.RiskRule
	if err := db.Where("rule_id = ?", "offhours_activity").First(&after).Error; err != nil {
		t.Fatalf("load rule: %v", err)
	}
	if after.Threshold != 3 {
		t.Fatalf("改占比后 risk_rules.threshold 必须是 3（连续天数），实际 %v。"+
			"若变成 0.9，R16 会因 consecutiveDays<1 直接短路永不告警", after.Threshold)
	}
	// 连续天数阈值本身也要还是 3（读接口）。
	items, _ := RiskThresholds(db)
	if got := riskThresholdItemByKey(t, items, "risk_rule_offhours_activity_threshold").Value; got != 3 {
		t.Fatalf("连续天数阈值 = %v, want 3", got)
	}
}

// 判据：重置占比项也不得把连续天数列改掉。
func TestResetOffhoursShareDoesNotTouchConsecutiveDaysColumn(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	if _, err := UpdateRiskThresholdWithDB(db, RiskThresholdSettingKeyOffhoursShare, 0.9); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := ResetRiskThresholdWithDB(db, RiskThresholdSettingKeyOffhoursShare); err != nil {
		t.Fatalf("reset: %v", err)
	}
	var row entity.RiskRule
	if err := db.Where("rule_id = ?", "offhours_activity").First(&row).Error; err != nil {
		t.Fatalf("load rule: %v", err)
	}
	if row.Threshold != 3 {
		t.Fatalf("重置占比后连续天数列必须仍是 3，实际 %v", row.Threshold)
	}
}

// 判据：重置为默认 —— 删行 + 回写列，读回等于代码默认值且 Configured=false。
//
// 这条钉的是「已存在但未配置」与「显式配置成默认值」的区别（任务书 ③(b)）。
func TestResetRiskThresholdReturnsToCodeDefault(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	const key = "risk_rule_ticket_flood_threshold"

	// 先显式配成一个非默认值。
	if _, err := UpdateRiskThresholdWithDB(db, key, 9); err != nil {
		t.Fatalf("update: %v", err)
	}
	items, _ := RiskThresholds(db)
	if got := riskThresholdItemByKey(t, items, key); got.Value != 9 || !got.Configured {
		t.Fatalf("前置条件：应为 9 且 Configured=true，实际 %v/%v", got.Value, got.Configured)
	}

	result, err := ResetRiskThresholdWithDB(db, key)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if !result.Reset {
		t.Fatalf("result.Reset 必须为 true")
	}
	// 值回到代码默认 5。
	if result.Item.Value != 5 {
		t.Fatalf("重置后 Value = %v, want 5（字面量）", result.Item.Value)
	}
	// Configured 必须变 false：数据上「没有行」，而不是「有一行写着 5」。
	if result.Item.Configured {
		t.Fatalf("重置后 Configured 必须为 false（行已删除）")
	}
	// 行确实被删了。
	var count int64
	if err := db.Model(&entity.SystemSetting{}).Where("key = ?", key).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("重置后 system_settings 不应有 %q 行，实际 %d 行", key, count)
	}
}

// ════════════════════════════════════════════════════════════════════
// 判据③（本任务唯一能证明「配置真的接进了判定」的证据）
//
// 改阈值 → 同一批数据从「不告警」变「告警」。
// ════════════════════════════════════════════════════════════════════

// R8：一条用户建 3 单。阈值 5 → 不告警；改成 2 → 告警。
func TestRiskThresholdChangeAffectsTicketFloodEvaluation(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 3 张工单（阈值默认 5 → 不告警）。
	for i := 0; i < 3; i++ {
		seedRiskTicket(t, db, 777, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
	}

	// 阶段一：默认阈值 5 → 必须不告警。
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 0 {
		t.Fatalf("默认阈值 5 时 3 单必须不告警，实际 %d 条", got)
	}

	// 阶段二：通过**写入路径**把阈值改成 2。
	if _, err := UpdateRiskThresholdWithDB(db, "risk_rule_ticket_flood_threshold", 2); err != nil {
		t.Fatalf("update threshold: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate after update: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("阈值改成 2 后同一批 3 单必须告警，实际 %d 条。"+
			"若为 0 说明配置没有接进判定（写到别处了）", got)
	}
	// 证据里要能看出用了新阈值。
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", "ticket_flood").First(&warning).Error; err != nil {
		t.Fatalf("load warning: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	if got, _ := detail["threshold"].(float64); got != 2 {
		t.Fatalf("detail.threshold = %v, want 2（判定必须用到新阈值）", detail["threshold"])
	}
}

// R4 之外的第二个证据：R16 占比阈值生效（填 1 边界 + 收到 0.8 的差异）。
func TestRiskThresholdChangeAffectsOffhoursEvaluation(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	day := time.Date(2026, 11, 14, 0, 0, 0, 0, logger.ShanghaiLocation())
	// 凌晨 4 笔 + 白天 1 笔 = 80%。
	seedRiskOffhourDay(t, db, 1, day)
	assertRiskSeedIsUTC(t, db)

	// 占比阈值设为 0.9 → 80% 不足 → 不写当日记录。
	if _, err := UpdateRiskThresholdWithDB(db, RiskThresholdSettingKeyOffhoursShare, 0.9); err != nil {
		t.Fatalf("update share: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "offhours_activity"); got != 0 {
		t.Fatalf("占比阈值 0.9 时 80%% 必须不告警，实际 %d 条", got)
	}
	// 改成 0.5 → 80% 达标 → 写当日记录。
	if _, err := UpdateRiskThresholdWithDB(db, RiskThresholdSettingKeyOffhoursShare, 0.5); err != nil {
		t.Fatalf("update share: %v", err)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "offhours_activity"); got != 1 {
		t.Fatalf("占比阈值 0.5 时 80%% 必须告警，实际 %d 条", got)
	}
}

// ════════════════════════════════════════════════════════════════════
// enabled 列的写路径 + 它与判定的接线
//
// 任务书 ③(b) 明确要求：「停用后该规则不再产生告警」——
// 这是 loadRiskRules 与 EvaluateDebtAlerts 之间的接线，本项目反复在这一层出问题。
// ════════════════════════════════════════════════════════════════════

// 判据：停用后该规则不再产生告警（含反向对照：重新启用后必须再生效）。
func TestSetRiskRuleEnabledStopsWarnings(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 6 单 ≥ 默认阈值 5 → 默认会告警。
	for i := 0; i < 6; i++ {
		seedRiskTicket(t, db, 555, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
	}

	// 前置：默认启用 → 必须告警。
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("前置条件：默认启用时 6 单必须告警，实际 %d 条", got)
	}

	// 通过写入路径停用。
	state, err := SetRiskRuleEnabledWithDB(db, "ticket_flood", false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if state.Enabled {
		t.Fatalf("停用后 state.Enabled 必须为 false")
	}
	// 判定必须不再产生新告警（换到下一天，避开节流）。
	nextDay := now.AddDate(0, 0, 1)
	for i := 0; i < 6; i++ {
		seedRiskTicket(t, db, 556, entity.TicketOriginUser, nextDay.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, nextDay); err != nil {
		t.Fatalf("evaluate after disable: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("停用后不得产生新告警（应仍为 1 条），实际 %d 条。"+
			"若 >1 说明 enabled 没有接进判定", got)
	}

	// 反向对照：重新启用后必须再次告警。
	if _, err := SetRiskRuleEnabledWithDB(db, "ticket_flood", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	day3 := nextDay.AddDate(0, 0, 1)
	for i := 0; i < 6; i++ {
		seedRiskTicket(t, db, 557, entity.TicketOriginUser, day3.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, day3); err != nil {
		t.Fatalf("evaluate after enable: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 2 {
		t.Fatalf("重新启用后必须再次告警（应为 2 条），实际 %d 条", got)
	}
}

// 判据：停用「仅靠代码默认值补齐」的规则时要建行。
//
// 若只 Update 不 Create，下次 loadRiskRules 又按默认启用 →
// 表现为「停用了但没生效」，且没有任何报错。
func TestSetRiskRuleEnabledCreatesRowWhenMissing(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	// 先删掉这条规则的行，模拟「只有代码默认值」的状态。
	if err := db.Where("rule_id = ?", "token_abuse").Delete(&entity.RiskRule{}).Error; err != nil {
		t.Fatalf("delete row: %v", err)
	}
	if _, err := SetRiskRuleEnabledWithDB(db, "token_abuse", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	// 必须建出显式行，且 enabled=false。
	var row entity.RiskRule
	if err := db.Where("rule_id = ?", "token_abuse").First(&row).Error; err != nil {
		t.Fatalf("停用后必须留下显式行，实际查不到：%v", err)
	}
	if row.Enabled {
		t.Fatalf("行已建但 enabled 仍为 true")
	}
	// 关键：threshold 不能是 0（否则判定退化成 >= 0 刷屏）。
	if row.Threshold != 3 {
		t.Fatalf("建行时 threshold 必须用代码默认值 3，实际 %v", row.Threshold)
	}
}

// 判据：行不存在时「停用」必须真的让规则停止告警。
//
// ⚠️ 这条与 TestSetRiskRuleEnabledStopsWarnings 不重复：那条的行是 seedRiskRules 建好的，
// 走 UpdateColumn 分支；这条的行被删掉，走「Create 建行」分支 ——
// 而 Create 分支有 `default:true` 的坑（零值 false 被数据库默认值覆盖）。
// 实测：去掉 Create 后的 UpdateColumn 补写，只有本用例会红，
// StopsWarnings 仍绿（它走不到那条分支）—— 所以两条都必须存在。
func TestSetRiskRuleEnabledWithoutRowActuallyStopsWarnings(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 先删掉该规则的行（模拟「只有代码默认值」的部署）。
	if err := db.Where("rule_id = ?", "ticket_flood").Delete(&entity.RiskRule{}).Error; err != nil {
		t.Fatalf("delete row: %v", err)
	}
	// 6 单 ≥ 默认阈值 5 → 前置必须告警。
	for i := 0; i < 6; i++ {
		seedRiskTicket(t, db, 888, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("前置条件：行不存在时按默认启用，6 单必须告警，实际 %d 条", got)
	}

	// 停用（此时表里没有行 → 走 Create 分支）。
	if _, err := SetRiskRuleEnabledWithDB(db, "ticket_flood", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	// 次日再评估：必须不再产生新告警。
	nextDay := now.AddDate(0, 0, 1)
	for i := 0; i < 6; i++ {
		seedRiskTicket(t, db, 889, entity.TicketOriginUser, nextDay.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, nextDay); err != nil {
		t.Fatalf("evaluate after disable: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("行不存在时停用后必须不再告警（应仍 1 条），实际 %d 条。"+
			"若为 2 说明 Create 分支的 enabled=false 被数据库默认值覆盖了", got)
	}
}

// 判据：未知 ruleId 必须拒绝。
func TestSetRiskRuleEnabledRejectsUnknownRule(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	if _, err := SetRiskRuleEnabledWithDB(db, "no_such_rule", false); !errors.Is(err, ErrRiskRuleUnknown) {
		t.Fatalf("未知 ruleId 必须返回 ErrRiskRuleUnknown，实际 %v", err)
	}
	// 反向对照：已登记的必须能改。
	if _, err := SetRiskRuleEnabledWithDB(db, "debt_user", false); err != nil {
		t.Fatalf("已登记 ruleId 必须可改，实际 %v", err)
	}
}

// 判据：RiskRuleStates 报出当前生效状态（含 Exists 标记）。
func TestRiskRuleStatesReflectConfiguration(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	states, err := RiskRuleStates(db)
	if err != nil {
		t.Fatalf("states: %v", err)
	}
	if len(states) != 11 {
		t.Fatalf("规则数 = %d, want 11", len(states))
	}
	for _, state := range states {
		if !state.Exists {
			t.Fatalf("%s 已 seed 却报 Exists=false", state.RuleID)
		}
		if !state.Enabled {
			t.Fatalf("%s 默认应启用", state.RuleID)
		}
	}
	// 改一个阈值后，states 里的 Threshold 必须跟着变（证明它读的是生效值）。
	if _, err := UpdateRiskThresholdWithDB(db, "risk_rule_ticket_flood_threshold", 2); err != nil {
		t.Fatalf("update: %v", err)
	}
	states, _ = RiskRuleStates(db)
	for _, state := range states {
		if state.RuleID == "ticket_flood" && state.Threshold != 2 {
			t.Fatalf("ticket_flood 生效阈值 = %v, want 2", state.Threshold)
		}
	}
}

// 辅助：按 key 取 Value。
func riskThresholdItemValue(t *testing.T, items []RiskThresholdItem, key string) float64 {
	t.Helper()
	return riskThresholdItemByKey(t, items, key).Value
}

func riskThresholdItemByKey(t *testing.T, items []RiskThresholdItem, key string) RiskThresholdItem {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("阈值列表里找不到 %q", key)
	return RiskThresholdItem{}
}

// 保证 gorm 引用被使用（seedRiskRules 等辅助在本文件外，此处仅需类型）。
var _ = gorm.ErrRecordNotFound
