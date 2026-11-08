package entity

import "time"

const (
	TicketRefundKindOriginal = "original"
	TicketRefundKindManual   = "manual"

	TicketRefundStatusPendingReview = "pending_review"
	TicketRefundStatusRejected      = "rejected"
	TicketRefundStatusFrozen        = "frozen"
	TicketRefundStatusProcessing    = "processing"
	TicketRefundStatusUnknown       = "unknown"
	TicketRefundStatusSucceeded     = "succeeded"
	TicketRefundStatusFailed        = "failed"
)

// TicketRefund is separate from general-purpose tickets. It never publishes
// an order callback, merchant credential or manual payment proof.
type TicketRefund struct {
	ID               int64      `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID           int32      `gorm:"column:user_id;not null;index" json:"userId"`
	OrderID          *int32     `gorm:"column:order_id;uniqueIndex" json:"orderId,omitempty"`
	Kind             string     `gorm:"column:kind;type:varchar(16);not null" json:"kind"`
	Status           string     `gorm:"column:status;type:varchar(24);not null;index" json:"status"`
	RequestedCents   int64      `gorm:"column:requested_cents;not null" json:"requestedCents"`
	ApprovedCents    int64      `gorm:"column:approved_cents;not null;default:0" json:"approvedCents"`
	OrderPaidCents   int64      `gorm:"column:order_paid_cents;not null;default:0" json:"orderPaidCents,omitempty"`
	OrderNo          string     `gorm:"column:order_no;type:varchar(64)" json:"orderNo,omitempty"`
	OriginalTradeNo  string     `gorm:"column:original_trade_no;type:varchar(128)" json:"-"`
	ChannelRef       string     `gorm:"column:channel_ref;type:varchar(255)" json:"-"`
	RefundNo         string     `gorm:"column:refund_no;type:varchar(64);not null;uniqueIndex" json:"-"`
	Reason           string     `gorm:"column:reason;type:text" json:"reason,omitempty"`
	ReviewedBy       int32      `gorm:"column:reviewed_by;not null;default:0" json:"-"`
	ReviewedAt       *time.Time `gorm:"column:reviewed_at" json:"reviewedAt,omitempty"`
	StartedAt        *time.Time `gorm:"column:started_at" json:"startedAt,omitempty"`
	ResolvedBy       int32      `gorm:"column:resolved_by;not null;default:0" json:"-"`
	ResolvedAt       *time.Time `gorm:"column:resolved_at" json:"resolvedAt,omitempty"`
	OutcomeReference string     `gorm:"column:outcome_reference;type:varchar(128)" json:"-"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func (*TicketRefund) TableName() string { return "ticket_refunds" }

// TicketRefundLedger is an append-only wallet hold journal. A confirmed
// refund consumes the prior hold without a second wallet debit.
type TicketRefundLedger struct {
	ID                 int64  `gorm:"primaryKey;autoIncrement" json:"-"`
	RefundID           int64  `gorm:"column:refund_id;not null;uniqueIndex:idx_ticket_refund_ledger_step,priority:1" json:"-"`
	UserID             int32  `gorm:"column:user_id;not null;index" json:"-"`
	Kind               string `gorm:"column:kind;type:varchar(16);not null;uniqueIndex:idx_ticket_refund_ledger_step,priority:2" json:"-"`
	AmountMicros       int64  `gorm:"column:amount_micros;not null" json:"-"`
	WalletBeforeMicros int64  `gorm:"column:wallet_before_micros;not null" json:"-"`
	WalletAfterMicros  int64  `gorm:"column:wallet_after_micros;not null" json:"-"`
	OperatorID         int32  `gorm:"column:operator_id;not null" json:"-"`
	CreatedAt          time.Time
}

func (*TicketRefundLedger) TableName() string { return "ticket_refund_ledger" }
