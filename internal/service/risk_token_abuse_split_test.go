package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════════════════════
// R12 token_abuse 的**两个独立阈值**（t193 审计的建议 3，t195 拆键）
//
// 拆键前：一个键同时当「配额倍数」和「多 IP 个数」用。
// 拆键后：
//
//	risk_rule_token_abuse_threshold      配额倍数，默认 3（97 号 L321）
//	risk_token_abuse_multi_ip_threshold  不同 IP 数，默认 5（**本仓自定**）
//
// 本文件的核心判据是 **互不干扰的双向对照**：
// 改 A 不能影响 B 的行为，改 B 不能影响 A 的行为。
// 只测单向会漏掉「两个维度都读同一个变量」这种实现错误的一半。
// ════════════════════════════════════════════════════════════════════

// seedTokenAbuseQuota 造一个「配额消耗超阈值」的场景（不产生多 IP）。
//
// quotaLimit = 100、已用 used → 倍数 = used/100。
// 只用一个 IP，所以多 IP 判据不会命中（不同 IP 数 = 1）。
func seedTokenAbuseQuota(t *testing.T, db *gorm.DB, used float64) {
	t.Helper()
	token := seedRiskAccessToken(t, db, 1, "token-quota", 100, used, "203.0.113.1")
	now := riskR1TestTime()
	seedRiskBillingRow(t, db, 1, token.ID, "203.0.113.1", used, now.Add(-time.Minute))
}

// seedTokenAbuseMultiIP 造一个「多 IP 漂移」的场景（配额不超）。
//
// 配额 1000、每行花 1 → 配额倍数约 0.00x，远低于任何阈值；
// 只命中多 IP 判据。
func seedTokenAbuseMultiIP(t *testing.T, db *gorm.DB, ips []string) {
	t.Helper()
	token := seedRiskAccessToken(t, db, 1, "token-multiip", 1000, float64(len(ips)), ips[0])
	now := riskR1TestTime()
	for i, ip := range ips {
		seedRiskBillingRow(t, db, 1, token.ID, ip, 1, now.Add(-time.Duration(i)*time.Minute))
	}
}

// countTokenAbuseSignal 数某条 signal 的告警数（quota_ratio / multi_ip_drift）。
func countTokenAbuseSignal(t *testing.T, db *gorm.DB, signal string) int {
	t.Helper()
	var warnings []entity.RiskWarning
	if err := db.Where("rule_id = ?", "token_abuse").Find(&warnings).Error; err != nil {
		t.Fatalf("load warnings: %v", err)
	}
	count := 0
	for _, warning := range warnings {
		var detail map[string]any
		if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
			t.Fatalf("detail JSON: %v", err)
		}
		if got, _ := detail["signal"].(string); got == signal {
			count++
		}
	}
	return count
}

// 判据①：**改配额倍数键，多 IP 维度行为不变**（反向对照 A→B）。
func TestTokenAbuseQuotaKeyDoesNotAffectMultiIP(t *testing.T) {
	cases := []struct {
		name          string
		quotaSetting  string
		wantMultiIP   int
		wantQuotaHits int
	}{
		// 配额键设成 1（极严）：配额维度会命中，但 4 个 IP < 默认 5 → 多 IP 不命中。
		{"quota_tight_ip_unaffected", "1", 0, 1},
		// 配额键设成 1000（极宽）：两个维度都不命中。
		{"quota_loose_ip_unaffected", "1000", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newRiskAlertTestDBNamed(t, "tok-q-"+tc.quotaSetting)
			seedRiskRules(t, db)
			putRiskSetting(t, db, "risk_rule_token_abuse_threshold", tc.quotaSetting)
			// 配额：100 配额用了 500 → 5 倍。
			seedTokenAbuseQuota(t, db, 500)
			// 多 IP：4 个（< 默认 5）。
			seedTokenAbuseMultiIP(t, db, []string{
				"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4",
			})

			if _, err := EvaluateDebtAlerts(db, riskR1TestTime()); err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if got := countTokenAbuseSignal(t, db, "multi_ip_drift"); got != tc.wantMultiIP {
				t.Fatalf("配额键改成 %q 后，多 IP 维度命中数 = %d, want %d。"+
					"多 IP 维度必须只受自己的键控制（4 个 IP 恒 < 默认 5）",
					tc.quotaSetting, got, tc.wantMultiIP)
			}
			if got := countTokenAbuseSignal(t, db, "quota_ratio"); got != tc.wantQuotaHits {
				t.Fatalf("配额键 = %q 时配额维度命中数 = %d, want %d",
					tc.quotaSetting, got, tc.wantQuotaHits)
			}
		})
	}
}

// 判据②：**改多 IP 键，配额维度行为不变**（反向对照 B→A）。
func TestTokenAbuseMultiIPKeyDoesNotAffectQuota(t *testing.T) {
	cases := []struct {
		name          string
		ipSetting     string
		wantMultiIP   int
		wantQuotaHits int
	}{
		// 多 IP 键设成 2：4 个 IP 命中多 IP；配额 5 倍 > 默认 3 也命中。
		{"ip_tight_quota_unaffected", "2", 1, 1},
		// 多 IP 键设成 100：多 IP 不命中；配额仍然命中（不受影响）。
		{"ip_loose_quota_unaffected", "100", 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newRiskAlertTestDBNamed(t, "tok-ip-"+tc.ipSetting)
			seedRiskRules(t, db)
			putRiskSetting(t, db, RiskThresholdSettingKeyTokenMultiIP, tc.ipSetting)
			// 配额：100 配额用了 500 → 5 倍（> 默认 3）。
			seedTokenAbuseQuota(t, db, 500)
			// 多 IP：4 个。
			seedTokenAbuseMultiIP(t, db, []string{
				"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4",
			})

			if _, err := EvaluateDebtAlerts(db, riskR1TestTime()); err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if got := countTokenAbuseSignal(t, db, "quota_ratio"); got != tc.wantQuotaHits {
				t.Fatalf("多 IP 键改成 %q 后，配额维度命中数 = %d, want %d。"+
					"配额维度必须只受 risk_rule_token_abuse_threshold 控制",
					tc.ipSetting, got, tc.wantQuotaHits)
			}
			if got := countTokenAbuseSignal(t, db, "multi_ip_drift"); got != tc.wantMultiIP {
				t.Fatalf("多 IP 键 = %q 时多 IP 维度命中数 = %d, want %d",
					tc.ipSetting, got, tc.wantMultiIP)
			}
		})
	}
}

// 判据③（**核心**）：两个键同时设置，各自独立生效。
//
// 这是「互不干扰」最强的一条：一次评估里两个维度都命中，
// 且各自的 detail.threshold 记录的是**自己那个键**的值。
func TestTokenAbuseTwoThresholdsWorkIndependently(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "tok-both")
	seedRiskRules(t, db)
	// 配额倍数 4、多 IP 3 —— 两个值刻意不同，混用会被断言抓到。
	putRiskSetting(t, db, "risk_rule_token_abuse_threshold", "4")
	putRiskSetting(t, db, RiskThresholdSettingKeyTokenMultiIP, "3")

	// 场景 A：配额 5 倍（> 4 命中），只 1 个 IP（多 IP 不命中）。
	seedTokenAbuseQuota(t, db, 500)
	// 场景 B：3 个 IP（= 3 命中），配额不超（1000 配额用 3）。
	// 用不同 token id 区分（seedRiskAccessToken 的第一个参数是 userID，token 由它生成）。
	now := riskR1TestTime()
	tokenB := seedRiskAccessToken(t, db, 2, "token-b", 1000, 3, "198.51.100.1")
	for i, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		seedRiskBillingRow(t, db, 2, tokenB.ID, ip, 1, now.Add(-time.Duration(i)*time.Minute))
	}

	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countTokenAbuseSignal(t, db, "quota_ratio"); got != 1 {
		t.Fatalf("配额 5 倍 > 阈值 4 应命中 1 条，实际 %d", got)
	}
	if got := countTokenAbuseSignal(t, db, "multi_ip_drift"); got != 1 {
		t.Fatalf("3 个 IP = 阈值 3 应命中 1 条，实际 %d", got)
	}

	// 关键：两条告警的 detail.threshold 必须记录**各自的键值**。
	var warnings []entity.RiskWarning
	if err := db.Where("rule_id = ?", "token_abuse").Find(&warnings).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	seen := map[string]float64{}
	for _, warning := range warnings {
		var detail map[string]any
		if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
			t.Fatalf("detail JSON: %v", err)
		}
		signal, _ := detail["signal"].(string)
		threshold, _ := detail["threshold"].(float64)
		seen[signal] = threshold
	}
	if got := seen["quota_ratio"]; got != 4 {
		t.Fatalf("quota_ratio 的 detail.threshold = %v, want 4（配额倍数键）", got)
	}
	if got := seen["multi_ip_drift"]; got != 3 {
		t.Fatalf("multi_ip_drift 的 detail.threshold = %v, want 3（多 IP 键）。"+
			"若等于 4，说明 IP 维度读的还是配额倍数键", got)
	}
}

// 判据④：默认值 —— 配额倍数 3（97 号），多 IP 5（本仓自定）。
func TestTokenAbuseThresholdDefaults(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "tok-defaults")
	seedRiskRules(t, db)

	// 规格表里的默认值。
	var quotaDefault, ipDefault float64
	for _, spec := range riskThresholdSpecs() {
		switch spec.Key {
		case "risk_rule_token_abuse_threshold":
			quotaDefault = spec.DefaultValue
		case RiskThresholdSettingKeyTokenMultiIP:
			ipDefault = spec.DefaultValue
		}
	}
	if quotaDefault != 3 {
		t.Fatalf("配额倍数默认值 = %v, want 3（97 号 L321 原文）", quotaDefault)
	}
	if ipDefault != 5 {
		t.Fatalf("多 IP 数默认值 = %v, want 5（本仓自定，97 号未给数量）", ipDefault)
	}

	// 读取器的兜底值必须与规格表一致（两处漂移会静默改变判定）。
	if got := riskTokenMultiIPThreshold(db); got != ipDefault {
		t.Fatalf("riskTokenMultiIPThreshold 兜底 = %v, 与规格表 %v 不一致", got, ipDefault)
	}
}

// 判据⑤：`SyncRuleColumn` —— 只有配额倍数同步 risk_rules.threshold。
//
// `risk_rules.threshold` 只有一列，两个键都 true 会互相覆盖
// （R16 的占比项污染连续天数就是同款）。这条用例钉住「谁同步」。
func TestTokenAbuseOnlyQuotaSyncsRuleColumn(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "tok-sync")
	seedRiskRules(t, db)

	// 先写多 IP 键（它不该同步列）。
	if _, err := UpdateRiskThresholdWithDB(db, RiskThresholdSettingKeyTokenMultiIP, 7); err != nil {
		t.Fatalf("写多 IP 键: %v", err)
	}
	var rule entity.RiskRule
	if err := db.Where("rule_id = ?", "token_abuse").First(&rule).Error; err != nil {
		t.Fatalf("load rule: %v", err)
	}
	afterMultiIP := rule.Threshold
	// 多 IP 键不该动列：列应仍是 seed 的默认 3。
	if afterMultiIP != 3 {
		t.Fatalf("写多 IP 键后 risk_rules.threshold = %v，want 3。"+
			"多 IP 键不该同步这一列（它只承载主阈值=配额倍数）", afterMultiIP)
	}

	// 再写配额键（它该同步列）。
	if _, err := UpdateRiskThresholdWithDB(db, "risk_rule_token_abuse_threshold", 8); err != nil {
		t.Fatalf("写配额键: %v", err)
	}
	if err := db.Where("rule_id = ?", "token_abuse").First(&rule).Error; err != nil {
		t.Fatalf("reload rule: %v", err)
	}
	if rule.Threshold != 8 {
		t.Fatalf("写配额键后 risk_rules.threshold = %v, want 8（配额倍数是主判据，应同步）",
			rule.Threshold)
	}

	// 反过来：写配额键之后再写多 IP 键，列必须**保持**配额值不变。
	if _, err := UpdateRiskThresholdWithDB(db, RiskThresholdSettingKeyTokenMultiIP, 9); err != nil {
		t.Fatalf("再写多 IP 键: %v", err)
	}
	if err := db.Where("rule_id = ?", "token_abuse").First(&rule).Error; err != nil {
		t.Fatalf("reload rule: %v", err)
	}
	if rule.Threshold != 8 {
		t.Fatalf("多 IP 键把 risk_rules.threshold 覆盖成了 %v（应为 8）。"+
			"这正是 R16 占比项污染连续天数同款缺陷", rule.Threshold)
	}
}

// 判据⑥：两个键的上界各自独立（配额倍数可到 1000，多 IP 只到 100）。
func TestTokenAbuseThresholdUpperBounds(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value float64
		want  bool // true = 应被拒
	}{
		{"quota_1001_rejected", "risk_rule_token_abuse_threshold", 1001, true},
		{"quota_1000_ok", "risk_rule_token_abuse_threshold", 1000, false},
		{"multi_ip_101_rejected", RiskThresholdSettingKeyTokenMultiIP, 101, true},
		{"multi_ip_100_ok", RiskThresholdSettingKeyTokenMultiIP, 100, false},
		// 关键：多 IP 的上界**不该**放宽到配额的量级。
		{"multi_ip_500_rejected", RiskThresholdSettingKeyTokenMultiIP, 500, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, ok := riskThresholdSpecByKey(tc.key)
			if !ok {
				t.Fatalf("规格表里查不到 %s", tc.key)
			}
			err := ValidateRiskThresholdValueForKey(tc.key, spec.Kind, tc.value)
			if tc.want && err == nil {
				t.Fatalf("%s 填 %v 应被拒（上界 %v）", tc.key, tc.value, spec.MaxValue)
			}
			if !tc.want && err != nil {
				t.Fatalf("%s 填 %v 不该被拒：%v", tc.key, tc.value, err)
			}
		})
	}
}

// 判据⑦：label 与 unit 不能再是「倍/个」这种双义写法。
func TestTokenAbuseThresholdLabelsAreUnambiguous(t *testing.T) {
	for _, spec := range riskThresholdSpecs() {
		if spec.RuleID != entity.RiskRuleIDTokenAbuse {
			continue
		}
		if spec.Unit == "倍/个" {
			t.Fatalf("%s 的 unit 仍是「倍/个」（双义）——拆键后每个键必须有单一单位", spec.Key)
		}
		switch spec.Key {
		case "risk_rule_token_abuse_threshold":
			if spec.Unit != "倍" {
				t.Fatalf("配额倍数键的 unit = %q, want 倍", spec.Unit)
			}
		case RiskThresholdSettingKeyTokenMultiIP:
			if spec.Unit != "个" {
				t.Fatalf("多 IP 键的 unit = %q, want 个", spec.Unit)
			}
		}
	}
}
