package service

import (
	"math"
	"strconv"
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 可退本金（refundable principal）的口径与推导。
//
// 背景（21-*.md §7.1 用户诉求「用户只能退款自己充值的余额」）：
// 双桶模型下 `balance` 是充值本金（可退）、`gift_balance` 是赠送金额（不可退但可消费），
// 消费**优先扣赠金**。若退款只看订单实付，就会出现：
//
//	充 100 → 赠 20 → 花掉 20 赠金 → 余额仍是 100 → 退 100
//	平台收 100、退 100、白给 20 元服务 → **净亏 20**
//
// 正确口径必须让「用户取走的总价值」不超过「实付」：
//
//	现金退款 + 已消耗服务价值 ≤ 实付
//
// 推导（关键恒等式）：
//
//	balance = 实付合计 − 已退合计 − 已消耗本金            （本金只被消费与退款减少）
//	可退额  = balance − 已消耗赠金                        ← 本文件实现的口径
//
// 代入验证「用户取走的总价值」：
//
//	可退额 + 已消耗本金 + 已消耗赠金
//	  = (balance − 赠金消耗) + 本金消耗 + 赠金消耗
//	  = balance + 本金消耗
//	  = 实付合计 − 已退合计
//
// 即「取走的总价值 == 实付净额」，**恒等为零利润**。任何其他口径都会留下敞口。
//
// 为什么用 `balance − 已消耗赠金` 而不是「实付 − 全部消耗」：
// 前者只需读一个钱包列 + 一个聚合，天然包含历史退款的影响（balance 已经反映过），
// 不需要再单独减「已退合计」，少一处可能算错的地方。
//
// ## 历史行（加列之前的消费）怎么处理 —— 不需要区分「未知」
//
// `billing_records.gift_charged` 是双桶上线时新增的列，历史行默认 0。
// 看似需要 sentinel 区分「0 = 未知」与「0 = 确实没消耗赠金」，但实际上**不需要**：
//
//	旧模型下 `gift_balance` 是只增不减的来源标记（消费从不扣它），
//	所有消费都只 `Update("balance", ...)`（见 680e2b8 的 search.go）。
//
// 所以历史行的「已消耗赠金」**事实上就是 0**，不是「未知」。
// 而历史消费消耗的本金已经体现在当前 `balance` 里，本公式自然扣到，不会重复也不会遗漏。
//
// 这是本口径相对「实付 − Σprincipal_charged」的另一个优势：
// 后者在历史行上必须靠 sentinel 兜底，前者不需要。

// RefundablePrincipalBreakdown 是一次可退额计算的全部中间量，供审计与测试断言。
type RefundablePrincipalBreakdown struct {
	// BalanceMicros 是当前充值本金余额（可为负，负值按 0 处理）。
	BalanceMicros int64
	// GiftConsumedMicros 是该用户累计消耗的赠送金额。
	GiftConsumedMicros int64
	// OrderPaidMicros 是本笔订单实付（0 表示无订单上限，用于无订单的手工退款）。
	OrderPaidMicros int64
	// GiftDeductedMicros 是「赠金消耗实际扣掉了多少可退额度」（截断到 balance 以内）。
	GiftDeductedMicros int64
	// RefundableMicros 是最终可退额。
	RefundableMicros int64
}

// computeRefundablePrincipal 是纯函数：可退额 = min(订单实付, max(0, 本金余额 − 已消耗赠金))。
//
// 三个边界都在这里收口，不散到调用方：
//   - 本金为负（欠款）→ 可退 0，绝不退成负数；
//   - 赠金消耗超过本金余额 → 可退 0（已消耗的服务价值已经超过剩余本金）；
//   - 有订单时不超过本笔实付（避免用户退不到自己刚充的钱，也避免超额退款）。
func computeRefundablePrincipal(balanceMicros, giftConsumedMicros, orderPaidMicros int64) RefundablePrincipalBreakdown {
	breakdown := RefundablePrincipalBreakdown{
		BalanceMicros:      balanceMicros,
		GiftConsumedMicros: giftConsumedMicros,
		OrderPaidMicros:    orderPaidMicros,
	}
	// 欠款（balance < 0）没有可退本金；赠金消耗按 0 起算，不追欠。
	effectiveBalance := balanceMicros
	if effectiveBalance < 0 {
		effectiveBalance = 0
	}
	if giftConsumedMicros < 0 {
		giftConsumedMicros = 0
	}
	claim := effectiveBalance - giftConsumedMicros
	if claim < 0 {
		claim = 0
	}
	// 赠金实际抵扣的额度：不会超过可用本金。
	breakdown.GiftDeductedMicros = giftConsumedMicros
	if breakdown.GiftDeductedMicros > effectiveBalance {
		breakdown.GiftDeductedMicros = effectiveBalance
	}
	if orderPaidMicros > 0 && claim > orderPaidMicros {
		claim = orderPaidMicros
	}
	breakdown.RefundableMicros = claim
	return breakdown
}

// RefundablePrincipalWithDB 读取钱包与账单，算出该用户当前的可退本金。
//
// 只读、不加锁：调用方若在事务内已有权威钱包快照（例如已 `lockTicketRefundWallet`），
// 应优先用 computeRefundablePrincipal 配那份快照，避免读到过期的 balance。
func RefundablePrincipalWithDB(db *gorm.DB, userID int32, orderPaidMicros int64) (RefundablePrincipalBreakdown, error) {
	if db == nil || userID <= 0 {
		return RefundablePrincipalBreakdown{}, ErrTicketRefundInvalid
	}
	var wallet struct {
		Balance float64 `gorm:"column:balance"`
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
		Select("balance").Where("id = ?", userID).Take(&wallet).Error; err != nil {
		return RefundablePrincipalBreakdown{}, err
	}
	giftConsumed, err := giftConsumedMicrosWithDB(db, userID)
	if err != nil {
		return RefundablePrincipalBreakdown{}, err
	}
	return computeRefundablePrincipal(yuanToMicros(wallet.Balance), giftConsumed, orderPaidMicros), nil
}

// giftConsumedMicrosWithDB 汇总该用户累计消耗的赠送金额（单位：micros）。
//
// 只读 `gift_charged`：历史行是 0，而历史行的赠金消耗**事实上就是 0**（旧模型从不扣
// gift_balance），因此不需要 sentinel，也不需要区分「未知」。
//
// 列不存在时（迁移还没跑过的老库）返回 0 并降级：此时所有消费都是旧模型产生的，
// 同样没有赠金消耗，返回 0 是正确结果而不是猜测。
func giftConsumedMicrosWithDB(db *gorm.DB, userID int32) (int64, error) {
	if !db.Migrator().HasColumn(entity.TableNameBillingRecord, "gift_charged") {
		return 0, nil
	}
	var row struct {
		Total float64 `gorm:"column:total"`
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{}).
		Select("COALESCE(SUM(gift_charged), 0) AS total").
		Where("user_id = ?", userID).Scan(&row).Error; err != nil {
		return 0, err
	}
	if row.Total <= 0 {
		return 0, nil
	}
	return yuanToMicros(row.Total), nil
}

// yuanToMicros 把 6 位精度的金额转成 micros（1 元 = 1e6 micros）。
//
// 与退款链路既有口径一致：`ticketRefundMicrosPerCent = 10_000`，即 1 分 = 1 万 micros。
func yuanToMicros(amount float64) int64 {
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0
	}
	return int64(math.Round(util.RoundMoney(amount) * 1e6))
}

// microsToCents 把 micros 向下取整到「分」：可退额必须**向下**取整，
// 否则多出来的零头会让退款额超过真实可退本金。
func microsToCents(micros int64) int64 {
	if micros <= 0 {
		return 0
	}
	return micros / ticketRefundMicrosPerCent
}

// appendTicketRefundPrincipalAudit 在申请单的 reason 里追加一行可退额审计。
//
// 为什么写进 reason 而不是新加列：`ticket_refunds` 加列要迁移，而这段信息是
// **一次性说明**（管理员与用户都要能看懂「为什么只能退这么多」），
// 塞进既有的自由文本字段最省成本，且不会影响任何按列读取的逻辑。
//
// 只在**确实发生了赠金抵扣**时才追加，避免给正常退款单塞无意义噪声。
func appendTicketRefundPrincipalAudit(reason string, breakdown RefundablePrincipalBreakdown, refundableCents int64) string {
	if breakdown.GiftDeductedMicros <= 0 {
		return reason
	}
	note := "【可退本金核算】已消费赠送金额 " + formatMicrosYuan(breakdown.GiftDeductedMicros) +
		" 元不计入可退本金；可退上限 " + formatCentsYuan(refundableCents) + " 元"
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		return trimmed + "\n" + note
	}
	return note
}

// formatMicrosYuan 把 micros 格式化成元（去掉多余的零）。
func formatMicrosYuan(micros int64) string {
	return formatYuanFromMicros(micros)
}

// formatCentsYuan 把分格式化成元（去掉多余的零）。
func formatCentsYuan(cents int64) string {
	return formatYuanFromMicros(cents * ticketRefundMicrosPerCent)
}

// formatYuanFromMicros 把 micros 转成人类可读的元（最多 6 位、去掉尾部零）。
//
// 用 strconv.FormatFloat 而不是 fmt.Sprintf("%.6f")：后者会给 20 元输出
// "20.000000"，读起来像精度噪声。FormatFloat 的 -1 精度会输出最短可往返表示。
func formatYuanFromMicros(micros int64) string {
	yuan := float64(micros) / 1e6
	return strconv.FormatFloat(util.RoundMoney(yuan), 'f', -1, 64)
}
