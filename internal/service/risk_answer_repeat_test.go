package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════════════════════
// R17 · answer_pattern_repeat（97 §5.1 L326）
//
// 判据：同一 user_id + 同一归一化题干，在 7 天滚动窗口内 ≥N 次（默认 20）。
//
// ⚠️ 本规则统计的是「**搜题**」不是「提交答案」——
// 数据源 `billing_records.type` 实测为 `normal_search`。
// 97 号原文写的是「重复提交」+「答题日志」，两者本仓都没有。
// 这个偏差是本轮必须记明的语义差异，见 risk_answer_repeat.go 顶部注释。
// ════════════════════════════════════════════════════════════════════

// seedAnswerRepeatBill 造一条搜题计费行。
func seedAnswerRepeatBill(t *testing.T, db *gorm.DB, userID int32, question string,
	billType string, hidden bool, at time.Time) entity.BillingRecord {
	t.Helper()
	row := entity.BillingRecord{
		UserID:              userID,
		Type:                billType,
		Provider:            "icodef",
		Amount:              0.5,
		QuestionText:        question,
		HiddenFromStatement: hidden,
		RequestID:           "req-" + itoaRisk(int32(at.UnixNano()%1000000)) + "-" + itoaRisk(userID),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create billing record: %v", err)
	}
	// created_at 由 GORM autoCreateTime 填当前时间，回写目标时间以控制窗口。
	if err := db.Model(&entity.BillingRecord{}).Where("id = ?", row.ID).
		UpdateColumn("created_at", at).Error; err != nil {
		t.Fatalf("backdate billing record: %v", err)
	}
	return row
}

const riskR17LongQuestion = "下列关于半加器与全加器的说法中，正确的是哪一项"

// 判据①（**核心**）：同一用户对同一题重复 20 次 → 告警。
func TestAnswerRepeatDetectsRepeatSearch(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "r17-hit")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 20 次同题搜索。
	for i := 0; i < 20; i++ {
		seedAnswerRepeatBill(t, db, 7701, riskR17LongQuestion, "normal_search", false,
			now.Add(-time.Duration(i+1)*time.Hour))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 1 {
		t.Fatalf("同一用户同题搜 20 次必须告警，实际 %d 条", got)
	}
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", "answer_pattern_repeat").First(&warning).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	// 管理员要能认出「他在刷哪道题」。
	if got, _ := detail["questionSnippet"].(string); got == "" {
		t.Fatalf("detail.questionSnippet 不能为空（管理员据此判断刷的是哪道题）")
	}
	if got, _ := detail["repeatCount"].(float64); got != 20 {
		t.Fatalf("detail.repeatCount = %v, want 20", detail["repeatCount"])
	}
	// 硬要求：threshold 是配置值（20），不是实际计数。
	if got, _ := detail["threshold"].(float64); got != 20 {
		t.Fatalf("detail.threshold = %v, want 20（配置值）", detail["threshold"])
	}
	if got, _ := detail["windowDays"].(float64); got != 7 {
		t.Fatalf("detail.windowDays = %v, want 7", detail["windowDays"])
	}
}

// 判据②（**核心对照**）：不同用户搜同一题 → 不告警（那是「热门题」）。
//
// 变异方向：去掉 user_id 分组（变成全站同题）→ 本用例必红。
func TestAnswerRepeatIgnoresCrossUserSameQuestion(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "r17-crossuser")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 4 个用户轮流搜同一道题，每用户 5 次 —— 全站 20 次，但每人只有 5 次。
	for i := 0; i < 5; i++ {
		for _, userID := range []int32{7801, 7802, 7803, 7804} {
			seedAnswerRepeatBill(t, db, userID, riskR17LongQuestion, "normal_search", false,
				now.Add(-time.Duration(i*4+int(userID-7801)+1)*time.Hour))
		}
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 0 {
		t.Fatalf("4 个用户各搜 5 次（全站 20 次）不该告警 —— 那说明分组没按 user_id，"+
			"把「热门题」误当成「个人重复」了，实际 %d 条", got)
	}
}

// 判据③：同一用户搜**不同**题 → 不告警。
func TestAnswerRepeatIgnoresDifferentQuestions(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "r17-diffq")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 同一用户 25 道**不同**题各 1 次。
	for i := 0; i < 25; i++ {
		question := riskR17LongQuestion + "（第 " + itoaRisk(int32(i)) + " 题）"
		seedAnswerRepeatBill(t, db, 7702, question, "normal_search", false,
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 0 {
		t.Fatalf("同一用户搜 25 道**不同**题不该告警，实际 %d 条", got)
	}
}

// 判据④：题干为空 → 跳过。
//
// 变异方向：去掉空题跳过 → 本用例必红（所有空题会被聚成一道「题」）。
func TestAnswerRepeatSkipsEmptyQuestion(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "r17-empty")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := 0; i < 25; i++ {
		seedAnswerRepeatBill(t, db, 7703, "", "normal_search", false,
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 0 {
		t.Fatalf("空题干不得当成同一道题，实际 %d 条", got)
	}
}

// 判据⑤：**短题干跳过**（"1+1" 这类会天然误合不同题）。
func TestAnswerRepeatSkipsShortQuestion(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "r17-short")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 25 次 3 个字符的题干（低于 8 字符下限）。
	for i := 0; i < 25; i++ {
		seedAnswerRepeatBill(t, db, 7704, "1+1", "normal_search", false,
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 0 {
		t.Fatalf("短题干（< 8 字符）会天然误合不同题，必须跳过，实际 %d 条", got)
	}
}

// 判据⑥：**归一化** —— 只差空白（含全角空格、连续空格、换行）的题干算同一道题。
func TestAnswerRepeatNormalizesWhitespace(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "r17-norm")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 20 次，同一道题但空白的**类型与数量**不同，归一化后必须相同。
	//
	// ⚠️ 这里刻意**不构造「零空白 vs 有空白」的对照**：本仓归一化是
	// 「折叠连续空白成一个空格」，不是「删除所有空白」（与既有
	// `search.normalizeTikuText` 一致）。所以「，正确」与「， 正确」
	// 归一化后**仍然不同** —— 那是已知的漏报面，不是缺陷。
	// 基准串里**已经含一个空格**（"说法中， 正确的是"），变体只改变这个空格的
	// 类型与数量 —— 这样归一化（折叠）之后才会真正相同。
	variants := []string{
		"下列关于半加器与全加器的说法中， 正确的是哪一项",
		"  下列关于半加器与全加器的说法中， 正确的是哪一项  ",
		"下列关于半加器与全加器的说法中，  正确的是哪一项",
		"下列关于半加器与全加器的说法中，\t正确的是哪一项",
		"下列关于半加器与全加器的说法中，\n正确的是哪一项",
		"下列关于半加器与全加器的说法中，\u3000正确的是哪一项",
	}
	// 先自证「这 6 个变体确实都归一到同一个串」——
	// 否则归一化坏了这条用例也会绿（等于测了个空）。
	base := normalizeAnswerRepeatQuestion(variants[0])
	for i, variant := range variants {
		if got := normalizeAnswerRepeatQuestion(variant); got != base {
			t.Fatalf("变体 %d 归一化后 = %q, 期望与基准 %q 相同（用例前提不成立）", i, got, base)
		}
	}
	for i := 0; i < 20; i++ {
		seedAnswerRepeatBill(t, db, 7705, variants[i%len(variants)], "normal_search", false,
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 1 {
		t.Fatalf("只差空白的题干必须归一化后算同一道题，实际 %d 条告警"+
			"（若为 0 说明没归一化，会大量漏报）", got)
	}
}

// 判据⑦：`type` 不是 normal_search 的行不参与（余额调整/充值不是搜题）。
func TestAnswerRepeatIgnoresNonSearchTypes(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "r17-type")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := 0; i < 25; i++ {
		billType := "recharge"
		if i%2 == 0 {
			billType = "balance_adjustment"
		}
		seedAnswerRepeatBill(t, db, 7706, riskR17LongQuestion, billType, false,
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 0 {
		t.Fatalf("非 normal_search 的行不参与 R17，实际 %d 条", got)
	}
}

// 判据⑧：`hidden_from_statement = true` 的行排除（那是调整流水）。
func TestAnswerRepeatIgnoresHiddenRows(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "r17-hidden")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := 0; i < 25; i++ {
		seedAnswerRepeatBill(t, db, 7707, riskR17LongQuestion, "normal_search", true,
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 0 {
		t.Fatalf("hidden_from_statement=true 的行不参与 R17，实际 %d 条", got)
	}
}

// 判据⑨：7 天滚动窗口 —— 窗口外不计入。
func TestAnswerRepeatRollingWindow(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "r17-window")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 20 次，但都在 8–9 天前（窗口外）。
	for i := 0; i < 20; i++ {
		seedAnswerRepeatBill(t, db, 7708, riskR17LongQuestion, "normal_search", false,
			now.Add(-time.Duration(8*24+i)*time.Hour))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 0 {
		t.Fatalf("8 天前的搜索不在 7 天窗口内，不得告警，实际 %d 条", got)
	}
}

// 判据⑩：阈值边界 —— 19 不报，**恰好 20 报**（判据是 `>=` 不是 `>`）。
func TestAnswerRepeatThresholdBoundary(t *testing.T) {
	t.Run("nineteen_below_threshold", func(t *testing.T) {
		db := newRiskAlertTestDBNamed(t, "r17-19")
		seedRiskRules(t, db)
		now := riskR1TestTime()
		for i := 0; i < 19; i++ {
			seedAnswerRepeatBill(t, db, 7709, riskR17LongQuestion, "normal_search", false,
				now.Add(-time.Duration(i+1)*time.Minute))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 0 {
			t.Fatalf("19 次 < 阈值 20 必须不告警，实际 %d 条", got)
		}
	})
	t.Run("exactly_twenty_alerts", func(t *testing.T) {
		db := newRiskAlertTestDBNamed(t, "r17-20")
		seedRiskRules(t, db)
		now := riskR1TestTime()
		for i := 0; i < 20; i++ {
			seedAnswerRepeatBill(t, db, 7710, riskR17LongQuestion, "normal_search", false,
				now.Add(-time.Duration(i+1)*time.Minute))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 1 {
			t.Fatalf("恰好 20 次 = 阈值 20 必须告警（判据是 >= 不是 >），实际 %d 条", got)
		}
	})
}

// 判据⑪：阈值可配（双向）—— 这是「搜题口径下 20 是否太严」的运营出口。
func TestAnswerRepeatThresholdIsConfigurable(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "r17-cfg")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := 0; i < 5; i++ {
		seedAnswerRepeatBill(t, db, 7711, riskR17LongQuestion, "normal_search", false,
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	// 阈值抬到 50：5 次远不足。
	putRiskSetting(t, db, "risk_rule_answer_pattern_repeat_threshold", "50")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 0 {
		t.Fatalf("阈值 50 时 5 次必须不告警，实际 %d 条", got)
	}
	// 阈值降到 3：必须告警。
	putRiskSetting(t, db, "risk_rule_answer_pattern_repeat_threshold", "3")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 1 {
		t.Fatalf("阈值 3 时 5 次必须告警，实际 %d 条", got)
	}
}

// 判据⑫：只告警不处置（C1）。
func TestAnswerRepeatNeverTakesAction(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "r17-noaction")
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate blacklist: %v", err)
	}
	seedRiskRules(t, db)
	now := riskR1TestTime()
	user := entity.User{Username: "repeat-target", Password: "x", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for i := 0; i < 20; i++ {
		seedAnswerRepeatBill(t, db, user.ID, riskR17LongQuestion, "normal_search", false,
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "answer_pattern_repeat"); got != 1 {
		t.Fatalf("前置条件：必须已告警，实际 %d 条", got)
	}
	var after entity.User
	if err := db.First(&after, user.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != "enabled" {
		t.Fatalf("C1：风控不得改用户状态（97 号说「封号 1 天」，但本实现只告警），实际 %q",
			after.Status)
	}
	var blacklist int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&blacklist).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if blacklist != 0 {
		t.Fatalf("C1：风控不得写黑名单，实际 %d 行", blacklist)
	}
}

// 判据⑬：`normalizeAnswerRepeatQuestion` 的单元真值表。
func TestNormalizeAnswerRepeatQuestion(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"trim_only", "  题目  ", "题目"},
		{"collapse_spaces", "题目  有  空格", "题目 有 空格"},
		{"collapse_tab", "题目\t\t有\t制表", "题目 有 制表"},
		{"collapse_newline", "题目\n有\n换行", "题目 有 换行"},
		{"collapse_fullwidth_space", "题目\u3000有\u3000全角", "题目 有 全角"},
		{"mixed", "  题目 \t\n 混合 \u3000 空白  ", "题目 混合 空白"},
		{"empty", "", ""},
		{"only_whitespace", " \t\n\u3000 ", ""},
		{"no_whitespace", "没有任何空白", "没有任何空白"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeAnswerRepeatQuestion(tc.raw); got != tc.want {
				t.Fatalf("normalizeAnswerRepeatQuestion(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
