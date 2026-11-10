package entity

import "time"

const (
	TableNameReferralInvite = "referral_invites"
	TableNameReferralRebate = "referral_rebates"

	// ReferralRebateStatusPending 表示支付成功已产生待结算奖励。
	ReferralRebateStatusPending = "pending"
	// ReferralRebateStatusSettled 表示管理员已确认结算：金额已计入受益人**赠送余额**
	// （gift_balance，可消费、不可退），可退本金（balance）不受影响。
	ReferralRebateStatusSettled = "settled"
	// ReferralRebateStatusReversed 表示原订单退款，奖励已冲回（从赠送余额扣回，clamp 到 0）。
	ReferralRebateStatusReversed = "reversed"

	// 返佣记录的来源（task_doc/70 需求 1）。管理员可以手动补录记录，
	// 手动记录没有对应的充值订单，因此 RechargeOrderID 必须可空。
	//
	// 为什么需要这个维度而不是靠「recharge_order_id 是否为 0」推断：
	//   - 它是真实业务维度（自动结算 vs 人工补发），统计「人工补发占比」要用；
	//   - MySQL 的复合唯一索引里 NULL 不参与判定，auto 的唯一性靠
	//     (source, recharge_order_id) 天然成立；SQLite 同理。
	ReferralRebateSourceAuto   = "auto"
	ReferralRebateSourceManual = "manual"
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

// ReferralRebate 是邀请返佣账本。
//
// 关键约束：
//   - 结算（settled）时金额进受益人 **gift_balance**（赠送余额，可消费不可退），
//     不进 balance —— balance 是可退款本金，返佣进这里就能被层层套现；
//   - recharge_order_id 唯一：一笔充值订单最多一条返佣，重复支付回调不会重复发放；
//   - BaseAmount 只取订单实付本金（不含赠金）：1688 充值赠 3376 时基数仍是 1688；
//   - Rate / PolicyVersion 是结算时快照，改配置不回溯已产生的记录。
type ReferralRebate struct {
	ID int32 `gorm:"primaryKey;autoIncrement" json:"id"`
	// Source 区分 auto（充值订单自动产生）与 manual（管理员手动补录）。
	// 与 RechargeOrderID 组成复合唯一索引：auto 的一单一条靠它保证，
	// manual 的 recharge_order_id 为 NULL（NULL 不参与唯一性判定）。
	Source          string     `gorm:"column:source;type:varchar(16);not null;default:auto" json:"source"`
	InviterUserID   int32      `gorm:"column:inviter_user_id;not null;index" json:"inviterUserId"`
	InvitedUserID   int32      `gorm:"column:invited_user_id;not null;index" json:"invitedUserId"`
	RechargeOrderID *int32     `gorm:"column:recharge_order_id;uniqueIndex:idx_referral_rebate_source_order" json:"rechargeOrderId,omitempty"`
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
