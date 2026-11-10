package service

import "github.com/itihey/tikuAdapter/pkg/util"

// 双桶钱包的两条核心规则（43-双桶扣减与欠款抵补的精确定义.md §1）。
//
// 桶的语义：
//   - balance      = **仅充值本金**，可退款，**可以为负**（欠款的载体）；
//   - gift_balance = **赠送金额**（注册赠送 / 评价返现 / 邀请返佣 / 充值赠金），
//     可消费但**不可退**，**下限 0，永不为负**。
//
// 这两条规则必须成对实现：只改扣减不改入账，会出现「欠款状态下赠金越攒越多」；
// 只改入账不改扣减，会出现「赠金有钱但消费不扣」。
//
// 为什么不用两个独立钱包：消费时要按顺序跨桶扣，拆成两个可用钱包会让「余额够不够」
// 出现两段判断，扣款失败后的补偿也更难写。这里保持一行两个列，用两个纯函数表达顺序。

// WalletAvailableTotal 返回账户的可用合计（本金 + 赠金）。
//
// 这是「还能不能继续用服务」的唯一判据：旧模型下 balance 就是合计，新模型下
// 必须相加 —— 否则 balance=0 但 gift=1 的账户会被误拒（43-*.md §2 表格）。
func WalletAvailableTotal(balance, gift float64) float64 {
	return util.RoundMoney(util.RoundMoney(balance) + util.RoundMoney(gift))
}

// ConsumeWalletBuckets 按「先赠金、扣光再扣本金」的规则扣款。
//
// 规则 2（43-*.md）：
//
//	fromGift    = min(gift, amount)
//	gift       -= fromGift
//	fromBalance = amount - fromGift
//	balance    -= fromBalance      // 允许变负
//
// 返回扣款后的两个桶。赠金永远不会被扣成负数；本金**不 clamp**，它是欠款载体
// （算例 A：gift=2 balance=1 消耗 4 → gift=0 balance=-1）。
func ConsumeWalletBuckets(balance, gift, amount float64) (newBalance, newGift float64) {
	balance = util.RoundMoney(balance)
	gift = util.RoundMoney(gift)
	amount = util.RoundMoney(amount)
	if amount <= 0 {
		return balance, gift
	}
	// 历史脏数据可能把赠金写成负数；按 0 处理，绝不把负赠金当成可用额度。
	if gift < 0 {
		gift = 0
	}
	fromGift := gift
	if fromGift > amount {
		fromGift = amount
	}
	fromBalance := util.RoundMoney(amount - fromGift)
	return util.RoundMoney(balance - fromBalance), util.RoundMoney(gift - fromGift)
}

// CreditGiftBuckets 按「先抵欠款、剩余进赠金」的规则给账户加赠送金额。
//
// 规则 3（43-*.md）：
//
//	offset      = min(max(-balance, 0), amount)   // 抵欠款，最多抵到 0
//	balance    += offset
//	gift       += amount - offset
//
// 这是最容易被漏掉的一条：直接 `gift += amount` 会让「欠款 1 元 + 赠 2 元」变成
// `balance=-1, gift=2`，而用户要的是 `balance=0, gift=1`（算例 B）。
//
// 入账后 balance 必然 >= 0（offset 抵到 0 为止），因此不变量 I4 天然成立；
// 也不变量 I3（balance<0 ⇒ gift==0）靠这条 + ConsumeWalletBuckets 共同维持。
func CreditGiftBuckets(balance, gift, amount float64) (newBalance, newGift float64) {
	balance = util.RoundMoney(balance)
	gift = util.RoundMoney(gift)
	amount = util.RoundMoney(amount)
	if amount <= 0 {
		return balance, gift
	}
	if gift < 0 {
		gift = 0
	}
	debt := util.RoundMoney(-balance)
	if debt < 0 {
		debt = 0
	}
	offset := debt
	if offset > amount {
		offset = amount
	}
	return util.RoundMoney(balance + offset), util.RoundMoney(gift + util.RoundMoney(amount-offset))
}

// SplitWalletCharge 是 ConsumeWalletBuckets 的**精确**版本：不做任何取整，
// 只决定「这笔钱从哪个桶出多少」。
//
// 为什么需要它：计费链路必须保留 1e-7 级的精确消耗额（AGENTS.md 第 6/7 条：
// 只在记录边界取整）。若先用 ConsumeWalletBuckets 取整，被丢弃的尾差无法靠
// 再次取整挽回，账单里的余额变化会与实际消耗对不上。
//
// 规则与 ConsumeWalletBuckets 完全一致（先赠金、再本金），只是不取整。
func SplitWalletCharge(balance, gift, amount float64) (fromGift, fromBalance float64) {
	if amount <= 0 {
		return 0, 0
	}
	if gift < 0 {
		gift = 0
	}
	fromGift = gift
	if fromGift > amount {
		fromGift = amount
	}
	return fromGift, amount - fromGift
}

// DeductGiftBucket 从赠金桶扣款并 clamp 到 0，返回实际扣减额与扣后余额。
//
// 用于「撤销一笔赠送」的场景（返佣冲回、退款冲回、管理员减赠金）：应扣 amount，
// 但赠金可能已被消费，只能扣到 0。实际扣减额由调用方写进流水，**差额必须留痕** ——
// 静默吞掉差额等于对账时凭空少一笔。
func DeductGiftBucket(gift, amount float64) (deducted, newGift float64) {
	gift = util.RoundMoney(gift)
	amount = util.RoundMoney(amount)
	if gift < 0 {
		gift = 0
	}
	if amount <= 0 {
		return 0, gift
	}
	if gift < amount {
		return gift, 0
	}
	return util.RoundMoney(amount), util.RoundMoney(gift - amount)
}
