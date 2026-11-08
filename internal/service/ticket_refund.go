package service

import (
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrTicketRefundInvalid             = errors.New("退款申请参数无效")
	ErrTicketRefundForbidden           = errors.New("无权操作退款工单")
	ErrTicketRefundNotFound            = errors.New("退款工单不存在")
	ErrTicketRefundOrderUnavailable    = errors.New("充值订单不可用于此次退款")
	ErrTicketRefundState               = errors.New("当前退款状态不允许执行该操作")
	ErrTicketRefundAmount              = errors.New("退款金额不合法或超出授权上限")
	ErrTicketRefundInsufficientBalance = errors.New("可用钱包余额不足，未冻结退款")
)

const (
	TicketRefundKindOriginal = entity.TicketRefundKindOriginal
	TicketRefundKindManual   = entity.TicketRefundKindManual

	TicketRefundStatusPendingReview = entity.TicketRefundStatusPendingReview
	TicketRefundStatusRejected      = entity.TicketRefundStatusRejected
	TicketRefundStatusFrozen        = entity.TicketRefundStatusFrozen
	TicketRefundStatusProcessing    = entity.TicketRefundStatusProcessing
	TicketRefundStatusUnknown       = entity.TicketRefundStatusUnknown
	TicketRefundStatusSucceeded     = entity.TicketRefundStatusSucceeded
	TicketRefundStatusFailed        = entity.TicketRefundStatusFailed

	// A local cancellation proves that the sole dispatch claim was never
	// committed. This is not interchangeable with a gateway failure.
	TicketRefundOutcomeUnsentReference = "local-cancel-unsent"

	ticketRefundAge             = 60 * 24 * time.Hour
	ticketRefundMicrosPerCent   = int64(10_000)
	ticketRefundMaxWalletMicros = int64(999_999_999_999_999_999)
	ticketRefundMaxCents        = ticketRefundMaxWalletMicros / ticketRefundMicrosPerCent

	ticketRefundLedgerFreeze  = "freeze"
	ticketRefundLedgerConsume = "consume"
	ticketRefundLedgerRelease = "release"
)

// TicketRefundOrderVerifier checks an already-verified original merchant
// association locally. It must not perform an external request in a DB
// transaction. Return an opaque stable channel ref; empty ref and
// ErrTicketRefundOrderUnavailable are fail-closed nonmatches.
type TicketRefundOrderVerifier func(*gorm.DB, entity.RechargeOrder) (string, error)

type TicketRefundOrder struct {
	ID                        int32     `json:"id"`
	OrderNo                   string    `json:"orderNo"`
	PaidCents                 int64     `json:"paidCents"`
	PaidAt                    time.Time `json:"paidAt"`
	PaymentMethodNameSnapshot string    `json:"paymentMethodNameSnapshot"`
}

type CreateTicketRefundInput struct {
	Kind           string `json:"kind"`
	OrderID        *int32 `json:"orderId"`
	RequestedCents int64  `json:"requestedCents"`
	Reason         string `json:"reason"`
}

func ListEligibleTicketRefundOrdersWithDB(db *gorm.DB, actor entity.User, verify TicketRefundOrderVerifier) ([]TicketRefundOrder, error) {
	if db == nil {
		return nil, ErrTicketRefundInvalid
	}
	if _, err := ticketRefundActor(db, actor.ID, false, false); err != nil {
		return nil, err
	}
	return listEligibleTicketRefundOrders(db, actor.ID, verify, time.Now(), 50)
}

func ListTicketRefundsWithDB(db *gorm.DB, actor entity.User) ([]entity.TicketRefund, error) {
	if db == nil {
		return nil, ErrTicketRefundInvalid
	}
	fresh, err := ticketRefundActor(db, actor.ID, false, false)
	if err != nil {
		return nil, err
	}
	items := make([]entity.TicketRefund, 0)
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketRefund{})
	if !isAdminUser(fresh) {
		query = query.Where("user_id = ?", fresh.ID)
	}
	if err := query.Order("id DESC").Limit(100).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func GetTicketRefundWithDB(db *gorm.DB, actor entity.User, id int64) (entity.TicketRefund, error) {
	if db == nil || id <= 0 {
		return entity.TicketRefund{}, ErrTicketRefundNotFound
	}
	fresh, err := ticketRefundActor(db, actor.ID, false, false)
	if err != nil {
		return entity.TicketRefund{}, err
	}
	query := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", id)
	if !isAdminUser(fresh) {
		query = query.Where("user_id = ?", fresh.ID)
	}
	var row entity.TicketRefund
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.TicketRefund{}, ErrTicketRefundNotFound
		}
		return entity.TicketRefund{}, err
	}
	return row, nil
}

func CreateTicketRefundWithDB(db *gorm.DB, actor entity.User, input CreateTicketRefundInput, verify TicketRefundOrderVerifier) (entity.TicketRefund, error) {
	if db == nil || (input.Kind != entity.TicketRefundKindOriginal && input.Kind != entity.TicketRefundKindManual) ||
		(input.OrderID != nil && *input.OrderID <= 0) ||
		(input.Kind == entity.TicketRefundKindOriginal && input.OrderID == nil) ||
		!utf8.ValidString(input.Reason) || len(input.Reason) > 2000 || utf8.RuneCountInString(input.Reason) > 500 {
		return entity.TicketRefund{}, ErrTicketRefundInvalid
	}
	if !validTicketRefundCents(input.RequestedCents) {
		return entity.TicketRefund{}, ErrTicketRefundAmount
	}
	now := time.Now()
	row := entity.TicketRefund{
		UserID: actor.ID, Kind: input.Kind, Status: entity.TicketRefundStatusPendingReview,
		RequestedCents: input.RequestedCents, Reason: strings.TrimSpace(input.Reason), RefundNo: "tr-" + uuid.NewString(),
		CreatedAt: now, UpdatedAt: now,
	}
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var order entity.RechargeOrder
		if input.OrderID != nil {
			// Match the recharge callback's lock order: order, then user.
			var err error
			order, err = lockTicketRefundOrder(tx, actor.ID, *input.OrderID)
			if err != nil {
				return err
			}
		}
		if _, err := ticketRefundActor(tx, actor.ID, false, true); err != nil {
			return err
		}
		if input.OrderID == nil {
			eligible, err := listEligibleTicketRefundOrders(tx, actor.ID, verify, now, 1)
			if err != nil {
				return err
			}
			if len(eligible) != 0 {
				return ErrTicketRefundOrderUnavailable
			}
		} else {
			if err := ensureTicketRefundOrderUnused(tx, order.ID); err != nil {
				return err
			}
			paid, err := ticketRefundPaidOrderCents(order)
			if err != nil || order.PaidAt.After(now) {
				return ErrTicketRefundOrderUnavailable
			}
			if input.RequestedCents > paid {
				return ErrTicketRefundAmount
			}
			ref, verifyErr := ticketRefundOriginalChannel(tx, order, verify, now)
			if input.Kind == entity.TicketRefundKindOriginal {
				if verifyErr != nil {
					return verifyErr
				}
				row.ChannelRef, row.OriginalTradeNo = ref, order.RefundOriginalTradeNo
			} else if verifyErr == nil {
				// Do not silently replace an eligible original-channel refund
				// with a manual payout.
				return ErrTicketRefundOrderUnavailable
			} else if !errors.Is(verifyErr, ErrTicketRefundOrderUnavailable) {
				return verifyErr
			}
			id := order.ID
			row.OrderID, row.OrderNo, row.OrderPaidCents = &id, order.OrderNo, paid
		}
		// The nullable unique order ID also enforces this on SQLite if a
		// concurrent request races the locking-read semantics.
		if err := tx.Create(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return ErrTicketRefundOrderUnavailable
			}
			return err
		}
		return nil
	})
	if err != nil {
		return entity.TicketRefund{}, err
	}
	return row, nil
}

// ReviewTicketRefundWithDB takes an atomic wallet hold. For an orderless
// manual request the administrator must review the private evidence before
// approving; no channel or payout is called from this method.
func ReviewTicketRefundWithDB(db *gorm.DB, admin entity.User, id int64, approve bool, amountCents int64, verify TicketRefundOrderVerifier) (entity.TicketRefund, error) {
	if (!approve && amountCents != 0) || (approve && !validTicketRefundCents(amountCents)) {
		return entity.TicketRefund{}, ErrTicketRefundAmount
	}
	return changeTicketRefundWithDB(db, admin, id, func(tx *gorm.DB, row *entity.TicketRefund) error {
		if row.Status != entity.TicketRefundStatusPendingReview {
			if (!approve && row.Status == entity.TicketRefundStatusRejected) ||
				(approve && row.ApprovedCents == amountCents && row.ReviewedAt != nil) {
				return nil
			}
			return ErrTicketRefundState
		}
		now := time.Now()
		if !approve {
			return updateTicketRefund(tx, row, map[string]any{
				"status": entity.TicketRefundStatusRejected, "reviewed_at": now, "reviewed_by": admin.ID,
				"resolved_at": now, "resolved_by": admin.ID,
			})
		}
		if amountCents > row.RequestedCents || (row.OrderID != nil && amountCents > row.OrderPaidCents) {
			return ErrTicketRefundAmount
		}
		switch row.Kind {
		case entity.TicketRefundKindOriginal:
			if err := verifyTicketRefundOriginalOrderInTx(tx, *row, verify); err != nil {
				return err
			}
		case entity.TicketRefundKindManual:
			if row.OrderID != nil {
				order, err := lockTicketRefundOrder(tx, row.UserID, *row.OrderID)
				if err != nil {
					return err
				}
				paid, err := ticketRefundPaidOrderCents(order)
				if err != nil || paid != row.OrderPaidCents || order.OrderNo != row.OrderNo {
					return ErrTicketRefundOrderUnavailable
				}
			}
		default:
			return ErrTicketRefundInvalid
		}
		wallet, err := lockTicketRefundWallet(tx, row.UserID)
		if err != nil {
			return err
		}
		if !wallet.enabled {
			return ErrTicketRefundForbidden
		}
		amountMicros := amountCents * ticketRefundMicrosPerCent
		if wallet.micros < amountMicros {
			return ErrTicketRefundInsufficientBalance
		}
		after := wallet.micros - amountMicros
		if err := updateTicketRefundWallet(tx, row.UserID, wallet, after); err != nil {
			return err
		}
		if err := insertTicketRefundLedger(tx, *row, admin.ID, ticketRefundLedgerFreeze, amountMicros, wallet.micros, after); err != nil {
			return err
		}
		return updateTicketRefund(tx, row, map[string]any{
			"status": entity.TicketRefundStatusFrozen, "approved_cents": amountCents, "reviewed_at": now, "reviewed_by": admin.ID,
		})
	})
}

// BeginTicketRefundWithDB grants the right to submit a single original
// channel request. A process loss leaves processing/unknown recoverable via
// inquiry only; it never allows a blind second submission.
func BeginTicketRefundWithDB(db *gorm.DB, admin entity.User, id int64, verify TicketRefundOrderVerifier) (entity.TicketRefund, error) {
	return changeTicketRefundWithDB(db, admin, id, func(tx *gorm.DB, row *entity.TicketRefund) error {
		if row.Status != entity.TicketRefundStatusFrozen || row.Kind != entity.TicketRefundKindOriginal {
			return ErrTicketRefundState
		}
		if err := verifyTicketRefundOriginalOrderInTx(tx, *row, verify); err != nil {
			return err
		}
		if _, err := ticketRefundHoldMicros(tx, *row); err != nil {
			return err
		}
		return updateTicketRefund(tx, row, map[string]any{"status": entity.TicketRefundStatusProcessing, "started_at": time.Now()})
	})
}

// CancelUnsentTicketRefundWithDB releases an original-channel hold only
// before a dispatch claim exists. The lock and state CAS compete with Begin:
// cancellation is never available for processing or unknown outcomes.
func CancelUnsentTicketRefundWithDB(db *gorm.DB, admin entity.User, id int64) (entity.TicketRefund, error) {
	return changeTicketRefundWithDB(db, admin, id, func(tx *gorm.DB, row *entity.TicketRefund) error {
		if row.Kind != entity.TicketRefundKindOriginal || row.StartedAt != nil {
			return ErrTicketRefundState
		}
		if row.Status == entity.TicketRefundStatusFailed && row.OutcomeReference == TicketRefundOutcomeUnsentReference {
			return nil
		}
		if row.Status != entity.TicketRefundStatusFrozen {
			return ErrTicketRefundState
		}
		amountMicros, err := ticketRefundHoldMicros(tx, *row)
		if err != nil {
			return err
		}
		return finalizeTicketRefundInTx(tx, row, admin.ID, entity.TicketRefundStatusFailed,
			TicketRefundOutcomeUnsentReference, amountMicros)
	})
}

// ReconcileTicketRefundWithDB applies evidence, not an instruction to pay.
// Original-channel outcomes must come from the caller's verified channel
// response or inquiry. Manual terminal outcomes are staff confirmations
// of an out-of-band payment or its definitive failure.
func ReconcileTicketRefundWithDB(db *gorm.DB, admin entity.User, id int64, outcome string, reference ...string) (entity.TicketRefund, error) {
	if outcome != entity.TicketRefundStatusUnknown && outcome != entity.TicketRefundStatusSucceeded && outcome != entity.TicketRefundStatusFailed {
		return entity.TicketRefund{}, ErrTicketRefundState
	}
	ref := ""
	if len(reference) > 1 {
		return entity.TicketRefund{}, ErrTicketRefundInvalid
	}
	if len(reference) == 1 {
		ref = strings.TrimSpace(reference[0])
		if ref != "" && !validTicketRefundReference(ref, 128) {
			return entity.TicketRefund{}, ErrTicketRefundInvalid
		}
	}
	return changeTicketRefundWithDB(db, admin, id, func(tx *gorm.DB, row *entity.TicketRefund) error {
		if row.Status == entity.TicketRefundStatusSucceeded || row.Status == entity.TicketRefundStatusFailed {
			if row.Status == outcome && (ref == "" || ref == row.OutcomeReference) {
				return nil
			}
			return ErrTicketRefundState
		}
		if row.Kind != entity.TicketRefundKindOriginal && row.Kind != entity.TicketRefundKindManual {
			return ErrTicketRefundInvalid
		}
		if row.Status != entity.TicketRefundStatusProcessing && row.Status != entity.TicketRefundStatusUnknown &&
			!(row.Kind == entity.TicketRefundKindManual && row.Status == entity.TicketRefundStatusFrozen) {
			return ErrTicketRefundState
		}
		amountMicros, err := ticketRefundHoldMicros(tx, *row)
		if err != nil {
			return err
		}
		if outcome == entity.TicketRefundStatusUnknown {
			if row.Status == outcome {
				return nil
			}
			return updateTicketRefund(tx, row, map[string]any{"status": outcome})
		}
		return finalizeTicketRefundInTx(tx, row, admin.ID, outcome, ref, amountMicros)
	})
}

func finalizeTicketRefundInTx(tx *gorm.DB, row *entity.TicketRefund, operatorID int32, outcome, ref string, amountMicros int64) error {
	// A disabled account does not invalidate a payout that already
	// happened, nor stop a definitive failure from restoring its hold.
	wallet, err := lockTicketRefundWallet(tx, row.UserID)
	if err != nil {
		return err
	}
	after, kind := wallet.micros, ticketRefundLedgerConsume
	if outcome == entity.TicketRefundStatusFailed {
		kind = ticketRefundLedgerRelease
		if wallet.micros > ticketRefundMaxWalletMicros-amountMicros {
			return ErrTicketRefundAmount
		}
		after += amountMicros
		if err := updateTicketRefundWallet(tx, row.UserID, wallet, after); err != nil {
			return err
		}
	}
	if err := insertTicketRefundLedger(tx, *row, operatorID, kind, amountMicros, wallet.micros, after); err != nil {
		return err
	}
	// 退款成功 → 冲回该订单产生的邀请返佣（同一事务，可追踪）。
	// 只改 referral_rebates 的状态，不动任何钱包：返佣是独立账本，
	// 已 settled 的记录也照样标记 reversed 并写 reversed_at。
	if outcome == entity.TicketRefundStatusSucceeded && row.OrderID != nil && *row.OrderID > 0 {
		if _, err := ReverseReferralRebateForOrderInTx(tx, *row.OrderID, "原订单退款，返佣已冲回"); err != nil {
			return err
		}
	}
	return updateTicketRefund(tx, row, map[string]any{
		"status": outcome, "resolved_at": time.Now(), "resolved_by": operatorID, "outcome_reference": ref,
	})
}

func listEligibleTicketRefundOrders(db *gorm.DB, userID int32, verify TicketRefundOrderVerifier, now time.Time, want int) ([]TicketRefundOrder, error) {
	items := make([]TicketRefundOrder, 0)
	if verify == nil {
		return items, nil
	}
	var lastID int32
	for {
		var orders []entity.RechargeOrder
		query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.RechargeOrder{}).
			Where("user_id = ? AND status = ? AND payment_provider = ?", userID, rechargeOrderStatusPaid, DefaultPaymentProvider).
			Where("paid_at >= ? AND paid_at <= ?", now.Add(-ticketRefundAge), now).
			Where("refund_channel_fingerprint <> '' AND refund_original_trade_no <> ''").
			Where("NOT EXISTS (SELECT 1 FROM ticket_refunds WHERE ticket_refunds.order_id = recharge_orders.id)")
		if lastID != 0 {
			query = query.Where("id < ?", lastID)
		}
		if err := query.Order("id DESC").Limit(64).Find(&orders).Error; err != nil {
			return nil, err
		}
		for _, order := range orders {
			_, err := ticketRefundOriginalChannel(db, order, verify, now)
			if errors.Is(err, ErrTicketRefundOrderUnavailable) {
				continue
			}
			if err != nil {
				return nil, err
			}
			paid, _ := ticketRefundPaidOrderCents(order)
			items = append(items, TicketRefundOrder{
				ID: order.ID, OrderNo: order.OrderNo, PaidCents: paid, PaidAt: *order.PaidAt,
				PaymentMethodNameSnapshot: order.PaymentMethodNameSnapshot,
			})
			if len(items) == want {
				return items, nil
			}
		}
		if len(orders) < 64 {
			return items, nil
		}
		lastID = orders[len(orders)-1].ID
	}
}

func ticketRefundOriginalChannel(db *gorm.DB, order entity.RechargeOrder, verify TicketRefundOrderVerifier, asOf time.Time) (string, error) {
	if _, err := ticketRefundPaidOrderCents(order); err != nil ||
		order.PaidAt.Before(asOf.Add(-ticketRefundAge)) || order.PaidAt.After(asOf) ||
		order.PaymentProvider != DefaultPaymentProvider ||
		!validTicketRefundReference(order.RefundOriginalTradeNo, 128) ||
		!validTicketRefundReference(order.RefundChannelFingerprint, 255) || verify == nil {
		return "", ErrTicketRefundOrderUnavailable
	}
	ref, err := verify(db, order)
	if err != nil {
		return "", err
	}
	if !validTicketRefundReference(ref, 255) {
		return "", ErrTicketRefundOrderUnavailable
	}
	return ref, nil
}

func verifyTicketRefundOriginalOrderInTx(tx *gorm.DB, row entity.TicketRefund, verify TicketRefundOrderVerifier) error {
	if row.OrderID == nil || row.CreatedAt.IsZero() {
		return ErrTicketRefundOrderUnavailable
	}
	order, err := lockTicketRefundOrder(tx, row.UserID, *row.OrderID)
	if err != nil {
		return err
	}
	ref, err := ticketRefundOriginalChannel(tx, order, verify, row.CreatedAt)
	if err != nil {
		return err
	}
	paid, _ := ticketRefundPaidOrderCents(order)
	if row.ChannelRef != ref || row.OrderNo != order.OrderNo || row.OriginalTradeNo != order.RefundOriginalTradeNo ||
		row.OrderPaidCents != paid {
		return ErrTicketRefundOrderUnavailable
	}
	return nil
}

func ticketRefundPaidOrderCents(order entity.RechargeOrder) (int64, error) {
	v := order.Amount
	if order.Status != rechargeOrderStatusPaid || order.PaidAt == nil || order.PaidAt.IsZero() ||
		!validTicketRefundReference(order.OrderNo, 64) || math.IsNaN(v) || math.IsInf(v, 0) ||
		v <= 0 || v > float64(ticketRefundMaxCents)/100 {
		return 0, ErrTicketRefundOrderUnavailable
	}
	cents := int64(math.Round(v * 100))
	if !validTicketRefundCents(cents) || v != float64(cents)/100 {
		return 0, ErrTicketRefundOrderUnavailable
	}
	return cents, nil
}

func validTicketRefundCents(cents int64) bool {
	return cents > 0 && cents <= ticketRefundMaxCents
}

func validTicketRefundReference(raw string, max int) bool {
	if raw == "" || len(raw) > max || raw != strings.TrimSpace(raw) || !utf8.ValidString(raw) {
		return false
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func lockTicketRefundOrder(tx *gorm.DB, userID, id int32) (entity.RechargeOrder, error) {
	var order entity.RechargeOrder
	err := tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND user_id = ?", id, userID).Take(&order).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return entity.RechargeOrder{}, ErrTicketRefundOrderUnavailable
	}
	return order, err
}

func ensureTicketRefundOrderUnused(tx *gorm.DB, orderID int32) error {
	var row entity.TicketRefund
	err := tx.Session(&gorm.Session{NewDB: true}).Select("id").Where("order_id = ?", orderID).Take(&row).Error
	if err == nil {
		return ErrTicketRefundOrderUnavailable
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

func ticketRefundActor(db *gorm.DB, id int32, requireAdmin, lock bool) (entity.User, error) {
	if id <= 0 {
		return entity.User{}, ErrTicketRefundForbidden
	}
	var user entity.User
	query := db.Session(&gorm.Session{NewDB: true}).Select("id", "role", "status").Where("id = ?", id)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.User{}, ErrTicketRefundForbidden
		}
		return entity.User{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(user.Status), "enabled") || (requireAdmin && !isAdminUser(user)) {
		return entity.User{}, ErrTicketRefundForbidden
	}
	return user, nil
}

func changeTicketRefundWithDB(db *gorm.DB, admin entity.User, id int64, mutate func(*gorm.DB, *entity.TicketRefund) error) (entity.TicketRefund, error) {
	if db == nil || id <= 0 {
		return entity.TicketRefund{}, ErrTicketRefundNotFound
	}
	var row entity.TicketRefund
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if _, err := ticketRefundActor(tx, admin.ID, true, false); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTicketRefundNotFound
			}
			return err
		}
		return mutate(tx, &row)
	})
	if err != nil {
		return entity.TicketRefund{}, err
	}
	return row, nil
}

func updateTicketRefund(tx *gorm.DB, row *entity.TicketRefund, updates map[string]any) error {
	result := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.TicketRefund{}).
		Where("id = ? AND status = ?", row.ID, row.Status).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTicketRefundState
	}
	return tx.Session(&gorm.Session{NewDB: true}).Where("id = ?", row.ID).Take(row).Error
}

type ticketRefundWallet struct {
	raw     string
	micros  int64
	enabled bool
}

func lockTicketRefundWallet(tx *gorm.DB, userID int32) (ticketRefundWallet, error) {
	// SQL decimal text and verified write-back preserve the wallet's sixth
	// decimal on supported databases; float64 is not a ledger unit.
	var value struct {
		BalanceText sql.NullString
		Status      string
	}
	expr := "CAST(balance AS CHAR) AS balance_text"
	if tx.Dialector.Name() == "sqlite" {
		expr = "CAST(balance AS TEXT) AS balance_text"
	}
	err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).Select(expr+", status").
		Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", userID).Take(&value).Error
	if err != nil {
		return ticketRefundWallet{}, err
	}
	if !value.BalanceText.Valid {
		return ticketRefundWallet{}, ErrTicketRefundAmount
	}
	// Reuse decimal arithmetic only, not the unrelated script escrow model,
	// release path, or transaction journal.
	micros, err := parseScriptEscrowMicros(value.BalanceText.String)
	if err != nil {
		return ticketRefundWallet{}, ErrTicketRefundAmount
	}
	return ticketRefundWallet{
		raw: value.BalanceText.String, micros: micros,
		enabled: strings.EqualFold(strings.TrimSpace(value.Status), "enabled"),
	}, nil
}

func updateTicketRefundWallet(tx *gorm.DB, userID int32, before ticketRefundWallet, after int64) error {
	if after < -ticketRefundMaxWalletMicros || after > ticketRefundMaxWalletMicros {
		return ErrTicketRefundAmount
	}
	result := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
		Where("id = ? AND balance = ?", userID, before.raw).Update("balance", formatScriptEscrowMicros(after))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTicketRefundState
	}
	fresh, err := lockTicketRefundWallet(tx, userID)
	if err != nil {
		return err
	}
	if fresh.micros != after {
		return ErrTicketRefundAmount
	}
	return nil
}

func insertTicketRefundLedger(tx *gorm.DB, row entity.TicketRefund, operatorID int32, kind string, amount, before, after int64) error {
	return tx.Session(&gorm.Session{NewDB: true}).Create(&entity.TicketRefundLedger{
		RefundID: row.ID, UserID: row.UserID, Kind: kind, AmountMicros: amount,
		WalletBeforeMicros: before, WalletAfterMicros: after, OperatorID: operatorID,
	}).Error
}

func ticketRefundHoldMicros(tx *gorm.DB, row entity.TicketRefund) (int64, error) {
	if !validTicketRefundCents(row.ApprovedCents) || row.ApprovedCents > row.RequestedCents ||
		(row.OrderID != nil && row.ApprovedCents > row.OrderPaidCents) {
		return 0, ErrTicketRefundAmount
	}
	amount := row.ApprovedCents * ticketRefundMicrosPerCent
	var held entity.TicketRefundLedger
	err := tx.Session(&gorm.Session{NewDB: true}).Where("refund_id = ? AND kind = ?", row.ID, ticketRefundLedgerFreeze).Take(&held).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, ErrTicketRefundState
	}
	if err != nil {
		return 0, err
	}
	if held.UserID != row.UserID || held.AmountMicros != amount || held.WalletBeforeMicros-held.WalletAfterMicros != amount {
		return 0, ErrTicketRefundState
	}
	return amount, nil
}
