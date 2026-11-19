package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestTicketRefundSummaryFreshWalletAndRounding(t *testing.T) {
	for _, test := range []struct {
		name                string
		balance             float64
		refundableCents     int64
		accountBalanceCents int64
	}{
		{"fractional cent", 10.004, 1001, 101001},
		{"exact cent", 10.01, 1001, 101001},
		{"smallest wallet unit", 0.000001, 1, 100001},
		{"zero principal with gift balance", 0, 0, 100000},
		{"debt with gift balance", -12.345678, 0, 98766},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, owner, _ := newRefundablePrincipalTestDB(t, 999, 1000)
			if err := db.Model(&entity.User{}).Where("id = ?", owner.ID).Update("balance", test.balance).Error; err != nil {
				t.Fatal(err)
			}
			summary, err := TicketRefundSummaryWithDB(db, owner)
			if err != nil || summary.RefundableCents != test.refundableCents || summary.BalanceCents != test.accountBalanceCents {
				t.Fatalf("summary=%+v want refundable=%d balance=%d err=%v", summary, test.refundableCents, test.accountBalanceCents, err)
			}
			if summary.NonRefundableCents != test.accountBalanceCents-test.refundableCents {
				t.Fatalf("non-refundable breakdown mismatch: %+v", summary)
			}
			if test.balance == 0 && !strings.Contains(summary.NonRefundableReason, "赠送余额") {
				t.Fatalf("gift-only account must explain why it cannot be refunded: %+v", summary)
			}
			var fresh entity.User
			if err := db.First(&fresh, owner.ID).Error; err != nil || fresh.Balance != test.balance || fresh.GiftBalance != 1000 {
				t.Fatalf("summary changed wallet: %+v %v", fresh, err)
			}
			var count int64
			if err := db.Model(&entity.TicketRefund{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("summary created refund: %d %v", count, err)
			}
			if err := db.Model(&entity.TicketRefundLedger{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("summary changed ledger: %d %v", count, err)
			}
		})
	}
}

func TestTicketRefundSummaryDeductsConsumedGiftsOnlyForCurrentActor(t *testing.T) {
	db, owner, other := newRefundablePrincipalTestDB(t, 25.000001, 500)
	seedConsumption(t, db, owner.ID, "summary-consumed-gifts", 20, 15, 5)
	seedConsumption(t, db, other.ID, "summary-other-account", 100, 99, 1)
	summary, err := TicketRefundSummaryWithDB(db, owner)
	if err != nil || summary.RefundableCents != 1001 || summary.BalanceCents != 52501 || summary.NonRefundableCents != 51500 {
		t.Fatalf("gift consumption not deducted: %+v %v", summary, err)
	}
	if !strings.Contains(summary.NonRefundableReason, "赠送余额 500 元不可退款") || !strings.Contains(summary.NonRefundableReason, "赠金已消费 15 元，实际抵减可退本金 15 元") {
		t.Fatalf("summary must explain gift amounts: %+v", summary)
	}
	seedConsumption(t, db, owner.ID, "summary-exhausted-gifts", 11, 11, 0)
	summary, err = TicketRefundSummaryWithDB(db, owner)
	if err != nil || summary.RefundableCents != 0 || summary.NonRefundableCents != summary.BalanceCents || !strings.Contains(summary.NonRefundableReason, "赠金消耗已抵扣全部可退本金") {
		t.Fatalf("gift consumption exceeded wallet: %+v %v", summary, err)
	}
}

func TestTicketRefundSummaryRejectsStaleOrMissingIdentityAndDBFailures(t *testing.T) {
	db, owner, _ := newRefundablePrincipalTestDB(t, 25, 0)
	for _, actor := range []entity.User{{ID: 0}, {ID: 999999, Status: "enabled"}} {
		if _, err := TicketRefundSummaryWithDB(db, actor); !errors.Is(err, ErrTicketRefundForbidden) {
			t.Fatalf("missing identity accepted: %v", err)
		}
	}
	if err := db.Model(&owner).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := TicketRefundSummaryWithDB(db, owner); !errors.Is(err, ErrTicketRefundForbidden) {
		t.Fatalf("cached enabled user bypassed current status: %v", err)
	}
	if _, err := TicketRefundSummaryWithDB(nil, owner); !errors.Is(err, ErrTicketRefundInvalid) {
		t.Fatalf("nil DB: %v", err)
	}
	if err := db.Model(&owner).Update("status", "enabled").Error; err != nil {
		t.Fatal(err)
	}
	failure := errors.New("synthetic summary wallet query failure")
	if err := db.Callback().Query().Before("gorm:query").Register("refund_summary_wallet_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameUser && len(tx.Statement.Selects) > 0 && tx.Statement.Selects[0] == "balance" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("refund_summary_wallet_failure") })
	if summary, err := TicketRefundSummaryWithDB(db, owner); !errors.Is(err, failure) || summary.RefundableCents != 0 {
		t.Fatalf("failed calculation returned misleading value: %+v %v", summary, err)
	}
}
