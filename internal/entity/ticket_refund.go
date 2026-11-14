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

// TicketRefund 是退款申请，**不是**通用工单：它有自己的 7 态状态机
// （pending_review → frozen → processing → succeeded/failed），且每次状态迁移都带
// 钱包 hold / ledger 写入。它从不出订单回调、商户凭据或手工付款凭证。
//
// # 与 tickets 的关系（T-209 合并，69-*.md §8.4 的裁决）
//
// 用户要求「退款工单直接合并到工单里面」。合并方式是**壳工单 + 单向关联**，
// 不是把两张表并成一张：
//   - 每笔退款申请在同一事务里建一张 `tickets` 行（category=refund，origin=user），
//     于是它天然出现在工单列表里、能被统一筛选/搜索/通知；
//   - 退款状态仍只存在本表的 `Status` 列，**不写进 tickets.status** ——
//     两套状态机不同构（工单 4 态 vs 退款 7 态），硬塞会让
//     「frozen/processing/unknown」无处可放，而这三个状态正是「资金还冻着」的唯一标识，
//     丢掉它们等于让管理员无法判断该不该重试打款；
//   - 关联方向是**本表 → tickets**（TicketID），不是 tickets → ticket_refunds：
//     一张工单最多对应一笔退款，反向唯一索引放在被引用侧更容易保证「一笔退款一张单」。
//
// TicketID 设计成**可空**：退款先落库、壳工单后建（或迁移前的老数据）时它暂时为空，
// 迁移会补齐；唯一索引允许多个 NULL（SQLite / MySQL 都如此），
// 所以「未关联」不会互相冲突，而「已关联」保证一笔退款只挂一张单。
type TicketRefund struct {
	ID int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	// TicketID 指向 ticket_refunds 对应的壳工单（tickets.id）。
	// json 里下发，前端据此从工单列表跳到退款详情。
	TicketID         *int64     `gorm:"column:ticket_id;uniqueIndex" json:"ticketId,omitempty"`
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
