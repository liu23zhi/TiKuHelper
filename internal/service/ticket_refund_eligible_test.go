package service

import (
	"testing"
	"time"

	"gorm.io/gorm"
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
