package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════════════════════
// R13 refund_abuse（97 §5.1）
//
// 判据：单用户 **30 天内成功退款 ≥N 次，且每次都接近满额**。
//
// 三个数据陷阱，每个都有用例：
//  1. approved_cents = 0（申请被拒）不能进分子
//  2. order_paid_cents = 0（手工退款无订单）不能除零
//  3. 同一订单只有一条退款（order_id 唯一索引）
//
// 以及最容易被写错的一条：**只算 status = succeeded**。
// 算错会把「申请 3 次全被驳回」的用户误报成滥用 —— 那类用户是被系统保护的人。
// ════════════════════════════════════════════════════════════════════

// seedTicketRefund 造一条退款记录。
func seedTicketRefund(t *testing.T, db *gorm.DB, userID int32, status string,
	approved, orderPaid int64, at time.Time, orderID *int32) entity.TicketRefund {
	t.Helper()
	row := entity.TicketRefund{
		UserID:         userID,
		OrderID:        orderID,
		Kind:           entity.TicketRefundKindOriginal,
		Status:         status,
		RequestedCents: approved,
		ApprovedCents:  approved,
		OrderPaidCents: orderPaid,
		// RefundNo 是唯一索引，必须每行不同。
		RefundNo: "refund-" + itoaRisk(userID) + "-" + itoaRisk(int32(approved)) + "-" + itoaRisk(int32(at.Unix())),
	}
	if orderID == nil {
		row.Kind = entity.TicketRefundKindManual
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create ticket refund: %v", err)
	}
	// created_at 由 GORM 自动填当前时间，这里回写目标时间以控制窗口。
	if err := db.Model(&entity.TicketRefund{}).Where("id = ?", row.ID).
		UpdateColumn("created_at", at).Error; err != nil {
		t.Fatalf("backdate refund: %v", err)
	}
	return row
}

func orderIDPtr(value int32) *int32 { return &value }

// 判据①（**最关键**）：只算 `succeeded`。
//
// 「申请 3 次全被驳回」的用户不能告警 —— 那是被系统保护了的人。
//
// 变异方向：去掉 status 过滤 → 本用例必红。
func TestRefundAbuseIgnoresNonSucceeded(t *testing.T) {
	cases := []struct {
		name   string
		status string
	}{
		{"rejected", entity.TicketRefundStatusRejected},
		{"pending_review", entity.TicketRefundStatusPendingReview},
		{"frozen", entity.TicketRefundStatusFrozen},
		{"processing", entity.TicketRefundStatusProcessing},
		{"failed", entity.TicketRefundStatusFailed},
		{"unknown", entity.TicketRefundStatusUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name+"_not_counted", func(t *testing.T) {
			db := newRiskAlertTestDBNamed(t, "refund-"+tc.status)
			seedRiskRules(t, db)
			now := riskR1TestTime()
			// 5 笔全部是接近满额，但状态不是 succeeded。
			for i := 0; i < 5; i++ {
				seedTicketRefund(t, db, 8801, tc.status, 10000, 10000,
					now.Add(-time.Duration(i+1)*time.Hour), orderIDPtr(int32(1000+i)))
			}
			if _, err := EvaluateDebtAlerts(db, now); err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if got := countRuleWarnings(t, db, "refund_abuse"); got != 0 {
				t.Fatalf("status=%s 的退款不得计入（只有 succeeded 是钱真的退出去），实际告警 %d 条",
					tc.status, got)
			}
		})
	}
}

// 判据②：succeeded + 接近满额 3 次 → 告警。
func TestRefundAbuseDetectsNearFullRefunds(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "refund-hit")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 3 笔全部满额（100%）。
	for i := 0; i < 3; i++ {
		seedTicketRefund(t, db, 8802, entity.TicketRefundStatusSucceeded, 10000, 10000,
			now.Add(-time.Duration(i+1)*24*time.Hour), orderIDPtr(int32(2000+i)))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "refund_abuse"); got != 1 {
		t.Fatalf("3 笔满额成功退款必须告警，实际 %d 条", got)
	}
	var warning entity.RiskWarning
	if err := db.Where("rule_id = ?", "refund_abuse").First(&warning).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(warning.Detail), &detail); err != nil {
		t.Fatalf("detail JSON: %v", err)
	}
	if got, _ := detail["nearFullCount"].(float64); got != 3 {
		t.Fatalf("detail.nearFullCount = %v, want 3", detail["nearFullCount"])
	}
	// 硬要求：threshold 是配置值（3），不是实际计数。
	if got, _ := detail["threshold"].(float64); got != 3 {
		t.Fatalf("detail.threshold = %v, want 3（配置值）", detail["threshold"])
	}
}

// 判据③（**第二个关键判据**）：不接近满额的退款不计入。
//
// 变异方向：去掉 90% 比例判断 → 本用例必红。
func TestRefundAbuseIgnoresPartialRefunds(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "refund-partial")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 5 笔只退 50% —— 明显是「按比例退」，不是套利。
	for i := 0; i < 5; i++ {
		seedTicketRefund(t, db, 8803, entity.TicketRefundStatusSucceeded, 5000, 10000,
			now.Add(-time.Duration(i+1)*24*time.Hour), orderIDPtr(int32(3000+i)))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "refund_abuse"); got != 0 {
		t.Fatalf("只退 50%% 的 5 笔退款不得告警（不是接近满额），实际 %d 条", got)
	}
}

// 判据④：比例边界 —— 恰好 90% 计入，89% 不计入。
func TestRefundAbuseNearFullRatioBoundary(t *testing.T) {
	t.Run("exactly_90_percent_counts", func(t *testing.T) {
		db := newRiskAlertTestDBNamed(t, "refund-90")
		seedRiskRules(t, db)
		now := riskR1TestTime()
		for i := 0; i < 3; i++ {
			seedTicketRefund(t, db, 8804, entity.TicketRefundStatusSucceeded, 9000, 10000,
				now.Add(-time.Duration(i+1)*24*time.Hour), orderIDPtr(int32(4000+i)))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "refund_abuse"); got != 1 {
			t.Fatalf("恰好 90%% 应计入（判据是 ≥90%%），实际 %d 条", got)
		}
	})
	t.Run("eighty_nine_percent_not_counted", func(t *testing.T) {
		db := newRiskAlertTestDBNamed(t, "refund-89")
		seedRiskRules(t, db)
		now := riskR1TestTime()
		for i := 0; i < 3; i++ {
			seedTicketRefund(t, db, 8805, entity.TicketRefundStatusSucceeded, 8900, 10000,
				now.Add(-time.Duration(i+1)*24*time.Hour), orderIDPtr(int32(5000+i)))
		}
		if _, err := EvaluateDebtAlerts(db, now); err != nil {
			t.Fatalf("evaluate: %v", err)
		}
		if got := countRuleWarnings(t, db, "refund_abuse"); got != 0 {
			t.Fatalf("89%% < 90%% 不该计入，实际 %d 条", got)
		}
	})
}

// 判据⑤：**陷阱 2** —— order_paid_cents = 0 不能除零。
//
// 手工退款（Kind=manual、OrderID 为 nil）没有订单实付金额。
// 对 0 元订单算「接近满额」没有意义，必须跳过而不是 panic / 误判。
func TestRefundAbuseSkipsZeroOrderPaid(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "refund-zero-paid")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 5 笔手工退款，order_paid = 0（无订单），approved = 100 元。
	for i := 0; i < 5; i++ {
		seedTicketRefund(t, db, 8806, entity.TicketRefundStatusSucceeded, 10000, 0,
			now.Add(-time.Duration(i+1)*24*time.Hour), nil)
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate 不得失败（除零会让整条规则崩）：%v", err)
	}
	if got := countRuleWarnings(t, db, "refund_abuse"); got != 0 {
		t.Fatalf("order_paid=0 的退款无法判断是否满额，必须跳过，实际告警 %d 条", got)
	}
}

// 判据⑥：**陷阱 1** —— approved_cents = 0 不能进分子。
func TestRefundAbuseSkipsZeroApproved(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "refund-zero-approved")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 脏数据：status=succeeded 但 approved=0（理论上不该有，防御性用例）。
	for i := 0; i < 5; i++ {
		seedTicketRefund(t, db, 8807, entity.TicketRefundStatusSucceeded, 0, 10000,
			now.Add(-time.Duration(i+1)*24*time.Hour), orderIDPtr(int32(6000+i)))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "refund_abuse"); got != 0 {
		t.Fatalf("approved=0 的退款不能进分子，实际告警 %d 条", got)
	}
}

// 判据⑦：30 天滚动窗口 —— 窗口外不计入。
func TestRefundAbuseRollingWindow(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "refund-window")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	// 3 笔满额，但都在 31–33 天前（窗口外）。
	for i := 0; i < 3; i++ {
		seedTicketRefund(t, db, 8808, entity.TicketRefundStatusSucceeded, 10000, 10000,
			now.Add(-time.Duration(31+i)*24*time.Hour), orderIDPtr(int32(7000+i)))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "refund_abuse"); got != 0 {
		t.Fatalf("31 天前的退款不在 30 天窗口内，不得告警，实际 %d 条", got)
	}
}

// 判据⑧：阈值可配（双向）。
func TestRefundAbuseThresholdIsConfigurable(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "refund-cfg")
	seedRiskRules(t, db)
	now := riskR1TestTime()
	for i := 0; i < 2; i++ {
		seedTicketRefund(t, db, 8809, entity.TicketRefundStatusSucceeded, 10000, 10000,
			now.Add(-time.Duration(i+1)*24*time.Hour), orderIDPtr(int32(8000+i)))
	}
	// 阈值 5：2 笔不足。
	putRiskSetting(t, db, "risk_rule_refund_abuse_threshold", "5")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "refund_abuse"); got != 0 {
		t.Fatalf("阈值 5 时 2 笔必须不告警，实际 %d 条", got)
	}
	// 阈值 2：必须告警。
	putRiskSetting(t, db, "risk_rule_refund_abuse_threshold", "2")
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "refund_abuse"); got != 1 {
		t.Fatalf("阈值 2 时 2 笔必须告警，实际 %d 条", got)
	}
}

// 判据⑨：只告警不处置（C1）。
func TestRefundAbuseNeverTakesAction(t *testing.T) {
	db := newRiskAlertTestDBNamed(t, "refund-noaction")
	if err := db.AutoMigrate(&entity.BlacklistEntry{}); err != nil {
		t.Fatalf("migrate blacklist: %v", err)
	}
	seedRiskRules(t, db)
	now := riskR1TestTime()
	user := entity.User{Username: "refunder", Password: "x", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for i := 0; i < 3; i++ {
		seedTicketRefund(t, db, user.ID, entity.TicketRefundStatusSucceeded, 10000, 10000,
			now.Add(-time.Duration(i+1)*24*time.Hour), orderIDPtr(int32(9000+i)))
	}
	if _, err := EvaluateDebtAlerts(db, now); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countRuleWarnings(t, db, "refund_abuse"); got != 1 {
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
		t.Fatalf("count: %v", err)
	}
	if blacklist != 0 {
		t.Fatalf("C1：风控不得写黑名单，实际 %d 行", blacklist)
	}
}
