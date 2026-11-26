package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/itihey/tikuAdapter/internal/entity"
	"testing"
	"time"
)

func TestEffectiveLedgerOnlyConfirmedServiceCharges(t *testing.T) {
	db := newUserLevelTestDB(t)
	rows := []entity.BillingRecord{
		{Type: "normal_search", Amount: 2, GiftCharged: 2},
		{Type: ScriptActivationPurchaseBillingType, Amount: 3, BalanceBefore: 10, BalanceAfter: 7},
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
	if err := db.AutoMigrate(&entity.EffectiveAdjustment{}, &entity.TicketRefundLedger{}); err != nil {
		t.Fatal(err)
	}
	owner := entity.User{ID: 7, Username: "fixture-owner", Role: "user", Status: "enabled"}
	admin := entity.User{ID: 8, Username: "fixture-admin", Role: "admin", Status: "enabled"}
	if err := db.Create(&[]entity.User{owner, admin}).Error; err != nil {
		t.Fatal(err)
	}
	order := entity.RechargeOrder{ID: 42, UserID: 7, Amount: 10, OrderNo: "receipt", Status: "paid"}
	seedLevelPaidOrder(t, db, order)
	if err := db.Create(&entity.TicketRefund{UserID: 7, OrderID: &order.ID, Status: "succeeded", ApprovedCents: 200, RefundNo: "settled"}).Error; err != nil {
		t.Fatal(err)
	}
	var refund entity.TicketRefund
	if err := db.Where("refund_no = ?", "settled").Take(&refund).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entity.TicketRefundLedger{UserID: 7, RefundID: refund.ID, Kind: "consume", AmountMicros: 2000000}).Error; err != nil {
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
	// A second, distinct paid order is corrected before its full refund.
	other := entity.RechargeOrder{UserID: 7, ID: 43, OrderNo: "second-order", Status: "paid", Amount: 5}
	seedLevelPaidOrder(t, db, other)
	_, err = CorrectEffectiveLedger(db, admin, 7, EffectiveCorrection{SourceKind: "order", SourceID: 43, Expected: "11.000001", New: "10.000001", Revision: audit.ID, Reason: "核对第二单"})
	if err != nil {
		t.Fatal(err)
	}
	full := entity.TicketRefund{UserID: 7, OrderID: &other.ID, Status: "succeeded", ApprovedCents: 500, RefundNo: "second-full"}
	if err = db.Create(&full).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&entity.TicketRefundLedger{UserID: 7, RefundID: full.ID, Kind: "consume", AmountMicros: 5000000}).Error; err != nil {
		t.Fatal(err)
	}
	sum, err = ComputeUserLevelTotalsWithDB(db, 7)
	if err != nil || sum.RechargeTotal != 6.000001 || sum.HistoricalRecharge != 15 {
		t.Fatalf("full second refund %+v %v", sum, err)
	}
	var audits int64
	db.Model(&entity.EffectiveAdjustment{}).Count(&audits)
	if audits != 2 {
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

func TestEffectiveRebuildPreviewCursorAuditAndStaleRefusal(t *testing.T) {
	db := newUserLevelTestDB(t)
	if err := db.AutoMigrate(&entity.EffectiveAdjustment{}, &entity.EffectiveRebuildAudit{}); err != nil {
		t.Fatal(err)
	}
	admin := entity.User{ID: 1, Role: "admin", Status: "enabled", Username: "admin"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	seedLevelUser(t, db, 2)
	seedLevelUser(t, db, 3)
	basic := mustCreateLevel(t, db, entity.UserLevel{Name: "基础", Rank: 1, Enabled: true, DeviceLimit: 1})
	input := EffectiveRebuildInput{AfterID: 1, Limit: 1}
	preview, err := RebuildEffectiveLevels(db, admin, input)
	if err != nil || preview.NextID != 2 || !preview.HasMore || preview.Changed != 1 || preview.Digest == "" {
		t.Fatalf("preview %+v %v", preview, err)
	}
	id, err := CurrentUserLevelIDWithDB(db, 2)
	if err != nil || id != 0 {
		t.Fatalf("preview wrote level %d: %v", id, err)
	}
	input.ApplyDigest = preview.Digest
	input.Reason = "核对后更新"
	if err := db.Create(&entity.BillingRecord{UserID: 2, Type: "normal_search", Amount: 1, GiftCharged: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := RebuildEffectiveLevels(db, admin, input); !errors.Is(err, ErrLedgerStale) {
		t.Fatalf("stale err=%v", err)
	}
	input.ApplyDigest = ""
	preview, err = RebuildEffectiveLevels(db, admin, input)
	if err != nil {
		t.Fatal(err)
	}
	input.ApplyDigest = preview.Digest
	applied, err := RebuildEffectiveLevels(db, admin, input)
	if err != nil || applied.AuditID <= 0 {
		t.Fatalf("apply %+v %v", applied, err)
	}
	var a entity.EffectiveRebuildAudit
	if err := db.Take(&a, applied.AuditID).Error; err != nil || a.OperatorID != 1 || a.Reason != input.Reason || a.Digest != preview.Digest {
		t.Fatalf("missing audit %+v %v", a, err)
	}
	if id, _ := CurrentUserLevelIDWithDB(db, 2); id != basic.ID {
		t.Fatal(id)
	}
	if id, _ := CurrentUserLevelIDWithDB(db, 3); id != 0 {
		t.Fatal("crossed cursor")
	}
	if err := db.Create(&entity.RechargeOrder{UserID: 3, OrderNo: "unproven", Status: "paid", Amount: 100}).Error; err != nil {
		t.Fatal(err)
	}
	last, err := RebuildEffectiveLevels(db, admin, EffectiveRebuildInput{AfterID: 2, Limit: 1})
	if err != nil || last.HasMore || last.Skipped != 1 || last.Changed != 0 {
		t.Fatalf("uncertain %+v %v", last, err)
	}
}

func TestEffectiveRefundMultiLineAndUnsettledStates(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  float64
	}{
		{"pending_review", 20}, {"failed", 20}, {"rejected", 20}, {"processing", 16}, {"partially_succeeded", 16}, {"succeeded", 16},
	} {
		t.Run(tc.state, func(t *testing.T) {
			db := newUserLevelTestDB(t)
			if err := db.AutoMigrate(&entity.TicketRefundOrderLine{}, &entity.TicketRefundLedger{}); err != nil {
				t.Fatal(err)
			}
			for i := int32(1); i <= 2; i++ {
				seedLevelPaidOrder(t, db, entity.RechargeOrder{ID: i, UserID: 7, OrderNo: fmt.Sprint(i), Status: "paid", Amount: 10})
			}
			r := entity.TicketRefund{UserID: 7, RefundNo: "test-refund", Status: tc.state, ApprovedCents: 1000}
			if err := db.Create(&r).Error; err != nil {
				t.Fatal(err)
			}
			ls := []entity.TicketRefundOrderLine{{RefundID: r.ID, OrderID: 1, Status: "succeeded", Cents: 400, RefundNo: "line1"}, {RefundID: r.ID, OrderID: 2, Status: "failed", Cents: 600, RefundNo: "line2"}}
			if err := db.Create(&ls).Error; err != nil {
				t.Fatal(err)
			}
			if tc.state == "partially_succeeded" || tc.state == "succeeded" {
				if err := db.Create(&entity.TicketRefundLedger{UserID: 7, RefundID: r.ID, Kind: "consume", AmountMicros: 4000000}).Error; err != nil {
					t.Fatal(err)
				}
			}
			got, err := ComputeUserLevelTotalsWithDB(db, 7)
			if err != nil || got.RechargeTotal != tc.want || got.HistoricalRecharge != 20 {
				t.Fatalf("got %+v want %v %v", got, tc.want, err)
			}
		})
	}
}

func TestEffectiveConcurrentRefundAppliesActualReceiptOnce(t *testing.T) {
	db, owner, _, admin := newTicketRefundCoreTestDB(t)
	o := seedTicketRefundOrder(t, db, 1, owner.ID, "paid", time.Now().Add(-time.Hour), 50, true)
	seedLevelCredit(t, db, o)
	r, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{Kind: TicketRefundKindOriginal, OrderID: &o.ID, RequestedCents: 3000}, testVerifyTicketRefundChannel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ReviewTicketRefundWithDB(db, admin, r.ID, true, 3000, testVerifyTicketRefundChannel); err != nil {
		t.Fatal(err)
	}
	if _, err = BeginTicketRefundWithDB(db, admin, r.ID, testVerifyTicketRefundChannel); err != nil {
		t.Fatal(err)
	}
	ch := make(chan error, 6)
	start := make(chan struct{})
	for range 6 {
		go func() {
			<-start
			_, e := ReconcileTicketRefundWithDB(db, admin, r.ID, TicketRefundStatusSucceeded, "synthetic-same-outcome")
			ch <- e
		}()
	}
	close(start)
	for range 6 {
		if e := <-ch; e != nil {
			t.Fatal(e)
		}
	}
	got, err := ComputeUserLevelTotalsWithDB(db, owner.ID)
	if err != nil || got.RechargeTotal != 20 || got.HistoricalRecharge != 50 {
		t.Fatalf("concurrent replay %+v %v", got, err)
	}
	var receipts int64
	if err = db.Model(&entity.TicketRefundLedger{}).Where("refund_id = ? AND kind = ?", r.ID, "consume").Count(&receipts).Error; err != nil || receipts != 1 {
		t.Fatalf("consume count %d %v", receipts, err)
	}
}

func TestEffectiveRebuildRollsBackWholeBatchOnFailedEntitlementWrite(t *testing.T) {
	db := newUserLevelTestDB(t)
	if err := db.AutoMigrate(&entity.EffectiveAdjustment{}, &entity.EffectiveRebuildAudit{}); err != nil {
		t.Fatal(err)
	}
	admin := entity.User{ID: 1, Username: "admin", Status: "enabled", Role: "admin"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	seedLevelUser(t, db, 2)
	seedLevelUser(t, db, 3)
	mustCreateLevel(t, db, entity.UserLevel{Name: "basic", Rank: 1, Enabled: true})
	in := EffectiveRebuildInput{AfterID: 1, Limit: 2}
	preview, err := RebuildEffectiveLevels(db, admin, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER fail_second_level BEFORE UPDATE OF level_id ON users WHEN NEW.id=3 BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	in.ApplyDigest = preview.Digest
	in.Reason = "核对"
	if _, err = RebuildEffectiveLevels(db, admin, in); err == nil {
		t.Fatal("partial batch was acknowledged")
	}
	for _, id := range []int32{2, 3} {
		if n, e := CurrentUserLevelIDWithDB(db, id); e != nil || n != 0 {
			t.Fatalf("persisted level %d %v", n, e)
		}
	}
	for _, model := range []any{&entity.EffectiveRebuildAudit{}, &entity.UserLevelHistory{}} {
		var n int64
		if e := db.Model(model).Count(&n).Error; e != nil || n != 0 {
			t.Fatalf("partial audit: %d %v", n, e)
		}
	}
}

func TestEffectiveConsumptionRequiresActualDebitAndMicros(t *testing.T) {
	db := newUserLevelTestDB(t)
	rows := []entity.BillingRecord{
		{UserID: 7, Type: "normal_search", Amount: .0000007, GiftCharged: .000001},
		{UserID: 7, Type: "normal_search", Amount: .0000007, PrincipalCharged: .000001},
		{UserID: 7, Type: "normal_search", Amount: .0000007, GiftCharged: .000001},
		{UserID: 7, Type: "normal_search", Amount: 3, BalanceBefore: 13, BalanceAfter: 10},
		{UserID: 7, Type: "normal_search", Amount: 7},                                       // legacy amount without a debit
		{UserID: 7, Type: "normal_search", Amount: 10, BalanceBefore: 15, BalanceAfter: 10}, // debit/quote conflict
		{UserID: 7, Type: "normal_search", Amount: 2, AccessTokenID: 19, UnitPriceSnapshot: "v1 script_mode=escrow script_multiplier=1.000000 script_base_amount=2.00000000 script_charge_amount=2.000000"},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	got, err := ComputeUserLevelTotalsWithDB(db, 7)
	if err != nil || got.ConsumptionTotal != 5.000003 || got.Issues["charge_without_exact_debit"] != 2 {
		t.Fatalf("actual debit total = %+v want 5.000003 + 2 unknown, %v", got, err)
	}
}

func TestReviewMissingLedgerSourcesPreserveStoredEntitlements(t *testing.T) {
	for _, table := range []string{"billing_records", "recharge_orders", "ticket_refunds"} {
		t.Run(table, func(t *testing.T) {
			db := newUserLevelTestDB(t)
			if err := db.AutoMigrate(&entity.EffectiveAdjustment{}, &entity.EffectiveRebuildAudit{}); err != nil {
				t.Fatal(err)
			}
			admin := entity.User{ID: 1, Username: "admin", Role: "admin", Status: "enabled"}
			if err := db.Create(&admin).Error; err != nil {
				t.Fatal(err)
			}
			seedLevelUser(t, db, 2)
			mustCreateLevel(t, db, entity.UserLevel{Name: "basic", Rank: 1, Enabled: true, DeviceLimit: 1})
			high := mustCreateLevel(t, db, entity.UserLevel{Name: "legacy", Rank: 2, Enabled: true, MinRechargeTotal: 20, DeviceLimit: 9})
			if err := db.Table("users").Where("id = ?", 2).Update("level_id", high.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Migrator().DropTable(table); err != nil {
				t.Fatal(err)
			}
			s, err := RefreshUserLevelSummaryWithDB(db, 2, "reconcile")
			if err != nil || !s.LevelReviewPending || s.LevelID != high.ID {
				t.Fatalf("critical source absent, personal changed stored level: %+v %v", s, err)
			}
			p, err := RebuildEffectiveLevels(db, admin, EffectiveRebuildInput{AfterID: 1, Limit: 1})
			if err != nil || p.Skipped != 1 || p.Changed != 0 {
				t.Fatalf("bulk mismatch %+v %v", p, err)
			}
			if n, _, e := EffectiveDeviceLimit(db, 2); e != nil || n != 9 {
				t.Fatalf("slots=%d %v", n, e)
			}
		})
	}
}
func TestReviewEstimatedRefundIsNotAnActualRefund(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			db := newUserLevelTestDB(t)
			if err := db.AutoMigrate(&entity.TicketRefundOrderLine{}, &entity.TicketRefundLedger{}); err != nil {
				t.Fatal(err)
			}
			o := entity.RechargeOrder{ID: 5, UserID: 7, Status: "paid", OrderNo: "real-credit", Amount: 10}
			seedLevelPaidOrder(t, db, o)
			r := entity.TicketRefund{UserID: 7, OrderID: &o.ID, RefundNo: "fixture", Status: "succeeded", ApprovedCents: 415}
			if err := db.Create(&r).Error; err != nil {
				t.Fatal(err)
			}
			if conflict {
				if err := db.Create(&entity.TicketRefundOrderLine{RefundID: r.ID, OrderID: o.ID, RefundNo: "line", Status: "failed", Cents: 415}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&entity.TicketRefundLedger{UserID: 7, RefundID: r.ID, Kind: "consume", AmountMicros: 4149123}).Error; err != nil {
					t.Fatal(err)
				}
			}
			p, err := replayEffectiveLedger(db, 7)
			if err != nil {
				t.Fatal(err)
			}
			src := p.orders[o.ID]
			if !src.Uncertain || src.Refunded != 0 || !p.Totals.RequiresReview() {
				t.Fatalf("unproved / contradictory per-order amount reported as actual: %+v totals %+v", src, p.Totals)
			}
		})
	}
}
func TestReviewCorrectionPrecisionBound(t *testing.T) {
	for _, v := range []string{"1000000000000.000000", "1.0000001", "-0.000001"} {
		if _, err := ledgerMicros(v); err == nil {
			t.Fatal("invalid DECIMAL(18,6)", v)
		}
	}
	for _, pair := range []struct {
		s string
		n int64
	}{{"10000000000.000001", 10000000000000001}, {"10000000000.000002", 10000000000000002}, {"999999999999.999999", 999999999999999999}} {
		if got, e := ledgerMicros(pair.s); e != nil || got != pair.n {
			t.Fatalf("full existing DECIMAL requires %s=%d; got %d %v", pair.s, pair.n, got, e)
		}
	}
}
func TestReviewPrecisionBoundAppliesToDatabaseAndAudit(t *testing.T) {
	db := newUserLevelTestDB(t)
	if err := db.AutoMigrate(&entity.EffectiveAdjustment{}); err != nil {
		t.Fatal(err)
	}
	admin := entity.User{ID: 1, Role: "admin", Status: "enabled", Username: "admin"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	seedLevelUser(t, db, 2)
	// SQLite stores a whole large principal exactly as INTEGER; never require an
	// artificial account cap to make a one-micro correction on a real source.
	o := entity.RechargeOrder{ID: 3, UserID: 2, OrderNo: "edge", Amount: 10000000001, Status: "paid"}
	seedLevelPaidOrder(t, db, o)
	in := EffectiveCorrection{SourceKind: "order", SourceID: o.ID, Expected: "10000000001", New: "10000000000.000002", Reason: "精确微元"}
	a, err := CorrectEffectiveLedger(db, admin, 2, in)
	if err != nil || a.NewMicros != 10000000000000002 {
		t.Fatalf("first micro lost: %+v %v", a, err)
	}
	in.Expected = in.New
	in.New = "10000000000.000001"
	in.Revision = a.ID
	b, err := CorrectEffectiveLedger(db, admin, 2, in)
	if err != nil || b.OldMicros-b.NewMicros != 1 {
		t.Fatalf("second micro lost: %+v %v", b, err)
	}
	out, e := ExportDataWithDB(db, ExportDataOptions{Resource: ExportResourceUsers, Format: ExportFormatJSON})
	if e != nil {
		t.Fatal(e)
	}
	var rows []map[string]any
	if json.Unmarshal(out.Content, &rows) != nil || rows[1]["effective_recharge"] != "10000000000.000001" {
		t.Fatalf("lossy export %s", out.Content)
	}
}
func TestReviewNoopRebuildDigestUsedOnce(t *testing.T) {
	db := newUserLevelTestDB(t)
	if err := db.AutoMigrate(&entity.EffectiveRebuildAudit{}); err != nil {
		t.Fatal(err)
	}
	admin := entity.User{ID: 1, Role: "admin", Status: "enabled", Username: "admin"}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	seedLevelUser(t, db, 2)
	l := mustCreateLevel(t, db, entity.UserLevel{Name: "basic", Rank: 1, Enabled: true})
	if err := db.Table("users").Where("id = ?", 2).Update("level_id", l.ID).Error; err != nil {
		t.Fatal(err)
	}
	in := EffectiveRebuildInput{AfterID: 1, Limit: 1}
	p, err := RebuildEffectiveLevels(db, admin, in)
	if err != nil || p.Changed != 0 {
		t.Fatalf("preview %+v %v", p, err)
	}
	in.ApplyDigest = p.Digest
	in.Reason = "已核对"
	if _, err := RebuildEffectiveLevels(db, admin, in); err != nil {
		t.Fatal(err)
	}
	if _, err := RebuildEffectiveLevels(db, admin, in); !errors.Is(err, ErrLedgerStale) {
		t.Fatalf("noop replay wrote duplicate audit: %v", err)
	}
	var n int64
	if err := db.Model(&entity.EffectiveRebuildAudit{}).Count(&n).Error; err != nil || n != 1 {
		t.Fatalf("audits=%d %v", n, err)
	}
}

func TestReviewLifetimeSumIsNotCappedAtSingleRowWidth(t *testing.T) {
	db := newUserLevelTestDB(t)
	seedLevelUser(t, db, 2)
	basic := mustCreateLevel(t, db, entity.UserLevel{Name: "basic", Rank: 1, Enabled: true})
	for i := 0; i < 101; i++ {
		seedLevelPaidOrder(t, db, entity.RechargeOrder{UserID: 2, OrderNo: fmt.Sprintf("synthetic-large-%d", i), Status: "paid", Amount: 9999999999})
	}
	got, err := RefreshUserLevelSummaryWithDB(db, 2, "reconcile")
	if err != nil || got.LevelID != basic.ID || got.Ledger.Exact.Recharge != "1009999999899.000000" {
		t.Fatalf("lifetime sum truncated: %+v %v", got, err)
	}
	var h entity.UserLevelHistory
	if err := db.First(&h).Error; err != nil || h.RechargeExact != got.Ledger.Exact.Recharge {
		t.Fatalf("exact history materialization: %+v %v", h, err)
	}
}
func TestReviewAmbiguousLegacySQLiteRealPreservesEntitlement(t *testing.T) {
	db := newUserLevelTestDB(t)
	seedLevelUser(t, db, 2)
	o := entity.RechargeOrder{ID: 3, UserID: 2, OrderNo: "old-real", Status: "paid", Amount: 10000000000.000002}
	seedLevelPaidOrder(t, db, o)
	got, err := ComputeUserLevelTotalsWithDB(db, 2)
	if err != nil || !got.RequiresReview() || got.Issues["stored_recharge_precision_unknown"] != 1 {
		t.Fatalf("lost precision presented as known %+v %v", got, err)
	}
}
