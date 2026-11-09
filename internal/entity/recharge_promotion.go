package entity

import "time"

const TableNameRechargePromotionGrant = "recharge_promotion_grants"

// RechargePromotionGrant 是「充值阶梯赠送」的独立发放账本。
//
// 为什么需要独立表（而不是只写一条 billing_records）：
//   - 幂等需要一个**结构性**唯一键。billing_records 没有 (type, related_order_id)
//     唯一索引，也没法在不影响历史行的前提下加上；
//   - 充值回调是核心支付路径，唯一键冲突必须被吞掉（AGENTS.md §26.4），
//     不能把已经提交的到账事务打回滚。`recharge_order_id` 唯一 + OnConflict
//     DoNothing 正好给出这个语义；
//   - 档位/比例/门槛要在产生时快照，改配置不回溯已发放（与 referral_rebates 同构）。
//
// 口径（与 users.gift_balance 的列注释、注册赠送、评价返现完全一致）：
//
//	赠金**计入 users.balance**（可消费），同时**累加 users.gift_balance**
//	（赠金来源标记桶，只增不减，消费不扣这里）。
//	赠金**不计入可退本金 / 净实付 / 返佣基数** —— 退款与邀请返佣只认充值实付
//	本金（BaseAmount = recharge_orders.amount），否则「充值 1688 赠 3376 再退款」
//	就能把赠金套成现金。
type RechargePromotionGrant struct {
	ID int32 `gorm:"primaryKey;autoIncrement" json:"id"`
	// RechargeOrderID 唯一：一笔充值订单最多发放一次赠金。
	// 重复支付回调靠这个唯一索引 + OnConflict{DoNothing} 静默跳过。
	RechargeOrderID int32  `gorm:"column:recharge_order_id;not null;uniqueIndex" json:"rechargeOrderId"`
	OrderNo         string `gorm:"column:order_no;type:varchar(64);not null;default:''" json:"orderNo"`
	UserID          int32  `gorm:"column:user_id;not null;index" json:"userId"`
	// PaidAmount 是本笔实付本金（2 位口径，即充值订单金额），不是到账总额。
	PaidAmount float64 `gorm:"column:paid_amount;type:decimal(18,6);not null;default:0" json:"paidAmount"`
	// Threshold 是命中的档位门槛（8.8/28/88/288/888/1688），0 = 未达最低门槛。
	Threshold float64 `gorm:"column:threshold;type:decimal(18,6);not null;default:0" json:"threshold"`
	// Rate 是命中档位的赠送比例（0.10 / 0.25 / 0.50 / 1.00 / 1.50 / 2.00）。
	Rate float64 `gorm:"column:rate;type:decimal(18,8);not null;default:0" json:"rate"`
	// GiftAmount 是发放的赠金（6 位口径）。
	GiftAmount float64 `gorm:"column:gift_amount;type:decimal(18,6);not null;default:0" json:"giftAmount"`
	// BalanceBefore / BalanceAfter 是**发放赠金前/后**的钱包快照。
	// 注意两者都已包含本笔充值入账，因为赠金在充值入账之后才累加。
	BalanceBefore float64 `gorm:"column:balance_before;type:decimal(18,6);not null;default:0" json:"balanceBefore"`
	BalanceAfter  float64 `gorm:"column:balance_after;type:decimal(18,6);not null;default:0" json:"balanceAfter"`
	// PolicyVersion 是档位表口径版本快照，改档位/比例必须换值，历史行不重算。
	PolicyVersion string    `gorm:"column:policy_version;type:varchar(32);not null;default:''" json:"policyVersion"`
	Remark        string    `gorm:"column:remark;type:varchar(255);not null;default:''" json:"remark"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*RechargePromotionGrant) TableName() string {
	return TableNameRechargePromotionGrant
}
