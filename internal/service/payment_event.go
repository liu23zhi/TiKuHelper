package service

import (
	"errors"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrRechargePaymentEventUnavailable 表示「支付已入账」事件表不可用。
//
// 这是**资金路径上的结构性故障**，必须 fail-closed：没有事件表就无法证明本次
// 回调是不是首次入账，宁可不加款（网关会重投），也不能冒重复加款的风险。
// 与充值赠送/返佣的「表缺失就降级」不同 —— 那两者降级只影响「送多少」，
// 这里降级会让同一个网关订单被加款两次。
var ErrRechargePaymentEventUnavailable = errors.New("支付已入账事件表不可用，暂不能处理充值回调")

// paymentEventSourceNotify 标记事件来源是网关回调。
const paymentEventSourceNotify = "notify"

// paymentEventSchemaAvailable 判断 payment_events 是否已经迁移就绪。
func paymentEventSchemaAvailable(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	return db.Migrator().HasTable(entity.TableNamePaymentEvent)
}

// paymentEventSession 清掉继承的模型，避免 `db.Where(...)` 带上跨表条件
// （同 rechargePromotionSession / referralSession 的成因）。
func paymentEventSession(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true})
}

// claimRechargePaymentEventInTx 尝试记入「支付已入账」唯一事件。
//
// 这是加款幂等的**唯一分岔判据**（第 20.1.5 节状态机定稿要求）：
//
//   - 返回 firstCredit=true  → 本次插入真的发生了，即这笔网关订单的首次入账，
//     调用方可以继续加余额、写账单、发赠金、生成返佣；
//   - 返回 firstCredit=false → 插入被 `OnConflict{DoNothing}` 忽略，说明这笔网关订单
//     早已入过账，本次是重复回调，调用方必须直接返回成功且**一分钱都不能动**。
//
// 为什么不能只看 order.Status == "paid"：status 是可变的运营字段。管理员把订单
// 改成 refunded 之后，网关重投的同一条成功回调会因「现在不等于 paid」而再次加款 ——
// 那是真实的重复加款。事件行只增不改，不随订单状态变化而丢失，因此不会重蹈覆辙。
//
// 事务语义：必须在**加款所在的同一个事务**里调用。加款失败回滚会连带撤销事件行，
// 所以不存在「事件存在但钱没到账」的永久漏账。
//
// 并发语义：唯一索引 + DoNothing 让并发的两条同回调只有一条拿到 firstCredit=true；
// 另一条（RowsAffected==0）不加款。注意某些驱动在唯一冲突被忽略时可能返回
// ErrDuplicatedKey 而不是 0 行 —— 两种都按「非首次」处理，绝不当成错误抛出。
func claimRechargePaymentEventInTx(tx *gorm.DB, order entity.RechargeOrder, tradeNo, tradeStatus string) (bool, error) {
	if !paymentEventSchemaAvailable(tx) {
		return false, ErrRechargePaymentEventUnavailable
	}
	event := entity.PaymentEvent{
		OrderNo:         order.OrderNo,
		RechargeOrderID: order.ID,
		UserID:          order.UserID,
		// 事件只记实付本金（充值口径 2 位），不记赠金/到账总额。
		Amount:      normalizeRechargeAmount(order.Amount),
		TradeNo:     tradeNo,
		TradeStatus: tradeStatus,
		Source:      paymentEventSourceNotify,
	}
	result := paymentEventSession(tx).Clauses(clause.OnConflict{DoNothing: true}).Create(&event)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
			// 唯一冲突被吞掉的驱动形态：同样是「已入过账」。
			return false, nil
		}
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}
