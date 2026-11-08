package entity

import "time"

const (
	TableNameReferralInvite = "referral_invites"
	TableNameReferralRebate = "referral_rebates"

	// ReferralRebateStatusPending 表示支付成功已产生待结算奖励。
	ReferralRebateStatusPending = "pending"
	// ReferralRebateStatusSettled 表示管理员已确认结算（只改状态，不入钱包）。
	ReferralRebateStatusSettled = "settled"
	// ReferralRebateStatusReversed 表示原订单退款，奖励已冲回。
	ReferralRebateStatusReversed = "reversed"
)

// ReferralInvite 是账户的邀请码与归因来源。
//
// 一个账户只有一个邀请码（user_id 唯一），邀请码本身唯一。
// 邀请码不是 API Key，不含任何凭据，可以公开分享。
type ReferralInvite struct {
	ID     int32  `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID int32  `gorm:"column:user_id;not null;uniqueIndex" json:"userId"`
	Code   string `gorm:"column:code;type:varchar(16);not null;uniqueIndex" json:"code"`
	// CreatedAt 是邀请码生成时间，同时是归因时点的下限（注册只能绑定已存在的邀请码）。
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*ReferralInvite) TableName() string { return TableNameReferralInvite }

// ReferralRebate 是邀请返佣的独立账本。
//
// 关键约束：
//   - 不写 users.balance：返佣既不是消费余额也不是赠金，只进这张账本；
//   - recharge_order_id 唯一：一笔充值订单最多一条返佣，重复支付回调不会重复发放；
//   - BaseAmount 只取订单实付本金（不含赠金）：1688 充值赠 3376 时基数仍是 1688；
//   - Rate / PolicyVersion 是结算时快照，改配置不回溯已产生的记录。
type ReferralRebate struct {
	ID              int32      `gorm:"primaryKey;autoIncrement" json:"id"`
	InviterUserID   int32      `gorm:"column:inviter_user_id;not null;index" json:"inviterUserId"`
	InvitedUserID   int32      `gorm:"column:invited_user_id;not null;index" json:"invitedUserId"`
	RechargeOrderID int32      `gorm:"column:recharge_order_id;not null;uniqueIndex" json:"rechargeOrderId"`
	OrderNo         string     `gorm:"column:order_no;type:varchar(64);not null;default:''" json:"orderNo"`
	BaseAmount      float64    `gorm:"column:base_amount;type:decimal(18,6);not null;default:0" json:"baseAmount"`
	Rate            float64    `gorm:"column:rate;type:decimal(18,8);not null;default:0" json:"rate"`
	Amount          float64    `gorm:"column:amount;type:decimal(18,6);not null;default:0" json:"amount"`
	Status          string     `gorm:"column:status;type:varchar(24);not null;index" json:"status"`
	PolicyVersion   string     `gorm:"column:policy_version;type:varchar(32);not null;default:''" json:"policyVersion"`
	Remark          string     `gorm:"column:remark;type:varchar(255);not null;default:''" json:"remark"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
	SettledAt       *time.Time `gorm:"column:settled_at" json:"settledAt,omitempty"`
	ReversedAt      *time.Time `gorm:"column:reversed_at" json:"reversedAt,omitempty"`
}

func (*ReferralRebate) TableName() string { return TableNameReferralRebate }
