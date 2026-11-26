package service

import (
	"encoding/json"
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

func TestEffectiveLedgerAdjustmentAuditsAndCannotRestoreRefundedPrincipal(t *testing.T) {
	db := newUserLevelTestDB(t)
	if err := db.AutoMigrate(&entity.EffectiveAdjustment{}); err != nil {
		t.Fatal(err)
	}
	owner := entity.User{ID: 7, Username: "fixture-owner", Role: "user", Status: "enabled"}
	admin := entity.User{ID: 8, Username: "fixture-admin", Role: "admin", Status: "enabled"}
	db.Create(&owner)
	db.Create(&admin)
	order := entity.RechargeOrder{ID: 42, UserID: 7, Amount: 10, OrderNo: "receipt", Status: "paid"}
	seedLevelPaidOrder(t, db, order)
	if err := db.Create(&entity.TicketRefund{UserID: 7, OrderID: &order.ID, Status: "succeeded", ApprovedCents: 200, RefundNo: "settled"}).Error; err != nil {
		t.Fatal(err)
	}
	input := EffectiveCorrection{SourceKind: "order", SourceID: 42, Expected: "8", New: "6.000001", Reason: "原单核对"}
	if _, err := CorrectEffectiveLedger(db, owner, 7, input); err == nil {
		t.Fatal("owner cannot correct")
	}
	for _, invalid := range []string{"-1", "8.000001", "0.0000001", "NaN", ""} {
		bad := input
		bad.New = invalid
		if _, err := CorrectEffectiveLedger(db, admin, 7, bad); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
	audit, err := CorrectEffectiveLedger(db, admin, 7, input)
	if err != nil || audit.OperatorID != 8 || audit.OldMicros != 8000000 || audit.NewMicros != 6000001 || audit.RefundIDs == "" || audit.CreatedAt.IsZero() {
		t.Fatalf("audit %+v: %v", audit, err)
	}
	if _, err := CorrectEffectiveLedger(db, admin, 7, input); err == nil {
		t.Fatal("stale duplicate accepted")
	}
	sum, err := ComputeUserLevelTotalsWithDB(db, 7)
	if err != nil || sum.RechargeTotal != 6.000001 || sum.HistoricalRecharge != 10 {
		t.Fatalf("summary %+v %v", sum, err)
	}
	if err := db.Model(&entity.TicketRefund{}).Where("user_id = ?", 7).Update("approved_cents", 1000).Error; err != nil {
		t.Fatal(err)
	}
	sum, err = ComputeUserLevelTotalsWithDB(db, 7)
	if err != nil || sum.RechargeTotal != 0 || sum.HistoricalRecharge != 10 {
		t.Fatalf("full refund %+v %v", sum, err)
	}
	var audits int64
	db.Model(&entity.EffectiveAdjustment{}).Count(&audits)
	if audits != 1 {
		t.Fatalf("audits=%d", audits)
	}
}

func TestEffectiveLedgerConsumerParity(t *testing.T) {
	db := newUserLevelTestDB(t)
	if err := db.AutoMigrate(&entity.EffectiveAdjustment{}); err != nil {
		t.Fatal(err)
	}
	owner := entity.User{ID: 7, Username: "fixture-owner", Role: "user", Status: "enabled"}
	admin := entity.User{ID: 8, Username: "fixture-admin", Role: "admin", Status: "enabled"}
	if err := db.Create(&[]entity.User{owner, admin}).Error; err != nil {
		t.Fatal(err)
	}
	mustCreateLevel(t, db, entity.UserLevel{Name: "基础", Rank: 1, Enabled: true, DeviceLimit: 1})
	mustCreateLevel(t, db, entity.UserLevel{Name: "进阶", Rank: 2, Enabled: true, DeviceLimit: 3, MinConsumptionTotal: 5})
	b := entity.BillingRecord{UserID: 7, Type: "normal_search", Amount: 6, GiftCharged: 6}
	if err := db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileUserLevelWithDB(db, 7, "consumption"); err != nil {
		t.Fatal(err)
	}
	if n, _, err := EffectiveDeviceLimit(db, 7); n != 3 || err != nil {
		t.Fatalf("before slots=%d %v", n, err)
	}
	audit, err := CorrectEffectiveLedger(db, admin, 7, EffectiveCorrection{SourceKind: "bill", SourceID: b.ID, Expected: "6", New: "2", Reason: "冲正错误扣费部分"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := BuildUserPortraitWithDB(db, PortraitScope{UserID: 7})
	if err != nil {
		t.Fatal(err)
	}
	personal, err := RefreshUserLevelSummaryWithDB(db, 7, "reconcile")
	if err != nil {
		t.Fatal(err)
	}
	review, err := ReviewEffectiveLedger(db, admin, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Base.Ledger.ConsumptionTotal != 2 || personal.Ledger.ConsumptionTotal != 2 || personal.Ledger.HistoricalConsumption != 6 || review.Totals.ConsumptionTotal != 2 || review.Revision != audit.ID || len(review.Sources) != 1 || review.Sources[0].Gross != 6 {
		t.Fatalf("disagree: portrait=%+v personal=%+v admin=%+v", p.Base, personal, review)
	}
	if n, _, err := EffectiveDeviceLimit(db, 7); n != 1 || err != nil {
		t.Fatalf("after slots=%d %v", n, err)
	}
	out, err := ExportDataWithDB(db, ExportDataOptions{Resource: ExportResourceUsers, Format: ExportFormatJSON})
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Content, &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0]["effective_consumption"] != "2.000000" || rows[0]["historical_consumption"] != "6.000000" {
		t.Fatalf("export: %+v", rows[0])
	}
	undo, err := CorrectEffectiveLedger(db, admin, 7, EffectiveCorrection{SourceKind: "bill", SourceID: b.ID, Expected: "2", New: "6", Revision: audit.ID, Reason: "复核撤销部分冲正"})
	if err != nil || undo.ID == audit.ID {
		t.Fatalf("undo %+v %v", undo, err)
	}
	var original entity.BillingRecord
	if err := db.Take(&original, b.ID).Error; err != nil || original.Amount != 6 || original.GiftCharged != 6 {
		t.Fatalf("bill changed: %+v %v", original, err)
	}
}
