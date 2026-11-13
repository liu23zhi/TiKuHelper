package service

import (
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 本文件钉住 R12 配额判据的**周期口径**。
//
// # 缺陷形态
//
// 本仓的额度周期是叠加层设计（entity/access_token_quota_epoch.go 的注释把它列为
// 「本设计的红线」）：
//
//	本轮已用 = total_amount_used - baseline_amount（当前 epoch 的基线）
//	total_amount_used 永不清零（账单/对账/审计都以它为据）
//
// R12 的第一版直接读 `total_amount_used` 原始列，完全没看 epoch baseline。后果：
// 管理员点「重置额度周期」（POST /access-tokens/:id/reset-quota-window）之后，
// 该 token 的「本轮已用」在前端显示 0，但**风控照样按历史累计达到 3 倍继续每天告警，
// 且用户没有任何操作能消除**（累计只增不减）。
//
// 这是**永久误报**，不是漏报 —— 比漏报更糟，因为它会持续骚扰管理员并消耗工时。
//
// 实测（修前）：quota=100 / used=300 / baseline=300 → 正确 windowUsed=0，R12 仍命中 1 条。

// TestRiskTokenAbuseIgnoresHistoryAfterQuotaReset 是核心判据。
func TestRiskTokenAbuseIgnoresHistoryAfterQuotaReset(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	if err := db.AutoMigrate(&entity.AccessTokenQuotaEpoch{}); err != nil {
		t.Fatalf("migrate epoch: %v", err)
	}

	user := entity.User{Username: "abuse-epoch", Password: "x", Role: "user",
		Status: "enabled", RegisterSource: "system"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	tok := entity.AccessToken{
		UserID: user.ID, Name: "epoch-token", TokenHash: "h-epoch", TokenValue: "v-epoch",
		Status: "enabled", TotalQuotaLimit: 100, TotalAmountUsed: 300,
	}
	if err := db.Create(&tok).Error; err != nil {
		t.Fatal(err)
	}

	now := riskR1TestTime()
	rule := riskRuleNamed(t, db, entity.RiskRuleIDTokenAbuse)

	// 重置前：历史累计 300 / 配额 100 = 3.0 ≥ 阈值 3 → 必须告警。
	// 这是**反向对照**：防「改完之后规则压根不跑」也能让本用例通过。
	before, err := collectTokenAbuseCandidates(db, rule, now)
	if err != nil {
		t.Fatalf("before reset: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("重置前 quota=100/used=300 必须命中 1 条（ratio=3.0），实际 %d 条", len(before))
	}

	// 追加 epoch：baseline=300 ⇒ 本轮已用 = 300-300 = 0。
	if err := db.Create(&entity.AccessTokenQuotaEpoch{
		TokenID: tok.ID, Epoch: 1, BaselineAmount: 300, ResetAt: now,
	}).Error; err != nil {
		t.Fatalf("insert epoch: %v", err)
	}

	after, err := collectTokenAbuseCandidates(db, rule, now)
	if err != nil {
		t.Fatalf("after reset: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("重置额度周期后本轮已用=0，不该再告警，实际 %d 条。"+
			"这是「永久误报」形态：用户无论怎么操作都无法消除（累计只增不减）", len(after))
	}
}

// TestRiskTokenAbuseUsesWindowNotHistory 是中间态判据：
// 历史累计超阈值、但本轮只花了一点 ⇒ 不该告警。
//
// 与上一条互补：上一条测「归零」，这一条测「部分消耗」。只测归零的话，
// 「把 windowUsed 恒设为 0」也能通过。
func TestRiskTokenAbuseUsesWindowNotHistory(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	if err := db.AutoMigrate(&entity.AccessTokenQuotaEpoch{}); err != nil {
		t.Fatalf("migrate epoch: %v", err)
	}

	user := entity.User{Username: "abuse-partial", Password: "x", Role: "user",
		Status: "enabled", RegisterSource: "system"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	// 历史累计 300（超 3 倍），但 baseline=290 ⇒ 本轮只用了 10 / 100 = 0.1。
	tok := entity.AccessToken{
		UserID: user.ID, Name: "partial-token", TokenHash: "h-partial", TokenValue: "v-partial",
		Status: "enabled", TotalQuotaLimit: 100, TotalAmountUsed: 300,
	}
	if err := db.Create(&tok).Error; err != nil {
		t.Fatal(err)
	}
	now := riskR1TestTime()
	if err := db.Create(&entity.AccessTokenQuotaEpoch{
		TokenID: tok.ID, Epoch: 1, BaselineAmount: 290, ResetAt: now,
	}).Error; err != nil {
		t.Fatalf("insert epoch: %v", err)
	}

	rule := riskRuleNamed(t, db, entity.RiskRuleIDTokenAbuse)
	candidates, err := collectTokenAbuseCandidates(db, rule, now)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("本轮只用了 10/100=0.1 远低于阈值 3，不该告警（历史累计 300 不算），实际 %d 条",
			len(candidates))
	}
}
