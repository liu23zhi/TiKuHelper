package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestTicketRefundRevisionNewTicketGroupsExplicitAllocations(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	if err := db.AutoMigrate(&entity.TicketRefundOrderLine{}); err != nil {
		t.Fatal(err)
	}
	owner := seedRefundActor(t, db, 60, "grouped-owner")
	admin := seedRefundActor(t, db, 61, "grouped-admin")
	if err := db.Model(&admin).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	first := seedTicketRefundOrder(t, db, 11, owner.ID, "paid", time.Now().Add(-2*time.Hour), 7.5, true)
	second := seedTicketRefundOrder(t, db, 12, owner.ID, "paid", time.Now().Add(-time.Hour), 7.5, true)
	detail, err := CreateTicketForUserWithDB(db, &owner, CreateTicketInput{TicketType: TicketTypeRefund, RefundMethod: TicketRefundKindOriginal,
		RefundRequestedCents: 1000, RefundReason: "两笔合计退款", RefundOrderAllocations: []TicketRefundOrderAllocation{
			{OrderID: first.ID, Cents: 750}, {OrderID: second.ID, Cents: 250},
		}}, nil)
	if err != nil || detail.Refund == nil || detail.Refund.RequestedCents != 1000 {
		t.Fatalf("grouped creation: %+v %v", detail.Refund, err)
	}
	var count int64
	if err := db.Model(&entity.Ticket{}).Where("user_id = ?", owner.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("grouped ticket count: %d %v", count, err)
	}
	lines, err := TicketRefundLinesWithDB(db, detail.Refund.RefundID)
	if err != nil || len(lines) != 2 || lines[0].Cents != 750 || lines[1].Cents != 250 {
		t.Fatalf("grouped lines: %+v %v", lines, err)
	}
	incorrectAmount := int64(900)
	verifyCalls := 0
	verify := func(tx *gorm.DB, order entity.RechargeOrder) (string, error) {
		verifyCalls++
		return testVerifyTicketRefundChannel(tx, order)
	}
	if _, err := ReviewTicketRefundDecisionWithDB(db, admin, detail.Refund.RefundID, true, &incorrectAmount, verify); !errors.Is(err, ErrTicketRefundAllocationMismatch) {
		t.Fatalf("expected allocation mismatch for lower approval amount: %v", err)
	}
	if verifyCalls != 0 {
		t.Fatalf("approval consulted channel for %d orders before checking allocation total", verifyCalls)
	}
	unchanged, err := GetTicketRefundWithDB(db, owner, detail.Refund.RefundID)
	if err != nil || unchanged.Status != TicketRefundStatusPendingReview || unchanged.ApprovedCents != 0 {
		t.Fatalf("invalid approval changed refund: %+v %v", unchanged, err)
	}
	after, err := TicketRefundLinesWithDB(db, detail.Refund.RefundID)
	if err != nil || len(after) != 2 {
		t.Fatalf("invalid approval changed orders: %+v %v", after, err)
	}
	for i, line := range after {
		if line.Cents != lines[i].Cents || line.Status != TicketRefundStatusPendingReview || line.ChannelRef != "" {
			t.Fatalf("invalid approval changed line: %+v", line)
		}
	}
	var holds int64
	if err := db.Model(&entity.TicketRefundLedger{}).Where("refund_id = ?", detail.Refund.RefundID).Count(&holds).Error; err != nil || holds != 0 {
		t.Fatalf("invalid approval froze funds: %+v %v", holds, err)
	}
	requireTicketRefundWallet(t, db, owner.ID, 100_000_000)
	if _, err := ReviewTicketRefundDecisionWithDB(db, admin, detail.Refund.RefundID, true, nil, nil); !errors.Is(err, ErrTicketRefundChannelDisabled) {
		t.Fatalf("approval accepted unconfigured gateway: %v", err)
	}
	if _, err := ReviewTicketRefundDecisionWithDB(db, admin, detail.Refund.RefundID, true, nil, testVerifyTicketRefundChannel); err != nil {
		t.Fatalf("approve grouped request: %v", err)
	}
}

func TestTicketRefundRevisionMultiOrderAndPartialSettlement(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	if err := db.AutoMigrate(&entity.TicketRefundOrderLine{}, &entity.ReferralRebate{}); err != nil {
		t.Fatal(err)
	}
	owner := seedRefundActor(t, db, 70, "revision-owner")
	admin := seedRefundActor(t, db, 71, "revision-admin")
	if err := db.Model(&admin).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	admin.Role = "admin"
	manual, err := CreateTicketRefundWithDB(db, owner, withTestRefundIdentity(t, db, owner, CreateTicketRefundInput{
		Kind: TicketRefundKindManual, RequestedCents: 1000, Reason: "原申请",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := EditTicketRefundWithDB(db, owner, manual.ID, TicketRefundRevisionInput{
		Kind: TicketRefundKindManual, RequestedCents: 990, Reason: "申请人修订",
	}, nil)
	if err != nil || changed.RequestedCents != 990 {
		t.Fatalf("owner update: %+v %v", changed, err)
	}
	first := seedTicketRefundOrder(t, db, 21, owner.ID, "paid", time.Now().Add(-2*time.Hour), 7.5, true)
	second := seedTicketRefundOrder(t, db, 22, owner.ID, "paid", time.Now().Add(-time.Hour), 7.5, true)
	revision := TicketRefundRevisionInput{Manage: true, Kind: TicketRefundKindOriginal, RequestedCents: 1000, Reason: "按两笔原路核对",
		Allocations: []TicketRefundOrderAllocation{{OrderID: first.ID, Cents: 750}, {OrderID: second.ID, Cents: 249}}}
	if _, err := EditTicketRefundWithDB(db, admin, manual.ID, revision, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundAmount) {
		t.Fatalf("underfunded allocation should fail: %v", err)
	}
	revision.Allocations[1].Cents = 250
	original, err := EditTicketRefundWithDB(db, admin, manual.ID, revision, testVerifyTicketRefundChannel)
	if err != nil || original.Kind != TicketRefundKindOriginal || *original.TicketID != *manual.TicketID {
		t.Fatalf("route conversion: %+v %v", original, err)
	}
	original, err = ReviewTicketRefundDecisionWithDB(db, admin, original.ID, true, nil, testVerifyTicketRefundChannel)
	if err != nil || original.Status != TicketRefundStatusFrozen || original.ApprovedCents != 1000 {
		t.Fatalf("review default: %+v %v", original, err)
	}
	requireTicketRefundWallet(t, db, owner.ID, 90_000_000)
	if _, _, _, err := PrepareTicketRefundLineWithDB(db, admin, original.ID, first.ID, true, testVerifyTicketRefundChannel); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := PrepareTicketRefundLineWithDB(db, admin, original.ID, first.ID, true, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundState) {
		t.Fatalf("duplicate dispatch: %v", err)
	}
	if _, err := CancelUnsentTicketRefundLineWithDB(db, admin, original.ID, first.ID); !errors.Is(err, ErrTicketRefundState) {
		t.Fatalf("an opened channel attempt cannot be canceled locally: %v", err)
	}
	revision.UnpaidConfirmed = true
	if _, err := EditTicketRefundWithDB(db, admin, original.ID, revision, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundState) {
		t.Fatalf("dispatch-claimed refund can still edit: %v", err)
	}
	if _, err := ReconcileTicketRefundLineWithDB(db, admin, original.ID, first.ID, TicketRefundStatusSucceeded, "success-first"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := PrepareTicketRefundLineWithDB(db, admin, original.ID, second.ID, true, testVerifyTicketRefundChannel); err != nil {
		t.Fatal(err)
	}
	completed, err := ReconcileTicketRefundLineWithDB(db, admin, original.ID, second.ID, TicketRefundStatusFailed, "failure-second")
	if err != nil || completed.Status != TicketRefundStatusPartial || completed.ApprovedCents != 1000 {
		t.Fatalf("partial settlement: %+v %v", completed, err)
	}
	requireTicketRefundWallet(t, db, owner.ID, 92_500_000)
	totals, err := ComputeUserLevelTotalsWithDB(db, owner.ID)
	if err != nil || totals.RechargeTotal != 7.5 {
		t.Fatalf("partial cumulative recharge: %+v %v", totals, err)
	}
	for _, order := range []entity.RechargeOrder{first, second} {
		if _, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
			Kind: TicketRefundKindOriginal, RequestedCents: 10, OrderID: &order.ID,
		}, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundOrderUnavailable) {
			t.Fatalf("relinked reserved order %d: %v", order.ID, err)
		}
	}
	ownerView, err := GetTicketWithDB(db, &owner, *original.TicketID)
	if err != nil || len(ownerView.Messages) != 3 || ownerView.Messages[2].AuthorName != "管理员" || ownerView.Messages[2].UserID != 0 ||
		!strings.Contains(ownerView.Messages[1].Body, "我修改的工单") {
		t.Fatalf("owner audit: %+v %v", ownerView.Messages, err)
	}
	adminView, err := GetTicketWithDB(db, &admin, *original.TicketID)
	if err != nil || len(adminView.Messages) != 3 || adminView.Messages[2].UserID != admin.ID ||
		!strings.Contains(adminView.Messages[2].Body, "我修改了工单。") || !strings.Contains(adminView.Messages[2].Body, first.OrderNo) {
		t.Fatalf("admin audit: %+v %v", adminView.Messages, err)
	}
}

func TestTicketRefundRevisionManualSettlementMarksChosenOrders(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	if err := db.AutoMigrate(&entity.TicketRefundOrderLine{}, &entity.ReferralRebate{}); err != nil {
		t.Fatal(err)
	}
	owner := seedRefundActor(t, db, 80, "manual-owner")
	foreign := seedRefundActor(t, db, 81, "manual-foreign")
	admin := seedRefundActor(t, db, 82, "manual-admin")
	if err := db.Model(&admin).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	order := seedTicketRefundOrder(t, db, 31, owner.ID, "paid", time.Now().Add(-time.Hour), 7.5, true)
	other := seedTicketRefundOrder(t, db, 32, foreign.ID, "paid", time.Now().Add(-2*time.Hour), 7.5, true)
	row, err := CreateTicketRefundWithDB(db, owner, withTestRefundIdentity(t, db, owner, CreateTicketRefundInput{
		Kind: TicketRefundKindManual, RequestedCents: 500,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReviewTicketRefundWithDB(db, admin, row.ID, true, 500, nil); err != nil {
		t.Fatal(err)
	}
	bad := []int32{order.ID, other.ID}
	if _, err := ReconcileManualTicketRefundWithDB(db, admin, row.ID, TicketRefundStatusSucceeded, "manual-1", &bad); !errors.Is(err, ErrTicketRefundOrderUnavailable) {
		t.Fatalf("foreign recharge order was accepted: %v", err)
	}
	chosen := []int32{order.ID}
	if completed, err := ReconcileManualTicketRefundWithDB(db, admin, row.ID, TicketRefundStatusSucceeded, "manual-1", &chosen); err != nil || completed.Status != TicketRefundStatusSucceeded {
		t.Fatalf("manual settlement: %+v %v", completed, err)
	}
	lines, err := TicketRefundLinesWithDB(db, row.ID)
	if err != nil || len(lines) != 1 || lines[0].OrderID != order.ID || lines[0].Status != TicketRefundStatusSucceeded {
		t.Fatalf("manual mark: %+v %v", lines, err)
	}
	history, err := ListRechargeOrdersWithDB(db, owner, RechargeOrderListQuery{Status: rechargeOrderStatusRefunded}, 1, 10)
	if err != nil || len(history.Data) != 1 || history.Total != 1 || history.Data[0].ID != order.ID ||
		!history.Data[0].RefundMarked || !history.Data[0].RefundReserved || history.Data[0].RefundOrderCapCents != 0 {
		t.Fatalf("marked recharge projection: %+v %v", history, err)
	}
	paid, err := ListRechargeOrdersWithDB(db, owner, RechargeOrderListQuery{Status: rechargeOrderStatusPaid}, 1, 10)
	if err != nil || len(paid.Data) != 0 {
		t.Fatalf("marked charge returned as paid: %+v %v", paid, err)
	}
	if _, err := UpdateRechargeOrderStatusWithDB(db, admin, order.ID, rechargeOrderStatusRefunded); !errors.Is(err, ErrRechargeOrderStatusInvalid) {
		t.Fatalf("refund status correction bypassed linked payout: %v", err)
	}
	if _, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
		Kind: TicketRefundKindOriginal, RequestedCents: 50, OrderID: &order.ID,
	}, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundOrderUnavailable) {
		t.Fatalf("marked manual order reused for original payout: %v", err)
	}
	requireTicketRefundWallet(t, db, owner.ID, 95_000_000)
}

func TestTicketRefundRevisionCannotReleaseLegacyOrderAtReconciliation(t *testing.T) {
	db := newTicketRefundMergeTestDB(t)
	if err := db.AutoMigrate(&entity.TicketRefundOrderLine{}); err != nil {
		t.Fatal(err)
	}
	owner := seedRefundActor(t, db, 83, "legacy-manual-owner")
	admin := seedRefundActor(t, db, 84, "legacy-manual-admin")
	if err := db.Model(&admin).Update("role", "admin").Error; err != nil {
		t.Fatal(err)
	}
	order := seedTicketRefundOrder(t, db, 33, owner.ID, "paid", time.Now().Add(-time.Hour), 7.5, true)
	row, err := CreateTicketRefundWithDB(db, owner, withTestRefundIdentity(t, db, owner, CreateTicketRefundInput{
		OrderID: &order.ID, Kind: TicketRefundKindManual, RequestedCents: 500,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReviewTicketRefundWithDB(db, admin, row.ID, true, 500, nil); err != nil {
		t.Fatal(err)
	}
	none := []int32{}
	settled, err := ReconcileManualTicketRefundWithDB(db, admin, row.ID, TicketRefundStatusSucceeded, "local-payout", &none)
	if err != nil || settled.OrderID == nil || *settled.OrderID != order.ID {
		t.Fatalf("legacy funding order was lost: %+v %v", settled, err)
	}
	if _, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
		Kind: TicketRefundKindOriginal, RequestedCents: 50, OrderID: &order.ID,
	}, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundOrderUnavailable) {
		t.Fatalf("legacy funding order reused after payout: %v", err)
	}
}
