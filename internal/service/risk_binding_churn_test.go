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
// R14 · binding_churn（96 §2.1）单账号滚动 24h 主动换绑 ≥N 次
//
// 三个数据源陷阱（本文件每条判据分别钉一个）：
//  1. 时间列必须 released_at，不是 bound_at
//  2. 必须排除 state='voided'
//  3. release_source 只有 user_unbind 计入；force_reassign 是被抢号（受害者）
// ════════════════════════════════════════════════════════════════════

const riskR14TestUserID = int32(7301)

// seedRiskReleasedBinding 造一条「已释放」绑定记录。
//
// boundAt 与 releasedAt 分开传 —— 这正是 R14 最容易搞错的地方：
// 判据看的是 releasedAt，boundAt 只是历史信息。
func seedRiskReleasedBinding(t *testing.T, db *gorm.DB, userID int32, kind, value string,
	boundAt, releasedAt time.Time, source, state string) {
	t.Helper()
	record := entity.UserBindingRecord{
		UserID:        userID,
		Kind:          kind,
		Value:         value,
		ValueHash:     BindingValueHash(value),
		State:         state,
		Source:        entity.BindingSourceUserBind,
		BoundAt:       &boundAt,
		ReleasedAt:    &releasedAt,
		ReleaseReason: entity.BindingReleaseReasonRebind,
		ReleaseSource: source,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatalf("create binding record: %v", err)
	}
}

// 判据①：阈值边界 —— 1 次不告警，2 次告警（阈值默认 2）。
func TestRiskBindingChurnThresholdBoundary(t *testing.T) {
	t.Run("one_release_below_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := riskR1TestTime()
		seedRiskReleasedBinding(t, db, riskR14TestUserID, entity.BindingKindPhone, "13800138101",
			now.Add(-48*time.Hour), now.Add(-2*time.Hour), entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)

		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "binding_churn"); got != 0 {
			t.Fatalf("1 次 < 阈值 2 必须不告警，实际 %d 条", got)
		}
	})

	t.Run("two_releases_meets_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := riskR1TestTime()
		for i, value := range []string{"13800138102", "13800138103"} {
			seedRiskReleasedBinding(t, db, riskR14TestUserID, entity.BindingKindPhone, value,
				now.Add(-72*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
				entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "binding_churn"); got != 1 {
			t.Fatalf("2 次 = 阈值 2 必须告警且只 1 条，实际 %d 条", got)
		}
		var warning entity.RiskWarning
		if err := db.Where("rule_id = ?", "binding_churn").First(&warning).Error; err != nil {
			t.Fatalf("load: %v", err)
		}
		if warning.UserID == nil || *warning.UserID != riskR14TestUserID {
			t.Fatalf("告警必须归属该用户，实际 %+v", warning.UserID)
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
			t.Fatalf("detail JSON: %v", err)
		}
		if got, _ := detail["churnCount"].(float64); got != 2 {
			t.Fatalf("detail.churnCount = %v, want 2", detail["churnCount"])
		}
	})
}

// 判据②（关键）：`force_reassign` 是**被抢号**，不得算作本人频繁换绑。
//
// 变异方向：把 force_reassign 加进白名单 → 本用例红（受害者被当成惯犯）。
func TestRiskBindingChurnExcludesForceReassignVictim(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 3 次都是「被别人抢号」——本人是受害者。
	for i, value := range []string{"13800138111", "13800138112", "13800138113"} {
		seedRiskReleasedBinding(t, db, riskR14TestUserID, entity.BindingKindPhone, value,
			now.Add(-72*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
			entity.BindingReleaseSourceForceReassign, entity.BindingStateReleased)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "binding_churn"); got != 0 {
		t.Fatalf("被抢号（force_reassign）3 次不得告警 —— 那是受害者，实际 %d 条", got)
	}
	// 反向对照：同样 3 条但 source=user_unbind 时必须告警。
	// 否则上面的 0 可能只是因为规则没跑（假绿）。
	for i, value := range []string{"13800138121", "13800138122"} {
		seedRiskReleasedBinding(t, db, 7302, entity.BindingKindPhone, value,
			now.Add(-72*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
			entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "binding_churn"); got != 1 {
		t.Fatalf("另一用户 2 次 user_unbind 必须告警，实际 %d 条（若为 0 说明上面的 0 与 source 无关）", got)
	}
}

// 判据③：`state='voided'` 必须排除。
//
// 管理员作废只改 state/active_key/void_*，**不动** released_at 与 release_reason，
// 所以被作废的行仍带着原来的释放时间 —— 不排除就会被当成一次换绑。
//
// 变异方向：去掉 `state <> 'voided'` → 本用例红。
func TestRiskBindingChurnExcludesVoidedRecords(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 2 条都是被管理员作废的行（state=voided），但 released_at 仍在窗口内。
	for i, value := range []string{"13800138131", "13800138132"} {
		seedRiskReleasedBinding(t, db, riskR14TestUserID, entity.BindingKindPhone, value,
			now.Add(-72*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
			entity.BindingReleaseSourceUserUnbind, entity.BindingStateVoided)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "binding_churn"); got != 0 {
		t.Fatalf("被作废（voided）的行不得计入换绑次数，实际 %d 条", got)
	}
}

// 判据④（关键）：时间列必须是 released_at，不是 bound_at。
//
// 造「很久以前绑的、24h 内释放」的记录：
//
//	· 用 released_at 判 → 命中（正确）
//	· 用 bound_at 判    → 不命中（错误）
//
// 变异方向：released_at 改成 bound_at → 本用例红。
func TestRiskBindingChurnUsesReleasedAtNotBoundAt(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// bound_at 是 10 天前（远在窗口外），released_at 是 1/2 小时前（窗口内）。
	oldBound := now.Add(-240 * time.Hour)
	for i, value := range []string{"13800138141", "13800138142"} {
		seedRiskReleasedBinding(t, db, riskR14TestUserID, entity.BindingKindPhone, value,
			oldBound, now.Add(-time.Duration(i+1)*time.Hour),
			entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "binding_churn"); got != 1 {
		t.Fatalf("25 小时前绑定、2 小时内释放的 2 条必须告警（按 released_at 判）；"+
			"实际 %d 条。若为 0 说明用了 bound_at 而不是 released_at", got)
	}
}

// 判据⑤：滚动 24h 窗口 —— 26 小时前的释放不计入（而自然日窗口可能会计入）。
func TestRiskBindingChurnRollingWindow(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 两条都在 26 小时前 → 滚动 24h 之外。
	for i, value := range []string{"13800138151", "13800138152"} {
		seedRiskReleasedBinding(t, db, riskR14TestUserID, entity.BindingKindPhone, value,
			now.Add(-480*time.Hour), now.Add(-26*time.Hour-time.Duration(i)*time.Minute),
			entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "binding_churn"); got != 0 {
		t.Fatalf("26 小时前的释放不在滚动 24h 窗口内，必须不告警，实际 %d 条", got)
	}
	// 反向对照：把同一批挪到 2 小时内 → 必须告警（证明窗口真的在起作用）。
	db2 := newRiskAlertTestDBNamed(t, t.Name()+"-second")
	seedRiskRules(t, db2)
	for i, value := range []string{"13800138161", "13800138162"} {
		seedRiskReleasedBinding(t, db2, riskR14TestUserID, entity.BindingKindPhone, value,
			now.Add(-480*time.Hour), now.Add(-2*time.Hour-time.Duration(i)*time.Minute),
			entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
	}
	if _, err := EvaluateDebtAlerts(db2, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db2, "binding_churn"); got != 1 {
		t.Fatalf("2 小时前的 2 条必须告警，实际 %d 条（若为 0 说明窗口条件写反）", got)
	}
}

// 判据⑥：阈值可配（双向）。
func TestRiskBindingChurnThresholdIsConfigurable(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	seedRiskReleasedBinding(t, db, riskR14TestUserID, entity.BindingKindPhone, "13800138171",
		now.Add(-48*time.Hour), now.Add(-2*time.Hour), entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)

	// 阈值调到 5：1 次不告警。
	putRiskSetting(t, db, "risk_rule_binding_churn_threshold", "5")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "binding_churn"); got != 0 {
		t.Fatalf("阈值 5 时 1 次必须不告警，实际 %d 条", got)
	}
	// 阈值调到 1：必须告警。
	putRiskSetting(t, db, "risk_rule_binding_churn_threshold", "1")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "binding_churn"); got != 1 {
		t.Fatalf("阈值 1 时 1 次必须告警，实际 %d 条", got)
	}
}

// 判据⑦：只告警不处置。
func TestRiskBindingChurnNeverTakesAction(t *testing.T) {
	db := newRiskAlertTestDB(t)
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate blacklist: %v", err)
	}
	seedRiskRules(t, db)
	now := riskR1TestTime()
	user := entity.User{Username: "churner", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for i, value := range []string{"13800138181", "13800138182"} {
		seedRiskReleasedBinding(t, db, user.ID, entity.BindingKindPhone, value,
			now.Add(-48*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
			entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "binding_churn"); got != 1 {
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

// ════════════════════════════════════════════════════════════════════
// R18 · wechat_phone_share（96 §2.1）
//
// ⚠️ 数据源真实边界（本文件必须钉住，否则会做出一条永不告警的规则）：
//   users.phone / users.wechat_id 都是 **UNIQUE INDEX**，
//   同一号码物理上不可能同时挂在两个账号上。所以「按 users 当前值分组」恒为 0，
//   主判据改用 binding 历史（同一 value 被多个不同 user_id 占用过）。
// ════════════════════════════════════════════════════════════════════

// seedRiskBannedUser 造一个封禁账号（可带 phone/wechat）。
func seedRiskBannedUser(t *testing.T, db *gorm.DB, username, status string, phone, wechat *string) entity.User {
	t.Helper()
	user := entity.User{Username: username, Password: "x", Status: status, Role: "user",
		Phone: phone, WechatID: wechat}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return user
}

// 判据①：同一手机号被 2 个账号占用（含 1 个封禁）→ 不告警；3 个 → 告警。
func TestRiskWechatPhoneShareThresholdBoundary(t *testing.T) {
	t.Run("two_accounts_below_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := riskR1TestTime()
		// 同一号码被 2 个账号占用过（1 banned + 1 enabled）。
		banned := seedRiskBannedUser(t, db, "r18b1", "banned", nil, nil)
		normal := seedRiskBannedUser(t, db, "r18n1", "enabled", nil, nil)
		for i, userID := range []int32{banned.ID, normal.ID} {
			seedRiskReleasedBinding(t, db, userID, entity.BindingKindPhone, "13800138201",
				now.Add(-240*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
				entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "wechat_phone_share"); got != 0 {
			t.Fatalf("同号 2 个账号 < 阈值 3 必须不告警，实际 %d 条", got)
		}
	})

	t.Run("three_accounts_meets_threshold", func(t *testing.T) {
		db := newRiskAlertTestDB(t)
		seedRiskRules(t, db)
		now := riskR1TestTime()
		banned := seedRiskBannedUser(t, db, "r18b2", "banned", nil, nil)
		n1 := seedRiskBannedUser(t, db, "r18n2", "enabled", nil, nil)
		n2 := seedRiskBannedUser(t, db, "r18n3", "enabled", nil, nil)
		for i, userID := range []int32{banned.ID, n1.ID, n2.ID} {
			seedRiskReleasedBinding(t, db, userID, entity.BindingKindPhone, "13800138202",
				now.Add(-240*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
				entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "wechat_phone_share"); got != 1 {
			t.Fatalf("同号 3 个账号 = 阈值 3 必须告警且只 1 条，实际 %d 条", got)
		}
		var warning entity.RiskWarning
		if err := db.Where("rule_id = ?", "wechat_phone_share").First(&warning).Error; err != nil {
			t.Fatalf("load: %v", err)
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
			t.Fatalf("detail JSON: %v", err)
		}
		if got, _ := detail["accountCount"].(float64); got != 3 {
			t.Fatalf("detail.accountCount = %v, want 3", detail["accountCount"])
		}
		if got, _ := detail["bannedCount"].(float64); got < 1 {
			t.Fatalf("detail.bannedCount 必须 ≥1，实际 %v", detail["bannedCount"])
		}
		// ⚠️ detail.threshold 必须是**阈值**，不能是实际计数。
		//
		// 这条判据是 captain 复核时发现的笔误补丁：原实现写的是
		// `"threshold": group.UserCount`（实际值 3），而不是 `threshold`（配置值 3）。
		// 表面看两个数字一样所以测试不会红 —— 但把阈值改成 5 之后，
		// 实际值仍是 3，排障的人会看到「threshold=3 却告警了」而无法理解。
		//
		// 所以断言必须**让阈值与计数不同**才能拷动这个缺陷。
		if got, _ := detail["threshold"].(float64); got != 3 {
			t.Fatalf("detail.threshold 应为阈值 3，实际 %v（写成 accountCount 会让阈值与计数混淆）", detail["threshold"])
		}
	})
}

// 判据②：**必须至少含一个封禁账号** —— 纯正常账号的换号流转不告警。
//
// 变异方向：去掉 bannedCount>0 判断 → 本用例红（正常流转被误报）。
func TestRiskWechatPhoneShareRequiresBannedAccount(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 3 个账号都用过同一号码，但**全是正常账号**（正常的换号流转）。
	for i, name := range []string{"r18ok1", "r18ok2", "r18ok3"} {
		user := seedRiskBannedUser(t, db, name, "enabled", nil, nil)
		seedRiskReleasedBinding(t, db, user.ID, entity.BindingKindPhone, "13800138203",
			now.Add(-240*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
			entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "wechat_phone_share"); got != 0 {
		t.Fatalf("全是正常账号的换号流转不得告警，实际 %d 条", got)
	}
}

// 判据③：phone / wechat 两个维度必须分开（SubjectKey 区分）。
func TestRiskWechatPhoneShareSeparatesDimensions(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 同一个字符串值，分别作为 phone 与 wechat，各 3 个账号。
	for i := 0; i < 3; i++ {
		user := seedRiskBannedUser(t, db, "r18dim"+itoaRisk(int32(i)), "banned", nil, nil)
		seedRiskReleasedBinding(t, db, user.ID, entity.BindingKindPhone, "shared-value-1",
			now.Add(-240*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
			entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
		seedRiskReleasedBinding(t, db, user.ID, entity.BindingKindWechat, "shared-value-1",
			now.Add(-240*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
			entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	// 两个维度各 1 条，共 2 条 —— 若 SubjectKey 不区分 kind 会退化成 1 条。
	if got := countRuleWarnings(t, db, "wechat_phone_share"); got != 2 {
		t.Fatalf("phone 与 wechat 两维度必须各 1 条（共 2 条），实际 %d 条。"+
			"若为 1 说明 SubjectKey 没区分 kind，两个维度互相覆盖了节流", got)
	}
	var phones, wechats int64
	db.Model(&entity.RiskWarning{}).Where("rule_id = ? AND subject_key LIKE ?",
		"wechat_phone_share", "%:phone:%").Count(&phones)
	db.Model(&entity.RiskWarning{}).Where("rule_id = ? AND subject_key LIKE ?",
		"wechat_phone_share", "%:wechat:%").Count(&wechats)
	if phones != 1 || wechats != 1 {
		t.Fatalf("subjectKey 维度分布错误：phone=%d wechat=%d（各应为 1）", phones, wechats)
	}
}

// 判据④：阈值可配（双向）。
func TestRiskWechatPhoneShareThresholdIsConfigurable(t *testing.T) {
	db := newRiskAlertTestDB(t)
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := 0; i < 2; i++ {
		user := seedRiskBannedUser(t, db, "r18cfg"+itoaRisk(int32(i)), "banned", nil, nil)
		seedRiskReleasedBinding(t, db, user.ID, entity.BindingKindPhone, "13800138204",
			now.Add(-240*time.Hour), now.Add(-time.Duration(i+1)*time.Hour),
			entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
	}
	// 阈值 5：2 个不足。
	putRiskSetting(t, db, "risk_rule_wechat_phone_share_threshold", "5")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "wechat_phone_share"); got != 0 {
		t.Fatalf("阈值 5 时 2 个账号必须不告警，实际 %d 条", got)
	}
	// 阈值 2：必须告警。
	putRiskSetting(t, db, "risk_rule_wechat_phone_share_threshold", "2")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "wechat_phone_share"); got != 1 {
		t.Fatalf("阈值 2 时 2 个账号必须告警，实际 %d 条", got)
	}

	// ⚠️ 关键：detail.threshold 必须是**配置的阈值**（2），而不是实际计数。
	//
	// 本用例的阈值(2) 与 accountCount(2) 恰好相等，所以必须用「阈值 5 时不告警、
	// 阈值 2 时告警」这个对照来验证配置确实生效；然后再断言 Detail 里的 threshold
	// 是 2 —— 若实现把 threshold 写成 group.UserCount，两者也恰好都是 2，
	// 仍然拷不动。
	//
	// 所以这条断言真正的价值在**下面的第三个子用例**：阈值 1、2 个账号。
	// 那时 threshold=1 而 accountCount=2，写错必红。
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", "wechat_phone_share").First(&warning).Error; err != nil {
		t.Fatalf("load warning: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	if got, _ := detail["threshold"].(float64); got != 2 {
		t.Fatalf("detail.threshold 应为 2，实际 %v", detail["threshold"])
	}

	// 子用例：阈值 1、2 个账号 ⇒ threshold(1) != accountCount(2)。
	// 这是唯一能让「threshold 写成 UserCount」这个笔误变红的构造。
	t.Run("threshold_differs_from_count", func(t *testing.T) {
		db2 := newRiskAlertTestDB(t)
		seedRiskRules(t, db2)
		now2 := riskR1TestTime()
		for i := 0; i < 2; i++ {
			user := seedRiskBannedUser(t, db2, "r18diff"+itoaRisk(int32(i)), "banned", nil, nil)
			seedRiskReleasedBinding(t, db2, user.ID, entity.BindingKindPhone, "13800138205",
				now2.Add(-240*time.Hour), now2.Add(-time.Duration(i+1)*time.Hour),
				entity.BindingReleaseSourceUserUnbind, entity.BindingStateReleased)
		}
		putRiskSetting(t, db2, "risk_rule_wechat_phone_share_threshold", "1")
		if _, err := EvaluateDebtAlerts(db2, now2); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		var w entity.RiskWarning
		if err := db2.Where("rule_id = ?", "wechat_phone_share").First(&w).Error; err != nil {
			t.Fatalf("阈值 1 时 2 个账号必须告警：%v", err)
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(w.Detail), &d); err != nil {
			t.Fatalf("detail JSON: %v", err)
		}
		gotThreshold, _ := d["threshold"].(float64)
		gotCount, _ := d["accountCount"].(float64)
		// ⚠️ 断言顺序有讲究：**先判功能正确性，再判构造有效性**。
		//
		// 反过来的话（先判「两者不等」），一旦实现把 threshold 写成 accountCount，
		// 两者会相等 → 自检先红 → 失败信息说「构造失效」，而真实原因是**实现写错了**。
		// 实测踩过：失败信息指向构造，掩盖了要测的缺陷。
		//
		// 所以先判 threshold 是否等于配置值 1 —— 写错时这里先红、信息直指实现。
		if gotThreshold != 1 {
			t.Fatalf("detail.threshold 应为配置值 1，实际 %v"+
				"（若等于 accountCount=%v，说明实现把 threshold 写成了实际计数）",
				gotThreshold, gotCount)
		}
		// 构造自检放最后：只有功能断言都过了才来说明「这个用例本身是否有效」。
		if gotThreshold == gotCount {
			t.Fatalf("threshold(%v) 与 accountCount(%v) 相等，本用例无法区分两者 —— 构造失效",
				gotThreshold, gotCount)
		}
	})
}

// itoaRisk 小工具（避免与已存在的 itoa 冲突）。
func itoaRisk(value int32) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// 保证 logger 引用（本文件的 seed 辅助跨文件使用）。
var _ = logger.ShanghaiLocation
