package service

import (
	"gorm.io/gorm"
	"strings"
	"testing"
	"time"
)

// 「有已支付订单，但可退列表里一条都没有」的回归钉子。
//
// 2026-11-18 用户报告：充值记录里明明有订单，退款工单的「退款订单」下拉却是空的，
// 且没有任何提示。根因是 listEligibleTicketRefundOrders 在 **SQL 层**就把
// `refund_channel_fingerprint <> '' AND refund_original_trade_no <> ''` 当硬条件，
// 于是「已支付、钱已收、但拿不到渠道凭证」的订单被静默丢弃。
//
// 正确语义：这类订单应当**列出来并标记只能人工退款**（OriginalSupported=false），
// 而不是让它凭空消失 —— 订单存在是事实，能否原路退回是另一件事。

// 缺渠道凭证的已支付订单必须出现在列表里，且标记为不支持原路退回。
func TestEligibleOrdersIncludeUnprovenPaidOrders(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-2 * time.Hour)

	// proven=false → 没有 refund_channel_fingerprint / refund_original_trade_no
	seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, false)

	items, err := listEligibleTicketRefundOrders(db, owner.ID, testVerifyTicketRefundChannel, time.Now(), 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("缺渠道凭证的已支付订单必须出现（否则用户看到「有订单却选不了」），want 1 got %d", len(items))
	}
	if items[0].OriginalSupported {
		t.Fatalf("缺渠道凭证的订单不能标记为支持原路退回，实际 %+v", items[0])
	}
	if items[0].PaidCents != 5000 {
		t.Fatalf("金额应为 5000 分，实际 %d", items[0].PaidCents)
	}
}

// 渠道完整的订单仍要标记为支持原路退回（不能因为放宽而把所有单都降级）。
func TestEligibleOrdersMarkProvenOrdersSupported(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-2 * time.Hour)
	seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, true)

	items, err := listEligibleTicketRefundOrders(db, owner.ID, testVerifyTicketRefundChannel, time.Now(), 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 got %d", len(items))
	}
	if !items[0].OriginalSupported {
		t.Fatalf("渠道完整的订单必须标记为支持原路退回，实际 %+v", items[0])
	}
}

// 未支付订单仍然不能进列表（钱没收到，无可退）。
func TestEligibleOrdersExcludeUnpaid(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	seedTicketRefundOrder(t, db, 1, owner.ID, "pending", time.Now().Add(-2*time.Hour), 50, true)

	items, err := listEligibleTicketRefundOrders(db, owner.ID, testVerifyTicketRefundChannel, time.Now(), 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("未支付订单不能进可退列表，实际 %+v", items)
	}
}

// 渠道未配置（verify == nil）时订单依然要列出来，只是全部只能人工退款。
//
// 原实现在 verify == nil 时直接 return 空列表 —— 用户看到「一条都没有」，
// 而真实原因是支付渠道没配置。
func TestEligibleOrdersListedEvenWhenVerifierMissing(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-2 * time.Hour)
	seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, true)

	items, err := listEligibleTicketRefundOrders(db, owner.ID, nil, time.Now(), 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("渠道未配置时订单仍应列出（否则表现为「列表全空」），want 1 got %d", len(items))
	}
	if items[0].OriginalSupported {
		t.Fatalf("渠道未配置时不能标记为支持原路退回，实际 %+v", items[0])
	}
}

var _ = gorm.ErrRecordNotFound

// 2026-11-19 用户要求：选择账单时要**显示不能原路退款的账单并写原因**。
//
// 这组用例钉两件事：
//   · 不能原路退款的订单必须给出**非空且可读**的原因（不是只给一个 false）；
//   · 能原路退款的订单原因必须为空（否则前端会在可退的选项上也显示警示）。

// 缺渠道凭证 → reason 必须非空，且指明是「缺渠道信息」而不是笼统的失败。
func TestEligibleOrderCarriesReasonWhenOriginalUnsupported(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-2 * time.Hour)
	// proven=false → 没有 refund_channel_fingerprint / refund_original_trade_no
	seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, false)

	items, err := listEligibleTicketRefundOrders(db, owner.ID, testVerifyTicketRefundChannel, time.Now(), 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("缺渠道凭证的订单必须出现在列表里，want 1 got %d", len(items))
	}
	if items[0].OriginalSupported {
		t.Fatalf("缺渠道凭证不能标记为支持原路退回")
	}
	reason := items[0].OriginalUnsupportedReason
	if strings.TrimSpace(reason) == "" {
		t.Fatal("不能原路退款时必须给出原因 —— 只给一个 false 用户无从判断该找谁处理")
	}
	// 断具体内容而不只是「非空」：这两列都缺时应该指向「回调未写入」，
	// 因为那决定了用户该等回调还是该找管理员。
	if !strings.Contains(reason, "渠道") {
		t.Fatalf("原因必须说明是渠道信息问题，实际 %q", reason)
	}
}

// 渠道完整 → reason 必须为空，避免前端在可原路退的选项上也显示警示。
func TestEligibleOrderHasNoReasonWhenOriginalSupported(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-2 * time.Hour)
	seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, true)

	items, err := listEligibleTicketRefundOrders(db, owner.ID, testVerifyTicketRefundChannel, time.Now(), 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 got %d", len(items))
	}
	if !items[0].OriginalSupported {
		t.Fatalf("渠道完整的订单必须支持原路退回")
	}
	if items[0].OriginalUnsupportedReason != "" {
		t.Fatalf("支持原路退回时原因必须为空，实际 %q", items[0].OriginalUnsupportedReason)
	}
}

// 渠道未配置（verify=nil）→ 原因必须指向「渠道未配置」。
// 这条与「订单缺凭证」是不同原因，混成一句会让管理员查错方向。
func TestEligibleOrderReasonDistinguishesUnconfiguredChannel(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-2 * time.Hour)
	seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, true)

	items, err := listEligibleTicketRefundOrders(db, owner.ID, nil, time.Now(), 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 got %d", len(items))
	}
	if items[0].OriginalSupported {
		t.Fatal("渠道未配置时不能标记为支持原路退回")
	}
	if !strings.Contains(items[0].OriginalUnsupportedReason, "未配置") {
		t.Fatalf("原因必须说明「渠道未配置」，实际 %q", items[0].OriginalUnsupportedReason)
	}
}
