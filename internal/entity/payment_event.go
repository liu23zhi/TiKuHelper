package entity

import "time"

const TableNamePaymentEvent = "payment_events"

// PaymentEvent 是「支付已入账」的唯一事件账本（第 20.1.5 节状态机定稿要求）。
//
// 为什么需要它 —— 一个真金白银的重复加款漏洞：
//
//	旧链路的唯一判据是「订单当前 status 是否 paid」。但 status 是**可变的**：
//	管理员把订单改成 refunded（或任何非 paid 状态）之后，支付网关**重投**的同一条
//	成功回调会重新走「首次成功」分支，于是同一个网关订单被加款第二次。
//	换言之，入账幂等不能依赖一个会随运营操作变化的状态字段。
//
// 设计要点：
//   - **order_no（网关订单号 / out_trade_no）唯一**：一笔网关订单最多一条事件。
//     加款前先尝试插入；`OnConflict{DoNothing}` 下 RowsAffected==1 表示「首次入账」，
//     RowsAffected==0 表示「已经入过账，本次是重复回调」，直接返回成功不加款。
//     这与 recharge_promotion_grants.recharge_order_id / referral_rebates.recharge_order_id
//     的唯一索引 + DoNothing 完全同构，也是本仓既有的幂等写法。
//   - **只增不改**：事件一旦记入，不随订单状态变化而删除或改写 —— 没有 updated_at 列，
//     也没有任何 UPDATE 路径。订单退款、订单状态被手工改、订单行被删，事件都还在。
//   - 事件与「加款」在**同一个事务**里提交：加款失败回滚会连带删掉事件，
//     所以不会出现「事件存在但钱没到账」的永久漏账。
//
// 与 billing_records 的关系：事件是**幂等锚点**，账单是**流水展示**。
// 两者不能互相替代 —— billing_records 没有 (type, related_order_id) 唯一索引，
// 且无法在不影响历史行的前提下补上（同 recharge_promotion_grants 的注释）。
type PaymentEvent struct {
	ID int32 `gorm:"primaryKey;autoIncrement" json:"id"`
	// OrderNo 是网关订单号（我方下单时传给网关的 out_trade_no）。
	// 唯一：一笔网关订单最多入账一次，重复回调靠这个唯一索引 + OnConflict{DoNothing} 静默跳过。
	OrderNo string `gorm:"column:order_no;type:varchar(64);uniqueIndex;not null" json:"orderNo"`
	// RechargeOrderID 指向 recharge_orders.id，便于对账（不建外键约束，订单行删除后事件仍在）。
	RechargeOrderID int32 `gorm:"column:recharge_order_id;type:int(11);not null;index" json:"rechargeOrderId"`
	UserID          int32 `gorm:"column:user_id;type:int(11);not null;index" json:"userId"`
	// Amount 是本次入账的实付本金（充值口径 2 位），不是到账总额（不含赠金）。
	Amount float64 `gorm:"column:amount;type:decimal(18,6);not null;default:0" json:"amount"`
	// TradeNo 是网关交易号快照（事件发生时的值），不随后续回调变化。
	TradeNo string `gorm:"column:trade_no;type:varchar(128);not null;default:''" json:"tradeNo"`
	// TradeStatus 是触发入账的回调交易状态快照（当前只有 TRADE_SUCCESS 会入账）。
	TradeStatus string `gorm:"column:trade_status;type:varchar(32);not null;default:''" json:"tradeStatus"`
	// Source 是事件来源（notify = 网关回调）。为将来「人工补单」等来源预留区分位。
	Source string `gorm:"column:source;type:varchar(32);not null;default:''" json:"source"`
	// CreatedAt 是入账时间。**没有 UpdatedAt**：事件不可变。
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime" json:"createdAt"`
}

func (*PaymentEvent) TableName() string {
	return TableNamePaymentEvent
}
