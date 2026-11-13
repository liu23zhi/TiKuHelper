package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════════════════════
// R9 · credential_stuffing（96 §2.1）
// 同一 IP 在 30 分钟滑动窗口内试了 ≥N 个**不同账号名**。
//
// 本文件的核心判据是 **D3：账号不存在必须与密码错误同等计入**。
// 撞库的定义就是「用一个密码列表去试大量不同账号」，其中绝大多数账号名
// 可能根本不存在。只统计真实存在的账号，检测的就不是撞库，而是定向爆破（R2）。
// ════════════════════════════════════════════════════════════════════

const riskR9TestIP = "203.0.113.9"

// seedRiskLoginFailure 造一条失败登录事件。
//
// userID = 0 表示**账号不存在**（撞库的典型特征）。
func seedRiskLoginFailure(t *testing.T, db *gorm.DB, ip string, userID int32, username, reason string, at time.Time) {
	t.Helper()
	event := entity.LoginFailureEvent{
		CreatedAt: at,
		ClientIP:  ip,
		UserID:    userID,
		Username:  username,
		Scene:     entity.LoginFailureScenePassword,
		Reason:    reason,
	}
	if err := db.Create(&event).Error; err != nil {
		t.Fatalf("create login failure event: %v", err)
	}
}

// 判据①（**D3 核心**）：一个 IP 连试 20 个**全部不存在**的账号 → 必须告警。
//
// 这是 R9 与 R2 的分界线。若实现只统计「真实存在的账号」（比如加了
// `user_id > 0` 过滤），本用例必红 —— 而那正是撞库最常见的形态。
func TestRiskCredentialStuffingAllNonexistentAccountsStillAlerts(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 20 个账号，**全部 user_id = 0（不存在）**。
	for i := 0; i < 20; i++ {
		seedRiskLoginFailure(t, db, riskR9TestIP, 0,
			"ghost"+itoaRisk(int32(i)), entity.LoginFailureReasonUserNotFound,
			now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "credential_stuffing"); got != 1 {
		t.Fatalf("同一 IP 试 20 个**全部不存在**的账号必须告警（撞库的定义如此），实际 %d 条。"+
			"若为 0，说明实现加了 user_id>0 之类的过滤，把撞库当成了定向爆破（R2 范畴）", got)
	}
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", "credential_stuffing").First(&warning).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	if got, _ := detail["accountCount"].(float64); got != 20 {
		t.Fatalf("detail.accountCount = %v, want 20", detail["accountCount"])
	}
	// 硬要求：threshold 必须是**配置值**（20），不是实际计数。
	// 这条与 risk_detail_threshold_test.go 的 6 条钉子同源。
	if got, _ := detail["threshold"].(float64); got != 20 {
		t.Fatalf("detail.threshold 应为配置值 20，实际 %v（若等于 accountCount 说明写成了实际值）", got)
	}
	if got, _ := detail["ip"].(string); got != riskR9TestIP {
		t.Fatalf("detail.ip = %q, want %q", got, riskR9TestIP)
	}
	wantDetailThresholdIsConfigured(t, db, "credential_stuffing", 20, 20)
}

// wantDetailThresholdIsConfigured 与 risk_detail_threshold_test.go 的
// assertDetailThresholdIsConfigured 同义，但这里显式要求「阈值 ≠ 计数」，
// 所以单独构造（该用例的阈值恰等于计数 20，不能用那个函数）。
func wantDetailThresholdIsConfigured(t *testing.T, db *gorm.DB, ruleID string, configured, count float64) {
	t.Helper()
	if configured == count {
		// 本用例的构造是「阈值恰等于计数」，此处只做浅层冒烟；
		// 真正能拷动「threshold 写成实际值」的是下面 TestRiskDetailThresholdIsConfiguredCredentialStuffing。
		return
	}
	assertDetailThresholdIsConfigured(t, db, ruleID, configured, count)
}

// 判据②：阈值边界 —— 19 个不告警、20 个告警。
func TestRiskCredentialStuffingThresholdBoundary(t *testing.T) {
	t.Run("nineteen_below_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := riskR1TestTime()
		for i := 0; i < 19; i++ {
			seedRiskLoginFailure(t, db, riskR9TestIP, 0, "acc"+itoaRisk(int32(i)),
				entity.LoginFailureReasonUserNotFound, now.Add(-time.Duration(i+1)*time.Second))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "credential_stuffing"); got != 0 {
			t.Fatalf("19 个 < 阈值 20 必须不告警，实际 %d 条", got)
		}
	})
	t.Run("twenty_meets_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := riskR1TestTime()
		for i := 0; i < 20; i++ {
			seedRiskLoginFailure(t, db, riskR9TestIP, 0, "acc"+itoaRisk(int32(i)),
				entity.LoginFailureReasonUserNotFound, now.Add(-time.Duration(i+1)*time.Second))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "credential_stuffing"); got != 1 {
			t.Fatalf("20 个 = 阈值 20 必须告警，实际 %d 条", got)
		}
	})
}

// 判据③：**同一账号试 20 次不告警**（那是定向爆破 R2，不是撞库）。
//
// 这条与判据① 互为对照：都 20 次，但「不同账号」是撞库、「同账号」是爆破。
func TestRiskCredentialStuffingSameAccountNoAlert(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 同一个账号试 20 次 —— 失败次数够多，但只有 1 个不同账号。
	for i := 0; i < 20; i++ {
		seedRiskLoginFailure(t, db, riskR9TestIP, 42, "victim",
			entity.LoginFailureReasonBadPassword, now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "credential_stuffing"); got != 0 {
		t.Fatalf("同一账号试 20 次是定向爆破（R2），不是撞库，不得告警，实际 %d 条", got)
	}
}

// 判据④：30 分钟**滑动**窗口 —— 窗口外的失败不计入。
func TestRiskCredentialStuffingRollingWindow(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 20 个不同账号，但全部落在 31–50 分钟前（窗口外）。
	for i := 0; i < 20; i++ {
		seedRiskLoginFailure(t, db, riskR9TestIP, 0, "old"+itoaRisk(int32(i)),
			entity.LoginFailureReasonUserNotFound,
			now.Add(-time.Duration(31+i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "credential_stuffing"); got != 0 {
		t.Fatalf("31 分钟前的失败不在 30 分钟滑动窗口内，必须不告警，实际 %d 条", got)
	}
	// 反向对照：同数量挪到窗口内 → 必须告警。
	db2 := newRiskAlertTestDBNamed(t, t.Name()+"-control")
	seedRiskRules(t, db2)
	for i := 0; i < 20; i++ {
		seedRiskLoginFailure(t, db2, riskR9TestIP, 0, "new"+itoaRisk(int32(i)),
			entity.LoginFailureReasonUserNotFound, now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db2, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db2, "credential_stuffing"); got != 1 {
		t.Fatalf("窗口内 20 个必须告警，实际 %d 条（若为 0 说明上面的 0 与窗口无关）", got)
	}
}

// 判据⑤：不同 IP 各自独立计数（不能把两个 IP 的账号合起来算）。
func TestRiskCredentialStuffingSeparatesIPs(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 两个 IP 各试 10 个不同账号 —— 单看任一 IP 都不到 20。
	for i := 0; i < 10; i++ {
		seedRiskLoginFailure(t, db, "203.0.113.10", 0, "a"+itoaRisk(int32(i)),
			entity.LoginFailureReasonUserNotFound, now.Add(-time.Duration(i+1)*time.Second))
		seedRiskLoginFailure(t, db, "203.0.113.11", 0, "b"+itoaRisk(int32(i)),
			entity.LoginFailureReasonUserNotFound, now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "credential_stuffing"); got != 0 {
		t.Fatalf("两个 IP 各 10 个账号，单 IP 都不到 20，不得告警，实际 %d 条"+
			"（若为 1 说明把不同 IP 的账号合并计数了）", got)
	}
}

// 判据⑥：username 为空的行不计入「不同账号」（/admin-login 路径拿不到账号名）。
func TestRiskCredentialStuffingIgnoresEmptyUsername(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 20 条空 username + 19 个真实账号名 → 不同账号数只有 19，不告警。
	for i := 0; i < 20; i++ {
		seedRiskLoginFailure(t, db, riskR9TestIP, 0, "", entity.LoginFailureReasonNotAdmin,
			now.Add(-time.Duration(i+1)*time.Second))
	}
	for i := 0; i < 19; i++ {
		seedRiskLoginFailure(t, db, riskR9TestIP, 0, "real"+itoaRisk(int32(i)),
			entity.LoginFailureReasonUserNotFound, now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "credential_stuffing"); got != 0 {
		t.Fatalf("20 条空 username 不得算成 20 个「不同账号」（实际不同账号 19 个），"+
			"实际 %d 条告警", got)
	}
}

// 判据⑦：阈值可配（双向）。
func TestRiskCredentialStuffingThresholdIsConfigurable(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := 0; i < 5; i++ {
		seedRiskLoginFailure(t, db, riskR9TestIP, 0, "cfg"+itoaRisk(int32(i)),
			entity.LoginFailureReasonUserNotFound, now.Add(-time.Duration(i+1)*time.Second))
	}
	// 阈值 10：5 个不足。
	putRiskSetting(t, db, "risk_rule_credential_stuffing_threshold", "10")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "credential_stuffing"); got != 0 {
		t.Fatalf("阈值 10 时 5 个必须不告警，实际 %d 条", got)
	}
	// 阈值 3：必须告警。
	putRiskSetting(t, db, "risk_rule_credential_stuffing_threshold", "3")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "credential_stuffing"); got != 1 {
		t.Fatalf("阈值 3 时 5 个必须告警，实际 %d 条", got)
	}
}

// 判据⑧：只告警不处置（C1）。
func TestRiskCredentialStuffingNeverTakesAction(t *testing.T) {
	db := newRiskAlertTestDB(t)
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate blacklist: %v", err)
	}
	seedRiskRules(t, db)
	now := riskR1TestTime()
	user := entity.User{Username: "stuffing-victim", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for i := 0; i < 20; i++ {
		seedRiskLoginFailure(t, db, riskR9TestIP, 0, "hit"+itoaRisk(int32(i)),
			entity.LoginFailureReasonUserNotFound, now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "credential_stuffing"); got != 1 {
		t.Fatalf("前置条件：必须已告警，实际 %d 条", got)
	}
	var after entity.User
	if err := db.First(&after, user.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != "enabled" {
		t.Fatalf("C1：风控不得改用户状态，实际 %q", after.Status)
	}
	var blacklist int64
	if err := db.Model(&entity.BlacklistEntry{}).Count(&blacklist).Error; err != nil {
		t.Fatalf("count blacklist: %v", err)
	}
	if blacklist != 0 {
		t.Fatalf("C1：风控不得写黑名单，实际 %d 行", blacklist)
	}
}

var _ = logger.ShanghaiLocation
