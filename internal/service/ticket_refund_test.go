package service

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

func newTicketRefundCoreTestDB(t *testing.T) (*gorm.DB, entity.User, entity.User, entity.User) {
	t.Helper()
	db := newRechargeOrderTestDB(t)
	if err := db.AutoMigrate(&entity.TicketRefund{}, &entity.TicketRefundLedger{}); err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// Serialize the in-memory SQLite connection pool without disabling the
	// transaction locks/CAS. The goroutine tests still contend on a single hold.
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	owner := entity.User{ID: 91, Username: "synthetic-refund-owner", Role: "user", Status: "enabled", Balance: 100.123456}
	other := entity.User{ID: 92, Username: "synthetic-refund-other", Role: "user", Status: "enabled", Balance: 8}
	admin := entity.User{ID: 93, Username: "synthetic-refund-reviewer", Role: "admin", Status: "enabled"}
	for _, row := range []*entity.User{&owner, &other, &admin} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, owner, other, admin
}

func seedTicketRefundOrder(t *testing.T, db *gorm.DB, id int32, ownerID int32, status string, paidAt time.Time, amount float64, proven bool) entity.RechargeOrder {
	t.Helper()
	row := entity.RechargeOrder{
		ID: id, UserID: ownerID, OrderNo: "synthetic-refund-order-" + paidAt.Format(time.RFC3339Nano),
		PaymentProvider: DefaultPaymentProvider, Status: status, Amount: amount, PaidAt: &paidAt,
		ThirdPartyOrderNo: "mutable-callback-trade", CallbackPayload: "private-gateway-callback",
		// 2026-11-19：退款资格新增「有支付方式」这条硬条件
		// （见 ticketRefundOrderMeetsBasicCriteria）。夹具必须带上它，
		// 否则所有用例都会因「缺少支付方式」被过滤 —— 那不是实现坏了。
		PaymentMethodNameSnapshot: "支付宝",
	}
	if proven {
		row.RefundChannelFingerprint = strings.Repeat("a", 64)
		row.RefundOriginalTradeNo = "synthetic-verified-original-trade"
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

func testVerifyTicketRefundChannel(_ *gorm.DB, order entity.RechargeOrder) (string, error) {
	if order.RefundChannelFingerprint != strings.Repeat("a", 64) {
		return "", ErrTicketRefundOrderUnavailable
	}
	return "synthetic-verified-merchant", nil
}

func requireTicketRefundWallet(t *testing.T, db *gorm.DB, id int32, expected int64) {
	t.Helper()
	wallet, err := lockTicketRefundWallet(db, id)
	if err != nil || wallet.micros != expected {
		t.Fatalf("wallet %d: got %d want %d err=%v", id, wallet.micros, expected, err)
	}
}

func TestTicketRefundEligibilityAndFreshOwnerAdminIsolation(t *testing.T) {
	db, owner, other, admin := newTicketRefundCoreTestDB(t)
	now := time.Now()
	valid := seedTicketRefundOrder(t, db, 1, owner.ID, "paid", now.Add(-ticketRefundAge+time.Minute), 50, true)
	old := seedTicketRefundOrder(t, db, 2, owner.ID, "paid", now.Add(-ticketRefundAge-time.Minute), 30, true)
	legacy := seedTicketRefundOrder(t, db, 3, owner.ID, "paid", now.Add(-time.Hour), 30, false)
	pending := seedTicketRefundOrder(t, db, 4, owner.ID, "pending", now.Add(-2*time.Hour), 25, true)
	future := seedTicketRefundOrder(t, db, 5, owner.ID, "paid", now.Add(time.Hour), 20, true)
	foreign := seedTicketRefundOrder(t, db, 6, other.ID, "paid", now.Add(-3*time.Hour), 25, true)
	badPrecision := seedTicketRefundOrder(t, db, 7, owner.ID, "paid", now.Add(-4*time.Hour), 20.001, true)

	// Complete paid orders are selectable even when legacy channel snapshots are absent.
	eligible, err := ListEligibleTicketRefundOrdersWithDB(db, owner, testVerifyTicketRefundChannel)
	if err != nil {
		t.Fatalf("eligibility err: %v", err)
	}
	byID := make(map[int32]TicketRefundOrder, len(eligible))
	for _, item := range eligible {
		byID[item.ID] = item
	}
	// 2026-11-19 行为变更（用户要求：「充值订单可退款金额向下取整到两位小数」）：
	// 精度超过两位小数的订单不再被判为「不可退」，而是按分向下取整后列出。
	// 旧预期把它归入 absent，对应的正是「金额 20.001 的订单凭空消失」的老行为。
	if len(byID) != 5 {
		t.Fatalf("应显示全部非未支付本人订单，实际 %+v", eligible)
	}
	if got, ok := byID[valid.ID]; !ok || !got.OriginalSupported || got.PaidCents != 5000 {
		t.Fatalf("valid 必须在列且标记支持原路退回，实际 %+v", got)
	}
	if got, ok := byID[legacy.ID]; !ok || !got.OriginalSupported {
		t.Fatalf("符合业务资格的历史订单必须可选，实际 %+v", got)
	}
	if got, ok := byID[badPrecision.ID]; !ok {
		t.Fatalf("精度超标的订单必须列出（向下取整到分），实际 %+v", eligible)
	} else if got.PaidCents != 2000 {
		// 20.001 元 → floor(20.001×100) = 2000 分。断言具体分值而不只是「出现」，
		// 否则四舍五入（2000）与向上取整（2001）都能蒙混过关。
		t.Fatalf("20.001 元必须向下取整为 2000 分，实际 %d", got.PaidCents)
	}
	for _, absent := range []entity.RechargeOrder{pending, foreign} {
		if _, ok := byID[absent.ID]; ok {
			t.Fatalf("order %d 不应出现在可退列表（%s）", absent.ID, absent.OrderNo)
		}
	}
	for _, disabled := range []entity.RechargeOrder{old, future} {
		if got, ok := byID[disabled.ID]; !ok || got.OriginalSupported || got.OriginalUnsupportedReason == "" {
			t.Fatalf("不合格本人订单必须显示并说明原因，实际 %+v", got)
		}
	}
	// Current gateway configuration must not alter application eligibility.
	noVerify, err := ListEligibleTicketRefundOrdersWithDB(db, owner, nil)
	if err != nil {
		t.Fatalf("no verifier err: %v", err)
	}
	if len(noVerify) == 0 {
		t.Fatalf("渠道未配置时仍应列出订单（否则表现为列表全空），实际 %+v", noVerify)
	}
	for _, item := range noVerify {
		if item.OriginalSupported != byID[item.ID].OriginalSupported || item.OriginalUnsupportedReason != byID[item.ID].OriginalUnsupportedReason {
			t.Fatalf("渠道配置不应改变订单申请资格或原因，实际 %+v", item)
		}
	}
	for _, order := range []entity.RechargeOrder{old, pending, future, foreign} {
		if _, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
			Kind: TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 100,
		}, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundOrderUnavailable) {
			t.Fatalf("order %d should be unavailable, got %v", order.ID, err)
		}
	}
	if _, err := CreateTicketRefundWithDB(db, owner, withTestRefundIdentity(t, db, owner, CreateTicketRefundInput{
		Kind: TicketRefundKindManual, RequestedCents: 100,
	}), testVerifyTicketRefundChannel); err != nil {
		t.Fatalf("explicit manual choice must remain available: %v", err)
	}
	item, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
		Kind: TicketRefundKindOriginal, OrderID: &valid.ID, RequestedCents: 3000,
	}, testVerifyTicketRefundChannel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GetTicketRefundWithDB(db, other, item.ID); !errors.Is(err, ErrTicketRefundNotFound) {
		t.Fatalf("other owner can infer refund: %v", err)
	}
	other.Role = "admin" // stale or forged caller projection
	if _, err := ReviewTicketRefundWithDB(db, other, item.ID, true, 100, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundForbidden) {
		t.Fatalf("stale admin approval allowed: %v", err)
	}
	all, err := ListTicketRefundsWithDB(db, admin)
	if err != nil || len(all) != 2 || all[0].ID != item.ID {
		t.Fatalf("admin read: %+v %v", all, err)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{item.RefundNo, item.OriginalTradeNo, item.ChannelRef, valid.CallbackPayload} {
		if private != "" && strings.Contains(string(encoded), private) {
			t.Fatalf("private refund metadata is in client JSON")
		}
	}
	requireTicketRefundWallet(t, db, owner.ID, 100_123_456)
}

func TestTicketRefundPartialUnknownAndTerminalReplay(t *testing.T) {
	db, owner, _, admin := newTicketRefundCoreTestDB(t)
	order := seedTicketRefundOrder(t, db, 1, owner.ID, "paid", time.Now().Add(-time.Hour), 50.25, true)
	row, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
		Kind: TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 3000,
	}, testVerifyTicketRefundChannel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReviewTicketRefundWithDB(db, admin, row.ID, true, 3001, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundAmount) {
		t.Fatalf("above consent: %v", err)
	}
	if _, err := ReviewTicketRefundWithDB(db, admin, row.ID, true, 1501, nil); !errors.Is(err, ErrTicketRefundChannelDisabled) {
		t.Fatalf("unverified approval: %v", err)
	}
	requireTicketRefundWallet(t, db, owner.ID, 100_123_456)
	for i := 0; i < 2; i++ {
		got, err := ReviewTicketRefundWithDB(db, admin, row.ID, true, 1501, testVerifyTicketRefundChannel)
		if err != nil || got.Status != TicketRefundStatusFrozen || got.ApprovedCents != 1501 {
			t.Fatalf("freeze replay %d: %+v %v", i, got, err)
		}
	}
	requireTicketRefundWallet(t, db, owner.ID, 85_113_456)
	if _, err := ReconcileTicketRefundWithDB(db, admin, row.ID, TicketRefundStatusSucceeded, "synthetic-provider-ref"); !errors.Is(err, ErrTicketRefundState) {
		t.Fatalf("original terminal before submit: %v", err)
	}
	if err := db.Model(&order).Update("third_party_order_no", "mutable-duplicate-callback").Error; err != nil {
		t.Fatal(err)
	}
	start, err := BeginTicketRefundWithDB(db, admin, row.ID, testVerifyTicketRefundChannel)
	if err != nil || start.Status != TicketRefundStatusProcessing || start.RefundNo != row.RefundNo || start.OriginalTradeNo != order.RefundOriginalTradeNo {
		t.Fatalf("begin verified attempt: %+v %v", start, err)
	}
	if _, err := BeginTicketRefundWithDB(db, admin, row.ID, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundState) {
		t.Fatalf("duplicate submission permitted: %v", err)
	}
	if got, err := ReconcileTicketRefundWithDB(db, admin, row.ID, TicketRefundStatusUnknown); err != nil || got.Status != TicketRefundStatusUnknown {
		t.Fatalf("record unknown: %+v %v", got, err)
	}
	requireTicketRefundWallet(t, db, owner.ID, 85_113_456)
	if _, err := BeginTicketRefundWithDB(db, admin, row.ID, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundState) {
		t.Fatalf("unknown should not resubmit: %v", err)
	}
	for i := 0; i < 2; i++ {
		if got, err := ReconcileTicketRefundWithDB(db, admin, row.ID, TicketRefundStatusSucceeded, "synthetic-provider-ref"); err != nil || got.Status != TicketRefundStatusSucceeded {
			t.Fatalf("consume hold %d: %+v %v", i, got, err)
		}
	}
	if _, err := ReconcileTicketRefundWithDB(db, admin, row.ID, TicketRefundStatusSucceeded, "conflicting-reference"); !errors.Is(err, ErrTicketRefundState) {
		t.Fatalf("terminal reference conflict: %v", err)
	}
	if _, err := ReconcileTicketRefundWithDB(db, admin, row.ID, TicketRefundStatusFailed); !errors.Is(err, ErrTicketRefundState) {
		t.Fatalf("terminal outcome conflict: %v", err)
	}
	if _, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
		Kind: TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 100,
	}, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundOrderUnavailable) {
		t.Fatalf("partial refund should not permit a new request: %v", err)
	}
	if err := db.First(&order, order.ID).Error; err != nil || order.Status != "paid" {
		t.Fatalf("refund changed paid status: %+v %v", order, err)
	}
	eligible, err := ListEligibleTicketRefundOrdersWithDB(db, owner, testVerifyTicketRefundChannel)
	if err != nil || len(eligible) != 1 || eligible[0].OriginalSupported || !strings.Contains(eligible[0].OriginalUnsupportedReason, "已申请过退款") {
		t.Fatalf("refunded order must remain visible but disabled: %+v %v", eligible, err)
	}
	var entries []entity.TicketRefundLedger
	if err := db.Order("id").Find(&entries).Error; err != nil || len(entries) != 2 ||
		entries[0].Kind != ticketRefundLedgerFreeze || entries[1].Kind != ticketRefundLedgerConsume ||
		entries[1].AmountMicros != 15_010_000 || entries[1].WalletBeforeMicros != entries[1].WalletAfterMicros {
		t.Fatalf("refund journal: %+v %v", entries, err)
	}
	requireTicketRefundWallet(t, db, owner.ID, 85_113_456)
}

func TestTicketRefundManualFailureReturnsHoldOnceEvenIfOwnerDisabled(t *testing.T) {
	db, owner, _, admin := newTicketRefundCoreTestDB(t)
	old := seedTicketRefundOrder(t, db, 1, owner.ID, "paid", time.Now().Add(-61*24*time.Hour), 20, false)
	row, err := CreateTicketRefundWithDB(db, owner, withTestRefundIdentity(t, db, owner, CreateTicketRefundInput{
		Kind: TicketRefundKindManual, OrderID: &old.ID, RequestedCents: 2000,
	}), testVerifyTicketRefundChannel)
	if err != nil || row.ChannelRef != "" || row.OriginalTradeNo != "" {
		t.Fatalf("old order manual fallback: %+v %v", row, err)
	}
	if _, err := ReconcileTicketRefundWithDB(db, admin, row.ID, TicketRefundStatusFailed, "synthetic-manual-proof"); !errors.Is(err, ErrTicketRefundState) {
		t.Fatalf("manual unapproved reconcile: %v", err)
	}
	if _, err := ReviewTicketRefundWithDB(db, admin, row.ID, true, 1105, testVerifyTicketRefundChannel); err != nil {
		t.Fatal(err)
	}
	requireTicketRefundWallet(t, db, owner.ID, 89_073_456)
	if _, err := BeginTicketRefundWithDB(db, admin, row.ID, testVerifyTicketRefundChannel); !errors.Is(err, ErrTicketRefundState) {
		t.Fatalf("manual may not dispatch original channel: %v", err)
	}
	if err := db.Model(&owner).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if got, err := ReconcileTicketRefundWithDB(db, admin, row.ID, TicketRefundStatusFailed, "synthetic-manual-proof"); err != nil || got.Status != TicketRefundStatusFailed {
			t.Fatalf("release hold %d: %+v %v", i, got, err)
		}
	}
	requireTicketRefundWallet(t, db, owner.ID, 100_123_456)
}

func TestTicketRefundConcurrentReviewAndHoldRollback(t *testing.T) {
	db, owner, _, admin := newTicketRefundCoreTestDB(t)
	row, err := CreateTicketRefundWithDB(db, owner, withTestRefundIdentity(t, db, owner, CreateTicketRefundInput{
		Kind: TicketRefundKindManual, RequestedCents: 1000,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("synthetic hold ledger write failure")
	if err := db.Callback().Create().Before("gorm:create").Register("test:ticket-refund-ledger-rollback", func(tx *gorm.DB) {
		if tx.Statement.Table == (&entity.TicketRefundLedger{}).TableName() {
			_ = tx.AddError(fault)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReviewTicketRefundWithDB(db, admin, row.ID, true, 1000, nil); !errors.Is(err, fault) {
		t.Fatalf("ledger fault: %v", err)
	}
	requireTicketRefundWallet(t, db, owner.ID, 100_123_456)
	if got, err := GetTicketRefundWithDB(db, owner, row.ID); err != nil || got.Status != TicketRefundStatusPendingReview {
		t.Fatalf("partial state leaked on rollback: %+v %v", got, err)
	}
	if err := db.Callback().Create().Remove("test:ticket-refund-ledger-rollback"); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < cap(errs); i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := ReviewTicketRefundWithDB(db, admin, row.ID, true, 1000, nil)
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := db.Model(&entity.TicketRefundLedger{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate hold ledger: count=%d err=%v", count, err)
	}
	requireTicketRefundWallet(t, db, owner.ID, 90_123_456)
}

func TestTicketRefundCancelUnsentAndBeginAreMutuallyExclusive(t *testing.T) {
	for _, action := range []string{"cancel_first", "begin_first", "concurrent"} {
		t.Run(action, func(t *testing.T) {
			db, owner, _, admin := newTicketRefundCoreTestDB(t)
			order := seedTicketRefundOrder(t, db, 1, owner.ID, "paid", time.Now().Add(-time.Hour), 50, true)
			row, err := CreateTicketRefundWithDB(db, owner, CreateTicketRefundInput{
				Kind: TicketRefundKindOriginal, OrderID: &order.ID, RequestedCents: 1501,
			}, testVerifyTicketRefundChannel)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := CancelUnsentTicketRefundWithDB(db, admin, row.ID); !errors.Is(err, ErrTicketRefundState) {
				t.Fatalf("cancel before hold: %v", err)
			}
			if _, err := ReviewTicketRefundWithDB(db, admin, row.ID, true, 1501, testVerifyTicketRefundChannel); err != nil {
				t.Fatal(err)
			}
			if _, err := CancelUnsentTicketRefundWithDB(db, owner, row.ID); !errors.Is(err, ErrTicketRefundForbidden) {
				t.Fatalf("owner cannot cancel staff hold: %v", err)
			}
			var cancelErr, beginErr error
			cancel := func() { _, cancelErr = CancelUnsentTicketRefundWithDB(db, admin, row.ID) }
			begin := func() { _, beginErr = BeginTicketRefundWithDB(db, admin, row.ID, testVerifyTicketRefundChannel) }
			switch action {
			case "cancel_first":
				cancel()
				begin()
				if cancelErr != nil {
					t.Fatal(cancelErr)
				}
			case "begin_first":
				begin()
				cancel()
				if beginErr != nil {
					t.Fatal(beginErr)
				}
			default:
				var group sync.WaitGroup
				start := make(chan struct{})
				group.Add(2)
				go func() { defer group.Done(); <-start; cancel() }()
				go func() { defer group.Done(); <-start; begin() }()
				close(start)
				group.Wait()
			}
			got, err := GetTicketRefundWithDB(db, owner, row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if cancelErr == nil {
				if !errors.Is(beginErr, ErrTicketRefundState) || got.Status != TicketRefundStatusFailed ||
					got.OutcomeReference != TicketRefundOutcomeUnsentReference || got.StartedAt != nil {
					t.Fatalf("cancel winner: begin=%v state=%+v", beginErr, got)
				}
				if _, err := CancelUnsentTicketRefundWithDB(db, admin, row.ID); err != nil {
					t.Fatalf("idempotent cancellation: %v", err)
				}
				var count int64
				if err := db.Model(&entity.TicketRefundLedger{}).Where("refund_id = ?", row.ID).Count(&count).Error; err != nil || count != 2 {
					t.Fatalf("cancel journal: count=%d err=%v", count, err)
				}
				requireTicketRefundWallet(t, db, owner.ID, 100_123_456)
			} else {
				if beginErr != nil || !errors.Is(cancelErr, ErrTicketRefundState) ||
					got.Status != TicketRefundStatusProcessing || got.StartedAt == nil {
					t.Fatalf("begin winner: begin=%v cancel=%v state=%+v", beginErr, cancelErr, got)
				}
				requireTicketRefundWallet(t, db, owner.ID, 85_113_456)
			}
		})
	}
}
