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
// 全局判据：**detail.threshold 必须是「配置的阈值」，不能是「实际计数」**
//
// # 为什么需要这个文件
//
// t189 里 R18 的实现把 `"threshold": threshold` 误写成 `"threshold": group.UserCount`。
// 当时没有任何用例变红 —— 因为所有用例都构造了「阈值恰好等于实际计数」
// （阈值 3 / 3 个账号、阈值 5 / 5 张工单 …），两个数字相同，写错也看不出。
//
// 这个错误本身不难查（排障时看到 threshold 恰好等于 count 会误以为「刚好卡线」），
// 但它揭示了一类**普遍的测试盲区**：只要构造里 `阈值 == 实际值`，
// 「threshold 写成实际值」这个缺陷就恒定不可见。
//
// 复核其他规则后确认：R8 / R11 / R12 / R14 都存在同样的盲区
// （变异把 threshold 改成实际计数后，各自全套用例仍全绿）。
//
// # 本文件的判据形态
//
// 每条规则都构造成 **阈值 < 实际计数**（例如阈值 2、实际 5），然后断言
// `detail.threshold` 等于**配置值**而不是实际值。这是唯一能拷动该缺陷的构造。
//
// 与各规则自己的 "ThresholdIsConfigurable" 用例不重复：那些用例验证
// 「改了阈值行为跟着变」（判据正确性），本文件验证「证据里记录的是阈值而非计数」
// （证据可信性）。两者都必要 —— 前者证明规则会响，后者证明排障时看到的是真信息。
// ════════════════════════════════════════════════════════════════════

// assertDetailThresholdIsConfigured 断言告警 Detail 里的 threshold 是配置值。
//
// 参数 configured 是**我们显式设置的阈值**，count 是本次构造的实际值。
// 函数内部先自检「两者不相等」—— 若相等则本用例失去意义，直接 fail，
// 避免将来有人改了构造却让用例静默失效。
//
// ⚠️ 断言顺序：先做自检（构造有效性），后断言功能。
// 反过来的话，自检先红会把「实现写错」掩盖成「构造失效」（captain 在 t189 复核时踩过）。
func assertDetailThresholdIsConfigured(t *testing.T, db *gorm.DB, ruleID string, configured, count float64) {
	t.Helper()
	// 构造自检必须在功能断言**之后**执行，见上方说明。
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", ruleID).First(&warning).Error; err != nil {
		t.Fatalf("load %s warning: %v", ruleID, err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("%s detail JSON: %v", ruleID, err)
	}
	got, ok := detail["threshold"].(float64)
	if !ok {
		t.Fatalf("%s detail 缺少 threshold 字段：%v", ruleID, detail)
	}
	// 功能断言：threshold 必须是配置值。
	if got != configured {
		t.Fatalf("%s detail.threshold 应为配置值 %v，实际 %v。"+
			"若实际等于 count=%v，说明实现把 threshold 写成了实际计数",
			ruleID, configured, got, count)
	}
	// 构造自检（放在功能断言之后）。
	if configured == count {
		t.Fatalf("%s 构造失效：阈值 %v 与计数 %v 相等，本用例拷不动「threshold 写成计数」的缺陷",
			ruleID, configured, count)
	}
}

// R8 ticket_flood：阈值 2、实际建 5 单。
func TestRiskDetailThresholdIsConfiguredTicketFlood(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	const configured = 2.0
	const count = 5
	putRiskSetting(t, db, "risk_rule_ticket_flood_threshold", "2")
	for i := 0; i < count; i++ {
		seedRiskTicket(t, db, riskR8TestUserID, entity.TicketOriginUser, now.Add(-time.Duration(i)*time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "ticket_flood"); got != 1 {
		t.Fatalf("前置条件：必须先告警，实际 %d 条", got)
	}
	assertDetailThresholdIsConfigured(t, db, "ticket_flood", configured, count)
}

// R11 abnormal_register_pattern：阈值 2、实际连续 4 个用户名。
func TestRiskDetailThresholdIsConfiguredAbnormalRegister(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	const configured = 2.0
	const count = 4
	putRiskSetting(t, db, "risk_rule_abnormal_register_pattern_threshold", "2")
	for _, name := range []string{"detailthr1", "detailthr2", "detailthr3", "detailthr4"} {
		seedRiskRegisterUser(t, db, name, now.Add(-time.Minute))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "abnormal_register_pattern"); got != 1 {
		t.Fatalf("前置条件：必须先告警，实际 %d 条", got)
	}
	assertDetailThresholdIsConfigured(t, db, "abnormal_register_pattern", configured, count)
}

// R14 binding_churn：阈值 1、实际 3 次换绑。
func TestRiskDetailThresholdIsConfiguredBindingChurn(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	const configured = 1.0
	const count = 3
	putRiskSetting(t, db, "risk_rule_binding_churn_threshold", "1")
	for i := 0; i < count; i++ {
		seedRiskReleasedBinding(t, db, 7401, entity.BindingKindPhone,
			"1380013830"+itoaRisk(int32(i)),
			now.Add(-48*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
			entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "binding_churn"); got != 1 {
		t.Fatalf("前置条件：必须先告警，实际 %d 条", got)
	}
	assertDetailThresholdIsConfigured(t, db, "binding_churn", configured, count)
}

// R18 wechat_phone_share：阈值 1、实际 4 个账号。
func TestRiskDetailThresholdIsConfiguredWechatPhoneShare(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	const configured = 1.0
	const count = 4
	putRiskSetting(t, db, "risk_rule_wechat_phone_share_threshold", "1")
	for i := 0; i < count; i++ {
		user := seedRiskBannedUser(t, db, "detailshare"+itoaRisk(int32(i)), "banned", nil, nil)
		seedRiskReleasedBinding(t, db, user.ID, entity.BindingKindPhone, "13800138399",
			now.Add(-240*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
			entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "wechat_phone_share"); got != 1 {
		t.Fatalf("前置条件：必须先告警，实际 %d 条", got)
	}
	assertDetailThresholdIsConfigured(t, db, "wechat_phone_share", configured, count)
}

// R12 token_abuse（配额分支）：阈值 2、实际 5 倍配额。
func TestRiskDetailThresholdIsConfiguredTokenAbuse(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	const configured = 2.0
	const count = 5.0
	putRiskSetting(t, db, "risk_rule_token_abuse_threshold", "2")
	seedRiskAccessToken(t, db, 1, "detail-token", 100, 500, "203.0.113.1") // 5 倍
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "token_abuse"); got != 1 {
		t.Fatalf("前置条件：必须先告警，实际 %d 条", got)
	}
	assertDetailThresholdIsConfigured(t, db, "token_abuse", configured, count)
}

// R1 multi_account_ip：阈值 2、实际 4 个账号。
func TestRiskDetailThresholdIsConfiguredMultiAccountIP(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	const configured = 2.0
	const count = 4
	putRiskSetting(t, db, "risk_rule_multi_account_ip_threshold", "2")
	for i := 0; i < count; i++ {
		seedRiskLoginAudit(t, db, int32(7500+i), "detailip"+itoaRisk(int32(i)), "203.0.113.50", now.Add(-time.Hour), "success")
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "multi_account_ip"); got != 1 {
		t.Fatalf("前置条件：必须先告警，实际 %d 条", got)
	}
	assertDetailThresholdIsConfigured(t, db, "multi_account_ip", configured, count)
}

var _ = logger.ShanghaiLocation
