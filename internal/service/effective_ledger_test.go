package service

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"testing"
)

func TestEffectiveLedgerOnlyConfirmedServiceCharges(t *testing.T) {
	db := newUserLevelTestDB(t)
	rows := []entity.BillingRecord{
		{Type: "normal_search", Amount: 2, GiftCharged: 2},
		{Type: ScriptActivationPurchaseBillingType, Amount: 3},
		{Type: "normal_search", Amount: 4, RequestID: "tool-test-001", Provider: "ai", QuestionText: "[AI工具测试]"},
		{Type: scriptReservationBillingRecordType, Amount: 10}, {Type: scriptRefundBillingRecordType, Amount: 10},
		{Type: AdminGrantBillingRecordType, Amount: 20}, {Type: AdminGrantReversalBillingRecordType, Amount: 18},
		{Type: "unknown_old", Amount: 90}, {Type: "test", Amount: 1},
	}
	for i := range rows {
		rows[i].UserID = 7
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	got, err := ComputeUserLevelTotalsWithDB(db, 7)
	if err != nil || got.ConsumptionTotal != 5 {
		t.Fatalf("confirmed service charges = %v want 5; err=%v", got.ConsumptionTotal, err)
	}
}

func TestEffectiveLedgerRefundUsesPrincipalActuallyConsumed(t *testing.T) {
	db := newUserLevelTestDB(t)
	if err := db.AutoMigrate(&entity.TicketRefundLedger{}); err != nil {
		t.Fatal(err)
	}
	o := entity.RechargeOrder{UserID: 7, OrderNo: "fixture", Status: "paid", Amount: 10}
	if err := db.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.BillingRecord{UserID: o.UserID, Type: "recharge", Amount: o.Amount, RequestID: o.OrderNo, RelatedOrderID: o.ID}).Error; err != nil {
		t.Fatal(err)
	}
	r := entity.TicketRefund{UserID: 7, OrderID: &o.ID, Status: "succeeded", ApprovedCents: 415, RefundNo: "fixture-r"}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.TicketRefundLedger{RefundID: r.ID, UserID: 7, Kind: "consume", AmountMicros: 4149876}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, err := ComputeUserLevelTotalsWithDB(db, 7)
		if err != nil || got.RechargeTotal != 5.850124 {
			t.Fatalf("remaining principal = %v want 5.850124; err=%v", got.RechargeTotal, err)
		}
	}
}
