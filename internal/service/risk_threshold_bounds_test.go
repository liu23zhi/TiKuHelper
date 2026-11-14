package service

import (
	"fmt"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// ════════════════════════════════════════════════════════════════════
// 阈值上界（t193 审计的第三个问题）
//
// 原实现只有下界。实测填 R16 连续天数 = 1440 → 规则静默失效（0 告警、2ms），
// 而管理页显示「阈值 = 1440」看起来完全正常。
//
// 本文件钉两件事：
//  1. **每类都有一条「超上界必拒」**（不是只测某一类就以为全覆盖）
//  2. **上界本身不能误伤正当配置**（默认值、以及明显合理的大值都必须放过）
//
// ⚠️ 第 2 点比第 1 点更容易漏：只写「超上界要拒」的话，把上界设成 1
// （比默认值还小）所有用例照样绿，但系统已经不可用了。
// ════════════════════════════════════════════════════════════════════

// 判据①：**每类**都拒绝超上界的值。
func TestRiskThresholdRejectsValuesAboveMax(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		kind  string
		value float64
		// wantMax 是该项的规格上界，用例自检用（防止调到不该调的值）。
		wantMax float64
	}{
		// 计数类（次数 / 账号数 / 建单数）
		{"R1 同 IP 账号数", "risk_rule_multi_account_ip_threshold", RiskThresholdKindAbsolute, 10_001, 10_000},
		{"R2 失败次数", "risk_rule_login_bruteforce_threshold", RiskThresholdKindAbsolute, 10_001, 10_000},
		{"R8 建单数", "risk_rule_ticket_flood_threshold", RiskThresholdKindAbsolute, 10_001, 10_000},
		{"R9 不同账号数", "risk_rule_credential_stuffing_threshold", RiskThresholdKindAbsolute, 10_001, 10_000},
		{"R11 同前缀用户名数", "risk_rule_abnormal_register_pattern_threshold", RiskThresholdKindAbsolute, 10_001, 10_000},
		{"R14 换绑次数", "risk_rule_binding_churn_threshold", RiskThresholdKindAbsolute, 1_001, 1_000},
		{"R18 同号关联账号数", "risk_rule_wechat_phone_share_threshold", RiskThresholdKindAbsolute, 10_001, 10_000},
		// 天数类
		{"R16 连续天数", "risk_rule_offhours_activity_threshold", RiskThresholdKindInteger, 366, 365},
		// 倍数类
		{"R12 配额倍数", "risk_rule_token_abuse_threshold", RiskThresholdKindAbsolute, 1_001, 1_000},
		// 速度类
		{"R3 速度", "risk_rule_impossible_travel_threshold", RiskThresholdKindAbsolute, 40_001, 40_000},
		// 金额类
		{"debt_user 金额", "risk_rule_debt_user_threshold", RiskThresholdKindAbsolute, 1_000_001, 1_000_000},
		{"debt_system 金额", "risk_rule_debt_system_threshold", RiskThresholdKindAbsolute, 10_000_001, 10_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 构造自检：确认规格里真有这个上界（防止我把 key 拼错导致用例恒绿）。
			spec, ok := riskThresholdSpecByKey(tc.key)
			if !ok {
				t.Fatalf("前置条件失效：规格表里查不到 %s", tc.key)
			}
			if spec.MaxValue != tc.wantMax {
				t.Fatalf("前置条件失效：%s 的上界是 %v，用例期望 %v",
					tc.key, spec.MaxValue, tc.wantMax)
			}
			err := ValidateRiskThresholdValueForKey(tc.key, tc.kind, tc.value)
			if err == nil {
				t.Fatalf("%s 填 %v 超过上界 %v，必须被拒绝（否则规则会静默失效）",
					tc.key, tc.value, tc.wantMax)
			}
		})
	}
}

// 判据②（**防止误伤**）：默认值与合理的大值必须放过。
//
// 少了这条，把上界设成「比默认值还小」所有「超上界被拒」用例照样绿，
// 但系统已经不能用了 —— 这是「上界实现」最容易出的错。
func TestRiskThresholdAcceptsReasonableValues(t *testing.T) {
	// 全部规格的**默认值**必须自身合法。
	for _, spec := range riskThresholdSpecs() {
		if err := ValidateRiskThresholdValueForKey(spec.Key, spec.Kind, spec.DefaultValue); err != nil {
			t.Fatalf("%s 的默认值 %v 不合法：%v —— 上界把默认值都拦了，系统不可用",
				spec.Key, spec.DefaultValue, err)
		}
	}
	// 各上界本身必须合法（边界值可填）。
	for _, spec := range riskThresholdSpecs() {
		if spec.MaxValue <= 0 {
			continue
		}
		if err := ValidateRiskThresholdValueForKey(spec.Key, spec.Kind, spec.MaxValue); err != nil {
			t.Fatalf("%s 的上界值 %v 本身不合法（边界应可填）：%v",
				spec.Key, spec.MaxValue, err)
		}
	}
	// 几个「明显合理但远大于默认」的配置必须放过。
	reasonable := []struct {
		key   string
		kind  string
		value float64
	}{
		{"risk_rule_multi_account_ip_threshold", RiskThresholdKindAbsolute, 500},
		{"risk_rule_login_bruteforce_threshold", RiskThresholdKindAbsolute, 100},
		{"risk_rule_offhours_activity_threshold", RiskThresholdKindInteger, 30},
		{"risk_rule_impossible_travel_threshold", RiskThresholdKindAbsolute, 1200},
		{"risk_offhours_share_threshold", RiskThresholdKindRatio, 0.95},
	}
	for _, tc := range reasonable {
		if err := ValidateRiskThresholdValueForKey(tc.key, tc.kind, tc.value); err != nil {
			t.Fatalf("合理值 %s=%v 不该被拒：%v", tc.key, tc.value, err)
		}
	}
}

// 判据③：**下界仍然有效**（本次改动不能把原有校验弄丢）。
func TestRiskThresholdStillRejectsBelowMin(t *testing.T) {
	cases := []struct {
		key   string
		kind  string
		value float64
	}{
		{"risk_rule_multi_account_ip_threshold", RiskThresholdKindAbsolute, 0},
		{"risk_rule_multi_account_ip_threshold", RiskThresholdKindAbsolute, -1},
		{"risk_rule_offhours_activity_threshold", RiskThresholdKindInteger, 0},
		{"risk_rule_offhours_activity_threshold", RiskThresholdKindInteger, 2.5},
		{"risk_offhours_share_threshold", RiskThresholdKindRatio, 0},
		{"risk_offhours_share_threshold", RiskThresholdKindRatio, 1.5},
	}
	for _, tc := range cases {
		if err := ValidateRiskThresholdValueForKey(tc.key, tc.kind, tc.value); err == nil {
			t.Fatalf("%s 填 %v 应被拒（下界校验丢了）", tc.key, tc.value)
		}
	}
}

// 判据④：单参数旧签名行为不变（向后兼容）。
//
// 空 key 查不到规格 ⇒ 不加上界限制。这是刻意的：老调用方不知道 key。
func TestValidateRiskThresholdValueKeepsLegacyBehavior(t *testing.T) {
	// 旧签名不查上界：1440 在绝对数上合法。
	if err := ValidateRiskThresholdValue(RiskThresholdKindAbsolute, 1440); err != nil {
		t.Fatalf("旧签名的行为不该被改（它查不到 key，不加上界）：%v", err)
	}
	// 带 key 时同样的值要被拒。
	if err := ValidateRiskThresholdValueForKey(
		"risk_rule_offhours_activity_threshold", RiskThresholdKindInteger, 1440); err == nil {
		t.Fatalf("带 key 时 1440 天必须被拒（这正是审计实测的「静默失效」场景）")
	}
}

// ════════════════════════════════════════════════════════════════════
// ⑤ 规则清单一致性自检（t193 审计发现的 13 vs 14 问题）
//
// 后端有两张互相独立的清单：
//   - `entity.DefaultRiskRules()`：**判定链**用（缺一条 = 少一道闸）
//   - `riskThresholdSpecs()`：**配置界面**用（缺一条 = 运营看不到/调不了）
//
// 两者必须覆盖同一批 RuleID。少任何一边，都会表现为「规则在工作但调不了」
// 或「界面上有但判定不跑」，而且**都没有任何报错**。
//
// 这条用例的作用：以后新增规则忘了注册阈值（或反之），立刻红。
// ════════════════════════════════════════════════════════════════════
func TestRiskRuleAndThresholdSpecsCoverSameRuleIDs(t *testing.T) {
	// 显式例外：这些规则**不需要**可调阈值，写在这里而不是靠「碰巧没加」。
	//
	// 每一条都要有理由，否则以后有人新增规则时会照抄这里而不是想清楚。
	allowedWithoutThreshold := map[string]string{
		// 目前没有例外：10 条行为 + 3 条欠款规则全部有可调阈值。
		// 保留这个 map 是为了把「确实不需要阈值」与「忘了加」区分开 ——
		// 直接写空集合比写一个没有理由的例外表更安全。
	}

	ruleIDs := map[string]bool{}
	for _, rule := range entity.DefaultRiskRules() {
		ruleIDs[rule.RuleID] = true
	}
	if len(ruleIDs) == 0 {
		t.Fatalf("前置条件失效：DefaultRiskRules 返回空")
	}

	specRuleIDs := map[string]bool{}
	for _, spec := range riskThresholdSpecs() {
		specRuleIDs[spec.RuleID] = true
	}

	// 方向一：每条规则都必须至少有一个可调阈值。
	for ruleID := range ruleIDs {
		if _, exempt := allowedWithoutThreshold[ruleID]; exempt {
			continue
		}
		if !specRuleIDs[ruleID] {
			t.Errorf("规则 %q 在 entity.DefaultRiskRules() 里注册了，但 riskThresholdSpecs() "+
				"**没有**它的可调阈值 —— 运营在管理页上看不到也调不了这条规则，且不会有任何报错", ruleID)
		}
	}

	// 方向二：规格表里的 RuleID 必须都在规则清单里（否则界面上有一个不存在的规则）。
	for ruleID := range specRuleIDs {
		if !ruleIDs[ruleID] {
			t.Errorf("riskThresholdSpecs() 为 %q 注册了阈值，但 entity.DefaultRiskRules() "+
				"里**没有**这条规则 —— 管理页会显示一个永不触发的规则", ruleID)
		}
	}

	// 计数自检：让「13 vs 14」这类偏差在失败信息里直接可见。
	if len(ruleIDs) != len(specRuleIDs) {
		t.Errorf("规则数（%d）与规格覆盖的规则数（%d）不一致。规则清单：%v；规格清单：%v",
			len(ruleIDs), len(specRuleIDs), sortedKeys(ruleIDs), sortedKeys(specRuleIDs))
	}
}

// sortedKeys 让失败信息里的集合顺序稳定（便于对比）。
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// 判据⑥：每个规格的 MaxValue 必须是「有意义的上界」而不是随手填的数。
//
// 具体钉：上界必须 > 默认值（否则默认值就被拦了），且不能是 0
// （0 = 不设上界，只有理论上无上限的项才允许 —— 当前一个都没有）。
func TestRiskThresholdMaxValuesAreSane(t *testing.T) {
	for _, spec := range riskThresholdSpecs() {
		if spec.MaxValue <= 0 {
			t.Errorf("%s 没有设置 MaxValue —— 上界缺失会让「填 1440 天让规则静默失效」重演。\n"+
				"（若某项确实理论上无上限，请在本用例里显式加例外并写明理由）", spec.Key)
			continue
		}
		if spec.MaxValue <= spec.DefaultValue {
			t.Errorf("%s 的上界 %v 不大于默认值 %v —— 默认值本身会被拒，系统不可用",
				spec.Key, spec.MaxValue, spec.DefaultValue)
		}
	}
}

var _ = fmt.Sprintf
