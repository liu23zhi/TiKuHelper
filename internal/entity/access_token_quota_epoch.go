package entity

import "time"

const TableNameAccessTokenQuotaEpoch = "access_token_quota_epochs"

// AccessTokenQuotaEpoch 是访问令牌「额度周期」的**叠加层**（P14 / sec20 §20.1.7 第 16 条决策）。
//
// 为什么是叠加层，而不是把 access_tokens.total_amount_used 清零：
//   - total_amount_used 是**历史累计**，账单、对账、审计都以它为据；
//     清零等于丢掉账单依据，而且这个字段还参与额度判定的比较，
//     清零后「这个令牌一共花过多少」这个问题就再也答不出来了；
//   - 清零是破坏性写，写错无法回滚；叠加层只追加一行，随时可以回退；
//   - 历史行不需要回填：没有 epoch 行的令牌天然是「从未重置」。
//
// 口径（本表的全部语义就这三行）：
//
//	本轮已用 = access_tokens.total_amount_used - baseline_amount（当前 epoch 的基线）
//	重置     = 追加一行 epoch + 1，baseline_amount = 重置那一刻的 total_amount_used
//	total_amount_used 永不清零 —— 这是本设计的红线，也是与「直接清零」方案的关键区别
//
// epoch 从 1 开始。没有任何行的令牌视为 epoch 0 / baseline 0，
// 于是「本轮已用」恰好等于「历史累计」，与重置功能上线前的行为完全一致
// （对既有额度判定逻辑的行为变化为零）。
type AccessTokenQuotaEpoch struct {
	ID int32 `gorm:"primaryKey;autoIncrement" json:"id"`
	// TokenID + Epoch 唯一：并发重置时双方会算出同一个 epoch，
	// 唯一索引让只有一方插入成功，另一方拿到 0 行影响并返回冲突错误。
	TokenID int32 `gorm:"column:token_id;not null;uniqueIndex:uk_access_token_quota_epochs_token_epoch" json:"tokenId"`
	Epoch   int32 `gorm:"column:epoch;not null;uniqueIndex:uk_access_token_quota_epochs_token_epoch" json:"epoch"`
	// ResetAt 是本次重置发生的时刻（上海时区），与 created_at 分开保留：
	// 重置时间属于业务口径，created_at 属于写入口径。
	ResetAt time.Time `gorm:"column:reset_at;not null" json:"resetAt"`
	// BaselineAmount 是重置那一刻 total_amount_used 的快照（6 位口径，
	// 一律过 util.RoundMoney）。本轮已用就是拿它做减法。
	BaselineAmount float64 `gorm:"column:baseline_amount;type:decimal(18,6);not null;default:0" json:"baselineAmount"`
	// OperatorID 是执行重置的管理员；EventID 把这一行与审计日志串起来。
	OperatorID int32     `gorm:"column:operator_id;not null;default:0" json:"operatorId"`
	EventID    string    `gorm:"column:event_id;type:varchar(64);not null;default:''" json:"eventId"`
	CreatedAt  time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*AccessTokenQuotaEpoch) TableName() string {
	return TableNameAccessTokenQuotaEpoch
}
