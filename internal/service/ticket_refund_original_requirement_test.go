package service

import (
	"errors"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
)

func TestOriginalRefundRequiresSelectedOrdersAndFullCoverage(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	base := CreateTicketInput{
		TicketType: TicketTypeRefund, RefundMethod: "original",
		RefundRequestedCents: 1000, RefundReason: "10元退款",
	}
	if _, err := CreateTicketForUserWithDB(db, &owner, base, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundOrderUnavailable) {
		t.Fatalf("原路退款没有订单必须被拒绝，got %v", err)
	}

	order := seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, time.Now().Add(-time.Hour), 5, true)
	base.RefundOrderAllocations = []TicketRefundOrderAllocation{{OrderID: order.ID, Cents: 500}}
	if _, err := CreateTicketForUserWithDB(db, &owner, base, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundAmount) {
		t.Fatalf("10元申请只选5元订单必须被拒绝，got %v", err)
	}

	base.RefundRequestedCents = 0
	if _, err := CreateTicketForUserWithDB(db, &owner, base, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundAmount) {
		t.Fatalf("分摊请求缺少申请总额必须被拒绝，got %v", err)
	}

	var count int64
	if err := db.Model(&entity.TicketRefund{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("无效申请不得创建退款记录，count=%d err=%v", count, err)
	}
}

func TestLegacyRefundOrderIDsKeepSameAmountBehavior(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	owner := seedRefundActor(t, db, 1, "legacy")
	paidAt := time.Now().Add(-time.Hour)
	a := seedTicketRefundOrder(t, db, 1, owner.ID, rechargeOrderStatusPaid, paidAt, 50, true)
	b := seedTicketRefundOrder(t, db, 2, owner.ID, rechargeOrderStatusPaid, paidAt.Add(time.Minute), 50, true)

	_, err := CreateTicketForUserWithDB(db, &owner, CreateTicketInput{
		TicketType: TicketTypeRefund, RefundOrderIDs: []int32{a.ID, b.ID},
		RefundRequestedCents: 1000, RefundReason: "legacy same amount",
	}, testVerifyTicketRefundChannel)
	if err != nil {
		t.Fatalf("legacy refundOrderIds payload should remain compatible: %v", err)
	}
	var refunds []entity.TicketRefund
	orderIDs := []int32{a.ID, b.ID}
	if err := db.Where("order_id IN ?", orderIDs).Order("order_id ASC").Find(&refunds).Error; err != nil {
		t.Fatalf("load refunds: %v", err)
	}
	if len(refunds) != 2 {
		t.Fatalf("legacy same-amount request should create 2 refunds, got %d", len(refunds))
	}
	for _, refund := range refunds {
		if refund.RequestedCents != 1000 {
			t.Fatalf("legacy same-amount semantics should refund 1000 cents per order, got %d", refund.RequestedCents)
		}
	}
}
