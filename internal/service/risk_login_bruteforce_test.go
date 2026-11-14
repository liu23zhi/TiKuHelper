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
// R2 · login_bruteforce（96 §2.1 L107）
//
// 与 R9（credential_stuffing）是**同一张表的两种聚合**，区别是**深度 vs 广度**：
//
//	R2 = 对同一个目标反复试（深度）—— 同一账号，或同一 IP 反复来
//	R9 = 从同一 IP 试很多不同账号（广度）
//
// 本文件的核心是**对称性**：
//
//	|        | 账号维度          | IP 维度      | 是否区分不同账号 |
//	| R2     | user_id > 0       | 全算（含 0） | 否（只看次数）   |
//	| R9     | —                 | 全算         | 是（≥N 个不同）  |
//
// 写反了就是「R2 写成 R9」。
// ════════════════════════════════════════════════════════════════════

const (
	riskR2TestIP      = "198.51.100.7"
	riskR2TestIPOther = "198.51.100.8"
)

// seedR2Failure 造一条带真实 user_id 的失败（账号维度用）。
func seedR2Failure(t *testing.T, db *gorm.DB, userID int32, username, ip string, at time.Time) {
	t.Helper()
	if err := db.Create(&entity.LoginFailureEvent{
		CreatedAt: at, ClientIP: ip, UserID: userID, Username: username,
		Scene: entity.LoginFailureScenePassword, Reason: entity.LoginFailureReasonBadPassword,
	}).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
}

// 判据①：账号维度 —— 同一真实账号失败 10 次 → 告警。
func TestRiskLoginBruteforceAccountDimension(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	user := entity.User{Username: "brute-target", Password: "x", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for i := 0; i < 10; i++ {
		seedR2Failure(t, db, user.ID, "brute-target", riskR2TestIP, now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// 账号维度 1 条；IP 维度也会命中（同一 IP 失败 10 次，且只涉及 1 个账号，
	// 不构成撞库形态）→ 共 2 条。
	if got := countRuleWarnings(t, db, "login_bruteforce"); got != 2 {
		t.Fatalf("同账号 + 同 IP 各失败 10 次 → 账号维度与 IP 维度各 1 条，共 2 条，实际 %d 条", got)
	}
	var accountWarnings int64
	db.Model(&entity.RiskWarning{}).Where("rule_id = ? AND subject_key LIKE ?",
		"login_bruteforce", "%:account:%").Count(&accountWarnings)
	if accountWarnings != 1 {
		t.Fatalf("账号维度应有 1 条，实际 %d", accountWarnings)
	}
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ? AND subject_key LIKE ?", "login_bruteforce", "%:account:%").
		First(&warning).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	if got, _ := detail["failCount"].(float64); got != 10 {
		t.Fatalf("detail.failCount = %v, want 10", detail["failCount"])
	}
	// 硬要求：threshold 必须是配置值（10），不是实际计数。
	if got, _ := detail["threshold"].(float64); got != 10 {
		t.Fatalf("detail.threshold 应为配置值 10，实际 %v", got)
	}
	if got, _ := detail["dimension"].(string); got != "account" {
		t.Fatalf("detail.dimension = %q, want account", got)
	}
}

// 判据②（**对称性核心**）：`user_id = 0` 的行**不进账号维度**。
//
// 不存在的账号没有「账号」可爆破，那些行属于 R9 的撞库范畴。
// 若不排除，撞库会被重复算成「对 0 号账号的爆破」。
//
// 变异方向：账号维度去掉 `row.UserID > 0` 判断 → 本用例必红。
func TestRiskLoginBruteforceIgnoresNonexistentAccounts(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 20 条全部 user_id = 0（账号不存在），用户名各不相同 → 这是撞库形态。
	for i := 0; i < 20; i++ {
		seedR2Failure(t, db, 0, "ghost"+itoaRisk(int32(i)), riskR2TestIP,
			now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// 账号维度必须 0 条（没有真实账号可爆破）。
	var accountWarnings int64
	db.Model(&entity.RiskWarning{}).Where("rule_id = ? AND subject_key LIKE ?",
		"login_bruteforce", "%:account:%").Count(&accountWarnings)
	if accountWarnings != 0 {
		t.Fatalf("user_id=0 的行不得进入账号维度（不存在的账号无法被爆破），实际 %d 条", accountWarnings)
	}
	// 同时确认 R9 正常告警（证明这批数据是「撞库」而非「没人管」）。
	if got := countRuleWarnings(t, db, "credential_stuffing"); got != 1 {
		t.Fatalf("前置条件：这批数据应触发 R9 撞库告警，实际 %d 条", got)
	}
	// R2 的 IP 维度也必须跳过（撞库形态交给 R9）。
	var ipWarnings int64
	db.Model(&entity.RiskWarning{}).Where("rule_id = ? AND subject_key LIKE ?",
		"login_bruteforce", "%:ip:%").Count(&ipWarnings)
	if ipWarnings != 0 {
		t.Fatalf("撞库形态的 IP 应由 R9 报，R2 的 IP 维度不得重复报，实际 %d 条", ipWarnings)
	}
}

// 判据③：**不同账号从同一 IP 试 → 不触发 R2**（那是 R9 的活）。
//
// 这是「互不误报」的核心判据。
func TestRiskLoginBruteforceDoesNotFireOnStuffing(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 造 20 个真实账号，同一 IP 各试 1 次 → 每个账号只失败 1 次（远低于 R2 阈值 10）。
	// 但 IP 维度失败 20 次 ≥ 10，且涉及 20 个不同账号 → 撞库形态，应交给 R9。
	for i := 0; i < 20; i++ {
		user := entity.User{Username: "many" + itoaRisk(int32(i)), Password: "x",
			Status: "enabled", Role: "user"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		seedR2Failure(t, db, user.ID, user.Username, riskR2TestIP,
			now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "login_bruteforce"); got != 0 {
		var details []entity.RiskWarning
		db.Where("rule_id = ?", "login_bruteforce").Find(&details)
		for _, d := range details {
			t.Logf("误报详情: subject=%s detail=%s", d.SubjectKey, d.Detail)
		}
		t.Fatalf("20 个不同账号各试 1 次（撞库形态）不该触发 R2，实际 %d 条。"+
			"账号维度：每个账号只 1 次失败；IP 维度：涉及 20 个不同账号 ≥ R9 阈值，应交给 R9", got)
	}
	// R9 必须告警（证明这批数据确实被覆盖了，不是没人管）。
	if got := countRuleWarnings(t, db, "credential_stuffing"); got != 1 {
		t.Fatalf("这批数据应触发 R9，实际 %d 条（若为 0 说明两条规则都没覆盖，是漏报）", got)
	}
}

// 判据④：同一账号从**多个 IP** 试 → 也要告警（IP 轮换是绕过限流的常见手法）。
func TestRiskLoginBruteforceMultiIPStillAlerts(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	user := entity.User{Username: "rotating-target", Password: "x", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	// 10 次失败，每次换一个 IP（每个 IP 只出现 1 次，所以 IP 维度不会命中）。
	for i := 0; i < 10; i++ {
		ip := "203.0.113." + itoaRisk(int32(100+i))
		seedR2Failure(t, db, user.ID, user.Username, ip, now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	var accountWarnings int64
	db.Model(&entity.RiskWarning{}).Where("rule_id = ? AND subject_key LIKE ?",
		"login_bruteforce", "%:account:%").Count(&accountWarnings)
	if accountWarnings != 1 {
		t.Fatalf("同一账号从 10 个不同 IP 各试 1 次，账号维度必须告警（IP 轮换不能绕过 R2），"+
			"实际 %d 条", accountWarnings)
	}
	// IP 维度不该命中（每个 IP 只 1 次失败）。
	var ipWarnings int64
	db.Model(&entity.RiskWarning{}).Where("rule_id = ? AND subject_key LIKE ?",
		"login_bruteforce", "%:ip:%").Count(&ipWarnings)
	if ipWarnings != 0 {
		t.Fatalf("每个 IP 只失败 1 次，IP 维度不该命中，实际 %d 条", ipWarnings)
	}
}

// 判据⑤：时间窗外的失败不计入（10 分钟滑动窗口）。
func TestRiskLoginBruteforceRollingWindow(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	user := entity.User{Username: "window-target", Password: "x", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	// 10 次失败全部落在 11–20 分钟前（窗口外）。
	for i := 0; i < 10; i++ {
		seedR2Failure(t, db, user.ID, user.Username, riskR2TestIP,
			now.Add(-time.Duration(11+i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "login_bruteforce"); got != 0 {
		t.Fatalf("11 分钟前的失败不在 10 分钟窗口内，不得告警，实际 %d 条", got)
	}
	// 反向对照：同数量挪到窗口内 → 必须告警。
	db2 := newRiskAlertTestDBNamed(t, t.Name()+"-control")
	seedRiskRules(t, db2)
	user2 := entity.User{Username: "window-target-2", Password: "x", Status: "enabled", Role: "user"}
	if err := db2.Create(&user2).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for i := 0; i < 10; i++ {
		seedR2Failure(t, db2, user2.ID, user2.Username, riskR2TestIP,
			now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db2, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db2, "login_bruteforce"); got == 0 {
		t.Fatalf("窗口内 10 次必须告警，实际 0 条（若为 0 说明上面的 0 与窗口无关）")
	}
}

// 判据⑥：阈值边界 —— 9 次不告警（账号维度），10 次告警。
func TestRiskLoginBruteforceThresholdBoundary(t *testing.T) {
	t.Run("nine_below_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := riskR1TestTime()
		user := entity.User{Username: "nine", Password: "x", Status: "enabled", Role: "user"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatalf("create user: %v", err)
		}
		for i := 0; i < 9; i++ {
			// 每次换 IP，避免 IP 维度干扰（每个 IP 只 1 次）。
			seedR2Failure(t, db, user.ID, user.Username,
				"203.0.113."+itoaRisk(int32(200+i)), now.Add(-time.Duration(i+1)*time.Second))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "login_bruteforce"); got != 0 {
			t.Fatalf("9 次 < 阈值 10 必须不告警，实际 %d 条", got)
		}
	})
}

// 判据⑦：**所有 reason 同等计入**（含 captcha_failed / account_disabled）。
//
// 理由：排除任一类都会产生绕过面 —— 攻击者只要「故意错验证码」或「先触发禁用」
// 就能把失败次数洗白。
func TestRiskLoginBruteforceCountsAllReasons(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	user := entity.User{Username: "reasons", Password: "x", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	// 10 次失败，reason 全部是 captcha_failed / account_disabled 混合。
	reasons := []string{entity.LoginFailureReasonCaptchaFailed, entity.LoginFailureReasonAccountDisabled}
	for i := 0; i < 10; i++ {
		if err := db.Create(&entity.LoginFailureEvent{
			CreatedAt: now.Add(-time.Duration(i+1) * time.Second),
			ClientIP:  riskR2TestIP, UserID: user.ID, Username: user.Username,
			Reason: reasons[i%len(reasons)],
		}).Error; err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	var accountWarnings int64
	db.Model(&entity.RiskWarning{}).Where("rule_id = ? AND subject_key LIKE ?",
		"login_bruteforce", "%:account:%").Count(&accountWarnings)
	if accountWarnings != 1 {
		t.Fatalf("captcha_failed 与 account_disabled 必须同等计入（排除它们会产生绕过面），"+
			"实际账号维度 %d 条", accountWarnings)
	}
}

// 判据⑧：阈值可配（双向）。
func TestRiskLoginBruteforceThresholdIsConfigurable(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	user := entity.User{Username: "cfg", Password: "x", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for i := 0; i < 5; i++ {
		seedR2Failure(t, db, user.ID, user.Username,
			"203.0.113."+itoaRisk(int32(220+i)), now.Add(-time.Duration(i+1)*time.Second))
	}
	// 阈值 8：5 次不足。
	putRiskSetting(t, db, "risk_rule_login_bruteforce_threshold", "8")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "login_bruteforce"); got != 0 {
		t.Fatalf("阈值 8 时 5 次必须不告警，实际 %d 条", got)
	}
	// 阈值 3：必须告警。
	putRiskSetting(t, db, "risk_rule_login_bruteforce_threshold", "3")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "login_bruteforce"); got == 0 {
		t.Fatalf("阈值 3 时 5 次必须告警，实际 0 条")
	}
}

// 判据⑨：只告警不处置（C1）。
func TestRiskLoginBruteforceNeverTakesAction(t *testing.T) {
	db := newRiskAlertTestDB(t)
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate blacklist: %v", err)
	}
	seedRiskRules(t, db)
	now := riskR1TestTime()
	user := entity.User{Username: "victim", Password: "x", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for i := 0; i < 10; i++ {
		seedR2Failure(t, db, user.ID, user.Username, riskR2TestIP, now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "login_bruteforce"); got == 0 {
		t.Fatalf("前置条件：必须已告警，实际 0 条")
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
		t.Fatalf("count: %v", err)
	}
	if blacklist != 0 {
		t.Fatalf("C1：风控不得写黑名单，实际 %d 行", blacklist)
	}
}

// 判据⑩：`detail.threshold` 是配置值不是实际计数（阈值 5 / 实际 8）。
func TestRiskDetailThresholdIsConfiguredLoginBruteforce(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	const configured = 5.0
	const count = 8
	putRiskSetting(t, db, "risk_rule_login_bruteforce_threshold", "5")
	user := entity.User{Username: "detail-cfg", Password: "x", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for i := 0; i < count; i++ {
		seedR2Failure(t, db, user.ID, user.Username,
			"203.0.113."+itoaRisk(int32(240+i)), now.Add(-time.Duration(i+1)*time.Second))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	var accountWarnings int64
	db.Model(&entity.RiskWarning{}).Where("rule_id = ? AND subject_key LIKE ?",
		"login_bruteforce", "%:account:%").Count(&accountWarnings)
	if accountWarnings != 1 {
		t.Fatalf("前置条件：账号维度必须先告警，实际 %d 条", accountWarnings)
	}
	// 用带 LIKE 的详情取出账号维度那条，再断言 threshold。
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ? AND subject_key LIKE ?",
		"login_bruteforce", "%:account:%").First(&warning).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	got, ok := detail["threshold"].(float64)
	if !ok {
		t.Fatalf("detail 缺少 threshold: %v", detail)
	}
	if got != configured {
		t.Fatalf("detail.threshold 应为配置值 %v，实际 %v（若等于 failCount=%v 说明写成了实际计数）",
			configured, got, count)
	}
	if configured == float64(count) {
		t.Fatalf("构造失效：阈值与计数相等，拷不动「threshold 写成计数」的缺陷")
	}
}

var _ = logger.ShanghaiLocation
