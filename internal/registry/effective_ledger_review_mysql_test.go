package registry

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func TestMariaDBEffectiveReviewUsesOneReadCommittedSnapshot(t *testing.T) {
	reader := mysqlTestFreshDB(t)
	mustAutoMigrate(t, reader, &entity.User{}, &entity.RechargeOrder{}, &entity.BillingRecord{}, &entity.TicketRefund{}, &entity.EffectiveAdjustment{})
	if err := reader.Exec("SET SESSION TRANSACTION ISOLATION LEVEL READ COMMITTED").Error; err != nil {
		t.Fatal(err)
	}
	writer := openMariaDBTestDB(t, mysqlTestDSN(t))
	admin := entity.User{ID: 651, Username: "snapshot-admin", Role: "admin", Status: "enabled"}
	owner := entity.User{ID: 652, Username: "snapshot-owner", Role: "user", Status: "enabled"}
	if err := writer.Create(&[]entity.User{admin, owner}).Error; err != nil {
		t.Fatal(err)
	}
	old := entity.BillingRecord{ID: 81, UserID: owner.ID, Type: "normal_search", Amount: 2, BalanceBefore: 9, BalanceAfter: 7, RequestID: "snapshot-old"}
	newer := entity.BillingRecord{ID: 82, UserID: owner.ID, Type: "normal_search", Amount: 5, BalanceBefore: 7, BalanceAfter: 2, RequestID: "snapshot-new"}
	if err := writer.Create(&old).Error; err != nil {
		t.Fatal(err)
	}

	// Commit on a separate connection after projection, immediately before the
	// bill page query. An isolated fixture represents only an existing receipt;
	// no wallet, entitlement, refund, or upstream operation is invoked.
	const callback = "test:effective_review_concurrent_receipt"
	var inserted bool
	if err := reader.Callback().Query().Before("gorm:query").Register(callback, func(q *gorm.DB) {
		if inserted || q.Statement.Table != "billing_records" || !strings.Contains(strings.Join(q.Statement.Selects, ""), " AS wallet_before") {
			return
		}
		inserted = true
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := writer.WithContext(ctx).Create(&newer).Error; err != nil {
			_ = q.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Callback().Query().Remove(callback) })

	ids := func(review service.EffectiveReview) []int32 {
		var out []int32
		for _, source := range review.Sources {
			if source.Kind == "bill" {
				out = append(out, source.ID)
			}
		}
		return out
	}
	first, err := service.ReviewEffectiveLedger(reader, admin, owner.ID, 0)
	if err != nil || !inserted {
		t.Fatalf("did not reach the concurrent cut: inserted=%v err=%v", inserted, err)
	}
	t.Logf("in-flight exact=%s sources=%v", first.Totals.Exact.Consumption, ids(first))
	if first.Totals.Exact.Consumption != "2.000000" || !reflect.DeepEqual(ids(first), []int32{old.ID}) {
		t.Fatalf("one review mixed the cumulative projection with a later bill page")
	}
	next, err := service.ReviewEffectiveLedger(reader, admin, owner.ID, 0)
	if err != nil || next.Totals.Exact.Consumption != "7.000000" || !reflect.DeepEqual(ids(next), []int32{newer.ID, old.ID}) {
		t.Fatalf("fresh review must see the committed fixture: total=%s sources=%v err=%v", next.Totals.Exact.Consumption, ids(next), err)
	}
	t.Logf("fresh exact=%s sources=%v", next.Totals.Exact.Consumption, ids(next))
}
