package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func TestEligibleOrdersUseBusinessCriteriaWithoutChannelConfiguration(t *testing.T) {
	db, owner, _, admin := newTicketRefundCoreTestDB(t)
	order := seedTicketRefundOrder(t, db, 1, owner.ID, "paid", time.Now().Add(-time.Hour), 7.509999, false)
	items, err := ListEligibleTicketRefundOrdersWithDB(db, owner, nil)
	if err != nil || len(items) != 1 || !items[0].OriginalSupported || items[0].OriginalUnsupportedReason != "" || items[0].PaidCents != 750 {
		t.Fatalf("complete legacy order must be selectable: %+v, %v", items, err)
	}
	refund, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{Kind: TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 750}, nil)
	if err != nil || refund.OriginalTradeNo != order.ThirdPartyOrderNo {
		t.Fatalf("legacy order application: %+v, %v", refund, err)
	}
	if _, err := ReviewTicketRefundWithDB(db, admin, refund.ID, true, 750, nil); !errors.Is(err, ErrTicketRefundChannelDisabled) {
		t.Fatalf("unconfigured channel must block approval explicitly: %v", err)
	}
	reject := func(*gorm.DB, entity.RechargeOrder) (string, error) { return "", ErrTicketRefundOrderUnavailable }
	if _, err := ReviewTicketRefundWithDB(db, admin, refund.ID, true, 750, reject); !errors.Is(err, ErrTicketRefundOrderUnavailable) {
		t.Fatalf("mismatched payment channel must block approval: %v", err)
	}
	unchanged, err := GetTicketRefundWithDB(db, owner, refund.ID)
	if err != nil || unchanged.Status != TicketRefundStatusPendingReview || unchanged.ChannelRef != "" {
		t.Fatalf("failed approval must not bind a channel or change state: %+v, %v", unchanged, err)
	}
	requireTicketRefundWallet(t, db, owner.ID, 100_123_456)
	verify := func(_ *gorm.DB, order entity.RechargeOrder) (string, error) {
		if order.RefundOriginalTradeNo == "" {
			t.Fatal("legacy trade number was not passed to verifier")
		}
		return "configured-channel", nil
	}
	approved, err := ReviewTicketRefundWithDB(db, admin, refund.ID, true, 750, verify)
	if err != nil || approved.ChannelRef != "configured-channel" || approved.Status != TicketRefundStatusFrozen {
		t.Fatalf("configured approval must bind channel before freeze: %+v, %v", approved, err)
	}
	if _, err := BeginTicketRefundWithDB(db, admin, refund.ID, verify); err != nil {
		t.Fatalf("legacy dispatch validation: %v", err)
	}
}

func TestTicketRefundEligibilityChecksBothTimestampsAndRequiredFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	boundary := now.Add(-60 * 24 * time.Hour)
	for _, tc := range []struct {
		name   string
		edit   func(*entity.RechargeOrder)
		want   bool
		reason string
	}{
		{"inclusive-oldest", func(o *entity.RechargeOrder) { o.CreatedAt = boundary; o.PaidAt = &boundary }, true, ""},
		{"inclusive-now", func(o *entity.RechargeOrder) { o.CreatedAt = now; o.PaidAt = &now }, true, ""},
		{"provider-does-not-hide-orders", func(o *entity.RechargeOrder) { o.PaymentProvider = "legacy" }, true, ""},
		{"created-too-old", func(o *entity.RechargeOrder) { o.CreatedAt = boundary.Add(-time.Second) }, false, "创建时间超过60天"},
		{"paid-too-old", func(o *entity.RechargeOrder) { p := boundary.Add(-time.Second); o.PaidAt = &p }, false, "支付时间超过60天"},
		{"created-future", func(o *entity.RechargeOrder) { o.CreatedAt = now.Add(time.Minute) }, false, "创建时间晚于当前时间"},
		{"paid-future", func(o *entity.RechargeOrder) { p := now.Add(time.Minute); o.PaidAt = &p }, false, "支付时间晚于当前时间"},
		{"created-missing", func(o *entity.RechargeOrder) { o.CreatedAt = time.Time{} }, false, "创建时间缺失"},
		{"paid-missing", func(o *entity.RechargeOrder) { o.PaidAt = nil }, false, "支付时间缺失"},
		{"order-number-missing", func(o *entity.RechargeOrder) { o.OrderNo = "  " }, false, "缺少订单号"},
		{"payment-method-missing", func(o *entity.RechargeOrder) { o.PaymentMethodNameSnapshot = "  " }, false, "缺少支付方式"},
		{"third-party-number-missing", func(o *entity.RechargeOrder) { o.ThirdPartyOrderNo = "  " }, false, "缺少第三方单号"},
		{"refunded", func(o *entity.RechargeOrder) { o.Status = "refunded" }, false, "订单已退款"},
		{"other-nonpaid", func(o *entity.RechargeOrder) { o.Status = "closed" }, false, "订单状态不是已支付"},
		{"zero-amount", func(o *entity.RechargeOrder) { o.Amount = 0 }, false, "订单金额无效"},
		{"subcent-amount", func(o *entity.RechargeOrder) { o.Amount = .009999 }, false, "不足0.01元"},
		{"unpaid", func(o *entity.RechargeOrder) { o.Status = "pending" }, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, owner, _, _ := newTicketRefundCoreTestDB(t)
			order := seedTicketRefundOrder(t, db, 1, owner.ID, "paid", now.Add(-time.Hour), 7.5, false)
			order.CreatedAt = now.Add(-2 * time.Hour)
			tc.edit(&order)
			if err := db.Save(&order).Error; err != nil {
				t.Fatal(err)
			}
			items, err := listEligibleTicketRefundOrders(db, owner.ID, nil, now, 0)
			if err != nil {
				t.Fatal(err)
			}
			if order.Status == rechargeOrderStatusPending {
				if len(items) != 0 {
					t.Fatalf("unpaid orders must stay hidden: %+v", items)
				}
			} else if len(items) != 1 || items[0].OriginalSupported != tc.want || !strings.Contains(items[0].OriginalUnsupportedReason, tc.reason) {
				t.Fatalf("listed eligibility want %v (%s): %+v", tc.want, tc.reason, items)
			}
			if !tc.want {
				_, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{Kind: TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 100}, nil)
				if !errors.Is(err, ErrTicketRefundOrderUnavailable) {
					t.Fatalf("creation disagrees with list: %v", err)
				}
			}
		})
	}
}

func TestRefundOrderChoicesReturnAllPagesAndDisableAnyPriorRefund(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	now := time.Now()
	for i := int32(1); i <= 80; i++ {
		order := seedTicketRefundOrder(t, db, i, owner.ID, "paid", now.Add(-time.Duration(i)*time.Minute), 1, false)
		if i <= 7 {
			statuses := []string{TicketRefundStatusPendingReview, TicketRefundStatusRejected, TicketRefundStatusFrozen, TicketRefundStatusProcessing, TicketRefundStatusUnknown, TicketRefundStatusSucceeded, TicketRefundStatusFailed}
			prior := entity.TicketRefund{UserID: owner.ID, OrderID: &order.ID, Kind: TicketRefundKindOriginal, Status: statuses[i-1], RequestedCents: 1, RefundNo: fmt.Sprintf("prior-%d", i)}
			if err := db.Create(&prior).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{Kind: TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 1}, nil); !errors.Is(err, ErrTicketRefundOrderUnavailable) {
				t.Fatalf("prior refund status %s permitted reuse: %v", prior.Status, err)
			}
		}
	}
	refundQueries := 0
	if err := db.Callback().Query().After("gorm:query").Register("test:refund-choice-bulk-lookup", func(tx *gorm.DB) {
		if tx.Statement.Table == "ticket_refunds" {
			refundQueries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	items, err := ListEligibleTicketRefundOrdersWithDB(db, owner, nil)
	if err != nil || len(items) != 80 || items[0].ID != 80 || items[len(items)-1].ID != 1 {
		t.Fatalf("all nonpending orders must survive SQL page and old 50-row cap: %+v, %v", items, err)
	}
	for _, item := range items {
		if item.ID <= 7 && (item.OriginalSupported || !strings.Contains(item.OriginalUnsupportedReason, "已申请过退款")) {
			t.Fatalf("prior applications must remain visible with a reason: %+v", item)
		}
		if item.ID > 7 && !item.OriginalSupported {
			t.Fatalf("unused order must remain selectable: %+v", item)
		}
	}
	if refundQueries != 2 {
		t.Fatalf("refund use must be queried once per 64-order batch, got %d queries for 80 orders", refundQueries)
	}
}

func TestRefundOrderChoicesExplainAllMissingCriteria(t *testing.T) {
	db, owner, _, _ := newTicketRefundCoreTestDB(t)
	now := time.Now()
	order := seedTicketRefundOrder(t, db, 1, owner.ID, "paid", now.Add(-61*24*time.Hour), 1, false)
	order.CreatedAt, order.PaymentMethodNameSnapshot, order.ThirdPartyOrderNo = now.Add(-62*24*time.Hour), "", ""
	if err := db.Save(&order).Error; err != nil {
		t.Fatal(err)
	}
	items, err := ListEligibleTicketRefundOrdersWithDB(db, owner, nil)
	if err != nil || len(items) != 1 || items[0].OriginalSupported {
		t.Fatalf("unavailable order: %+v, %v", items, err)
	}
	for _, reason := range []string{"创建时间超过60天", "支付时间超过60天", "缺少支付方式", "缺少第三方单号"} {
		if !strings.Contains(items[0].OriginalUnsupportedReason, reason) {
			t.Fatalf("missing explanation %q: %+v", reason, items[0])
		}
	}
}
