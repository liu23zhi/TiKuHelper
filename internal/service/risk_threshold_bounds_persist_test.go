package service

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════════════════════
// 超上界的端到端行为：**拒绝且不落库**
//
// `ValidateRiskThresholdValueForKey` 的单元用例（risk_threshold_bounds_test.go）
// 只证明「返回了 error」。但任务书要求的是「超上界 → 400 且**不落库**」——
// 「返回值对了」和「数据库没被写脏」是两件事。
//
// ⚠️ 这条必须用真实的 `UpdateRiskThresholdWithDB` 走一遍：如果校验发生在
// 写库**之后**，错误仍会返回、状态码仍是 400，但库里已经躺着一个让规则
// 静默失效的值 —— 单元用例完全看不出来。
// ════════════════════════════════════════════════════════════════════

// readThresholdSetting 读 system_settings 里的原始值（空串 = 没写过）。
func readThresholdSetting(t *testing.T, db *gorm.DB, key string) string {
	t.Helper()
	var row entity.SystemSetting
	err := db.Where("key = ?", key).First(&row).Error
	if err != nil {
		return ""
	}
	return row.Value
}

// 判据：每类各取一条，超上界时 `UpdateRiskThresholdWithDB` 报错且**不写库**。
func TestUpdateRiskThresholdAboveMaxDoesNotPersist(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value float64
	}{
		{"计数类 R1", "risk_rule_multi_account_ip_threshold", 10_001},
		{"计数类 R2", "risk_rule_login_bruteforce_threshold", 10_001},
		{"天数类 R16", "risk_rule_offhours_activity_threshold", 1440},
		// ↑ 1440 正是 t193 审计实测「规则静默失效」的那个值
		{"占比类 R16", "risk_offhours_share_threshold", 1.5},
		{"速度类 R3", "risk_rule_impossible_travel_threshold", 40_001},
		{"倍数类 R12", "risk_rule_token_abuse_threshold", 1_001},
		{"金额类 debt_user", "risk_rule_debt_user_threshold", 1_000_001},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newRiskAlertTestDBNamed(t, "bound-"+tc.key)
			seedRiskRules(t, db)

			// 前置：这一项原本没有被显式配置（避免「本来就有值」混淆断言）。
			if before := readThresholdSetting(t, db, tc.key); before != "" {
				t.Fatalf("前置条件失效：%s 初始已有值 %q", tc.key, before)
			}

			_, err := UpdateRiskThresholdWithDB(db, tc.key, tc.value)
			if err == nil {
				t.Fatalf("%s 填 %v 超上界，必须报错（否则规则会静默失效）", tc.key, tc.value)
			}

			// 关键断言：**库里不能留下这个值**。
			if after := readThresholdSetting(t, db, tc.key); after != "" {
				t.Fatalf("%s 被拒绝后仍写入了 %q —— 校验发生在写库之后，库里已躺着一个让规则失效的值",
					tc.key, after)
			}

			// 再确认「生效值」没变（走三层兜底读回默认值）。
			items, itemErr := RiskThresholds(db)
			if itemErr != nil {
				t.Fatalf("读阈值列表: %v", itemErr)
			}
			for _, item := range items {
				if item.Key != tc.key {
					continue
				}
				if item.Configured {
					t.Fatalf("%s 被拒绝后 configured 仍为 true（显式配置标志被写脏）", tc.key)
				}
			}
		})
	}
}

// 判据：上界内的值必须**能写入**（防止改动把正常保存也弄坏）。
func TestUpdateRiskThresholdBelowMaxPersists(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "bound-ok")
	seedRiskRules(t, db)

	// 一个明显合理、且与默认值不同的值。
	const key = "risk_rule_multi_account_ip_threshold"
	const value = 42
	if _, err := UpdateRiskThresholdWithDB(db, key, value); err != nil {
		t.Fatalf("合理值 %v 必须能保存，实际报错: %v", value, err)
	}
	if got := readThresholdSetting(t, db, key); got == "" {
		t.Fatalf("合理值保存后库里应有记录，实际为空")
	}
	items, err := RiskThresholds(db)
	if err != nil {
		t.Fatalf("读阈值列表: %v", err)
	}
	found := false
	for _, item := range items {
		if item.Key != key {
			continue
		}
		found = true
		if item.Value != value {
			t.Fatalf("%s 生效值 = %v，应为 %v", key, item.Value, value)
		}
		if !item.Configured {
			t.Fatalf("%s 保存后 configured 应为 true", key)
		}
	}
	if !found {
		t.Fatalf("阈值列表里找不到 %s", key)
	}
}
