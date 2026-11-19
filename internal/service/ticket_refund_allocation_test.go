package service

import (
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

// 跨多笔订单的金额分摊（2026-11-19 用户要求）。
//
// 用户原话：「假如我要退款10块钱，但是支付记录有两笔，每笔都7.5，这个时候
// 要允许用户选择两笔订单……默认情况下，最旧的那笔全额退回，然后最新的部分退款」。
//
// 旧的「所有订单同额」模型表达不出 7.5 + 2.5，所以本轮加了 Allocations。

// 分摊：两笔各 7.5 元，退 10 元 → 第一笔 7.5、第二笔 2.5，两张退款单。
func TestCreateRefundsWithAllocationsSplitsAcrossOrders(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-2 * time.Hour)
	older := seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt.Add(-time.Hour), 7.5, true)
	newer := seedTicketRefundOrder(t, db, 2, owner.ID, rechargeOrderStatusPaid, paidAt, 7.5, true)

	rows, err := CreateTicketRefundsForOrdersWithDB(db, owner, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindOriginal,
		Allocations: []TicketRefundOrderAllocation{
			{OrderID: older.ID, Cents: 750},
			{OrderID: newer.ID, Cents: 250},
		},
		Reason: "跨两笔分摊",
	}, testVerifyTicketRefundChannel)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("两笔分摊应生成两张退款单，实际 %d", len(rows))
	}
	// 逐笔比对金额，不能只看总数：总数相同但分配反了同样是错的
	// （用户要求「最旧的那笔全额退回，然后最新的部分退款」）。
	byOrder := map[int32]int64{}
	for _, row := range rows {
		if row.OrderID == nil {
			t.Fatal("每张退款单都必须绑定订单")
		}
		byOrder[*row.OrderID] = row.RequestedCents
	}
	if byOrder[older.ID] != 750 {
		t.Fatalf("较旧那笔应全额 750 分，实际 %d", byOrder[older.ID])
	}
	if byOrder[newer.ID] != 250 {
		t.Fatalf("较新那笔应部分 250 分，实际 %d", byOrder[newer.ID])
	}
}

// 分摊里单笔超过该笔订单实付 → 被拒（每笔都不能超额）。
func TestCreateRefundsWithAllocationsRejectsPerOrderOverflow(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-time.Hour)
	small := seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 7.5, true)

	_, err := CreateTicketRefundsForOrdersWithDB(db, owner, CreateTicketRefundInput{
		Kind:        entity.TicketRefundKindOriginal,
		Allocations: []TicketRefundOrderAllocation{{OrderID: small.ID, Cents: 800}},
		Reason:      "超过该笔实付",
	}, testVerifyTicketRefundChannel)
	if !errors.Is(err, ErrTicketRefundAmount) {
		t.Fatalf("单笔超过该笔实付（7.5 元）必须被拒，got %v", err)
	}
}

// 同一订单在分摊里出现两次 → 拒绝，不能静默选一个。
func TestCreateRefundsWithAllocationsRejectsDuplicateOrder(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-time.Hour)
	order := seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, true)

	_, err := CreateTicketRefundsForOrdersWithDB(db, owner, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindOriginal,
		Allocations: []TicketRefundOrderAllocation{
			{OrderID: order.ID, Cents: 1000},
			{OrderID: order.ID, Cents: 2000},
		},
		Reason: "重复订单",
	}, testVerifyTicketRefundChannel)
	if !errors.Is(err, ErrTicketRefundInvalid) {
		t.Fatalf("同一订单两个金额是真矛盾，必须拒绝，got %v", err)
	}
}

// 批次合计超过可退本金 → 拒绝。
//
// 这是单笔闸门管不到的那一类：本金 8 元、两笔各申请 7.5 与 2.5，单看都在
// 8 元额度内，合计 10 元超了。
func TestCreateRefundsWithAllocationsRejectsBatchOverPrincipal(t *testing.T) {
	// 余额 8 元、无赠金消耗 → 可退本金 8 元。
	db, owner, _ := newRefundablePrincipalTestDB(t, 8, 0)
	paidAt := time.Now().Add(-2 * time.Hour)
	older := seedRefundableOrder(t, db, 1, owner.ID, 7.5)
	newer := seedRefundableOrder(t, db, 2, owner.ID, 7.5)
	_ = paidAt

	_, err := CreateTicketRefundsForOrdersWithDB(db, owner, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindOriginal,
		Allocations: []TicketRefundOrderAllocation{
			{OrderID: older.ID, Cents: 750},
			{OrderID: newer.ID, Cents: 250},
		},
		Reason: "批次超本金",
	}, testVerifyTicketRefundChannel)
	if err == nil {
		t.Fatal("两笔合计 10 元超过可退本金 8 元，必须被拒（单笔闸门挡不住这种）")
	}
}

// 未给 Allocations 时保持老的「同额」语义（向后兼容）。
func TestCreateRefundsWithoutAllocationsUsesSharedAmount(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	paidAt := time.Now().Add(-time.Hour)
	a := seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, true)
	b := seedTicketRefundOrder(t, db, 2, owner.ID, rechargeOrderStatusPaid, paidAt.Add(time.Minute), 50, true)

	rows, err := CreateTicketRefundsForOrdersWithDB(db, owner, CreateTicketRefundInput{
		Kind: entity.TicketRefundKindOriginal, OrderIDs: []int32{a.ID, b.ID},
		RequestedCents: 1000, Reason: "同额模式",
	}, testVerifyTicketRefundChannel)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 got %d", len(rows))
	}
	for _, row := range rows {
		if row.RequestedCents != 1000 {
			t.Fatalf("同额模式下每笔都应是 1000 分，实际 %d", row.RequestedCents)
		}
	}
}
