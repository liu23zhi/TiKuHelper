package service

import (
	"errors"
	"strconv"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RechargePromotionPolicyVersion 是充值阶梯赠送当前档位表的口径版本快照。
//
// 写进 recharge_promotion_grants.policy_version：门槛、比例或「取不高于本笔金额的
// 最高档」这条规则一旦变化就必须换值，历史发放行保持原样不重算
// （与 ReferralRebatePolicyVersion / ReviewRebatePolicyVersion 同构）。
const RechargePromotionPolicyVersion = "v1"

// rechargePromotionBillingRecordType 是充值赠送的账单类型。
//
// 语义上等同 registration_bonus / review_rebate：都是「送的钱」，都计入 balance
// 但不计入可退本金。单独一个 type 是为了让用户与管理员能把「实付」和「送的」
// 在账单流水里分开看，也让汇总统计不会把它算成消费。
const rechargePromotionBillingRecordType = "recharge_promotion"

// RechargePromotionTier 是充值赠送的一个档位。
//
// Threshold 是档位门槛（充值实付金额），Rate 是赠送比例（0.10 = 10%）。
type RechargePromotionTier struct {
	Threshold float64
	Rate      float64
}

// DefaultRechargePromotionTiers 是六档充值赠送表（04 稿 §3 / 01-04 要点 §2）。
//
//	8.8   → 10%
//	28    → 25%
//	88    → 50%
//	288   → 100%
//	888   → 150%
//	1688  → 200%
//
// 规则（**必须与计算函数保持一致**，改表必须连带改测试）：
//   - 取**不高于本笔实付金额的最高门槛**，整笔按该档比例赠送；
//   - 不做线性插值、不累进、不叠加、付款后不追溯补赠；
//   - 低于最低门槛（8.8）可充值但不参与赠送。
var DefaultRechargePromotionTiers = []RechargePromotionTier{
	{Threshold: 8.8, Rate: 0.10},
	{Threshold: 28, Rate: 0.25},
	{Threshold: 88, Rate: 0.50},
	{Threshold: 288, Rate: 1.00},
	{Threshold: 888, Rate: 1.50},
	{Threshold: 1688, Rate: 2.00},
}

// SelectRechargePromotionTier 按「不高于实付金额的最高门槛」选档。
//
// 返回 ok=false 表示本笔金额低于最低门槛，不参与赠送。
// 门槛表按升序遍历，命中即更新，因此结果与表顺序无关（表无序也不会选错档）。
func SelectRechargePromotionTier(paidAmount float64) (RechargePromotionTier, bool) {
	paid := util.RoundMoneyToDecimals(paidAmount, util.RechargePrecision)
	if paid <= 0 {
		return RechargePromotionTier{}, false
	}
	var selected RechargePromotionTier
	matched := false
	for _, tier := range DefaultRechargePromotionTiers {
		threshold := util.RoundMoneyToDecimals(tier.Threshold, util.RechargePrecision)
		if threshold <= 0 || tier.Rate <= 0 {
			continue
		}
		// 用充值口径比较：充值金额只有 2 位小数，门槛也是 2 位。
		if paid >= threshold && (!matched || threshold > selected.Threshold) {
			selected = RechargePromotionTier{Threshold: threshold, Rate: tier.Rate}
			matched = true
		}
	}
	return selected, matched
}

// ComputeRechargePromotionGiftAmount 计算某笔充值的赠金金额（6 位口径）。
//
// 金额精度分层（余额/账务 6 位，充值实付 2 位）：
//   - **入参**是充值实付金额，2 位口径；
//   - **出参**是赠金，属于钱包/账务，6 位口径；
//   - 全程用 util.RoundMoney* 收敛，不自己写四舍五入。
//
// 低于最低门槛返回 0（= 不赠送）。赠金**不计入可退本金/净实付/返佣基数**，
// 这里只负责算出「送多少」，钱包口径由 GrantRechargePromotionInTx 落。
func ComputeRechargePromotionGiftAmount(paidAmount float64) float64 {
	tier, ok := SelectRechargePromotionTier(paidAmount)
	if !ok {
		return 0
	}
	paid := util.RoundMoneyToDecimals(paidAmount, util.RechargePrecision)
	return util.RoundMoney(paid * tier.Rate)
}

// rechargePromotionSchemaAvailable 判断赠送账本表与 gift_balance 列是否已就绪。
//
// 存在的意义与 referralSchemaAvailable 一致：充值回调是**既有核心支付路径**，
// 不能因为赠送迁移还没跑过（老库升级、单测里的最小 schema）就整体报错。
// 结构没准备好时一律降级为「本次不发放赠金」，绝不阻断充值入账。
func rechargePromotionSchemaAvailable(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	if !db.Migrator().HasTable(entity.TableNameRechargePromotionGrant) {
		return false
	}
	if !db.Migrator().HasTable(entity.TableNameUser) {
		return false
	}
	// gift_balance 是赠金的来源标记桶。缺这一列时不能只加 balance ——
	// 那样「送的」和「实付的」就再也分不开了（对账不可逆），宁可本次不发。
	return db.Migrator().HasColumn(entity.TableNameUser, "gift_balance")
}

// rechargePromotionSession 清掉继承的模型，避免 `db.Where(...).Take(&Grant{})`
// 生成跨表查询（同 referralSession 的成因）。
func rechargePromotionSession(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.Session(&gorm.Session{NewDB: true})
}

// GrantRechargePromotionInTx 在充值到账事务内发放阶梯赠金。
//
// 调用时机：必须在 order 已置 paid、users.balance 已加上 order.Amount 之后 ——
// 因此 balanceAfter 参数是**充值入账后**的余额，赠金累加在它上面。
//
// 落账三件事（这是「赠金归口」的全部落点，与评价返现 creditReviewRebateGiftInTx 一致）：
//  1. users.gift_balance += gift   —— 赠送金额桶：**可消费、不可退款**，下限 0；
//  2. users.balance **不动**       —— balance 是充值本金、可退款；赠金进这里
//     等于「充值赠金可退现」。唯一例外是账户本来欠款（balance<0）：
//     按规则 3 先抵欠款，抵完的余额才是 balance 的最终值；
//  3. billing_records 一条 type=recharge_promotion —— 账单流水，让用户在「账单流水」
//     里看得见这笔收入，也让管理员能按 type 汇总。
//  4. recharge_promotion_grants 一条 —— 独立发放账本，幂等锚点 + 档位快照。
//
// 口径（写在注释里，因为这是最容易走样的地方）：
//
//	赠金**计入可用额度（本金 + 赠金）可消费**，但**不计入可退本金 / 净实付 / 返佣基数**。
//	退款与邀请返佣只认充值实付本金（referralRebateBaseAmount 用 order.Amount），
//	两者都不读 gift_balance —— 否则「充值 1688 赠 3376 再退款」就能套出赠金。
//
// 幂等：由 recharge_promotion_grants.recharge_order_id 的唯一索引保证。
// 重复支付回调在 HandleRechargePaymentNotifyWithDB 里已被 paid 状态提前 return，
// 即使极端并发走到了这里，第二笔插入会撞唯一键 —— 用 OnConflict{DoNothing} +
// RowsAffected==0 静默跳过（唯一键冲突跳过并完成），**绝不把已提交的到账事务打回滚**。
//
// 返回 nil 的三种情况（都属于正常降级，不是错误）：
//   - 开关关闭；
//   - 表/列还没迁移；
//   - 本笔金额低于最低门槛（不参与赠送）。
func GrantRechargePromotionInTx(tx *gorm.DB, order entity.RechargeOrder, balanceAfter float64) (*entity.RechargePromotionGrant, error) {
	if tx == nil {
		return nil, nil
	}
	// 开关关闭 → 不产生任何赠金，也不写赠送流水（需求：关闭时只按实付入账）。
	if !GetRechargePromotionEnabledWithDB(tx) {
		return nil, nil
	}
	// 表/列还没迁移 → 降级，不影响充值到账。
	if !rechargePromotionSchemaAvailable(tx) {
		return nil, nil
	}
	if order.ID <= 0 || order.UserID <= 0 {
		return nil, nil
	}

	paid := util.RoundMoneyToDecimals(order.Amount, util.RechargePrecision)
	tier, matched := SelectRechargePromotionTier(paid)
	if !matched {
		return nil, nil
	}
	gift := util.RoundMoney(paid * tier.Rate)
	if gift <= 0 {
		return nil, nil
	}

	var user entity.User
	if err := rechargePromotionSession(tx).Model(&entity.User{}).
		Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", order.UserID).Take(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}

	// 双桶模型（43-*.md）：赠金只进 gift_balance，**不再加进 balance** ——
	// balance 是充值本金、可退款，赠金进这里就等于「充值赠金可退现」。
	// 入账按规则 3：账户若仍有欠款（balanceAfter 为负），先抵欠款，剩余才进赠金桶。
	//
	// 余额口径是 6 位，必须用钱包口径。before 取入参（充值入账后）而不是
	// user.Balance —— 后者可能被并发会话读成入账前的值。
	before := util.RoundMoney(balanceAfter)
	after, giftAfter := CreditGiftBuckets(before, user.GiftBalance, gift)

	// 先插发放账本（幂等锚点），再写钱包与流水。
	// 顺序很重要：唯一键冲突时要能在**没有动过钱包**的前提下直接返回，
	// 否则就得补偿写回，而补偿本身又可能失败。
	record := entity.RechargePromotionGrant{
		RechargeOrderID: order.ID,
		OrderNo:         order.OrderNo,
		UserID:          order.UserID,
		PaidAmount:      paid,
		Threshold:       tier.Threshold,
		Rate:            tier.Rate,
		GiftAmount:      gift,
		BalanceBefore:   before,
		BalanceAfter:    after,
		PolicyVersion:   RechargePromotionPolicyVersion,
		Remark:          "充值阶梯赠送",
	}
	result := rechargePromotionSession(tx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		// 这笔订单已经发放过（重复回调 / 并发）→ 不重复加钱，返回既有记录。
		var existing entity.RechargePromotionGrant
		if err := rechargePromotionSession(tx).Model(&entity.RechargePromotionGrant{}).
			Where("recharge_order_id = ?", order.ID).Take(&existing).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return &existing, nil
	}

	if err := rechargePromotionSession(tx).Model(&entity.User{}).Where("id = ?", order.UserID).Updates(map[string]any{
		"balance":      after,
		"gift_balance": giftAfter,
	}).Error; err != nil {
		return nil, err
	}

	// 赠送流水：type=recharge_promotion、provider=system（界面标签「系统赠送」）。
	// request_id 与充值单一致，related_order_id 指向订单，便于两行对照。
	billingRecord := entity.BillingRecord{
		UserID:            order.UserID,
		Type:              rechargePromotionBillingRecordType,
		Provider:          "system",
		Amount:            gift,
		BalanceBefore:     before,
		BalanceAfter:      after,
		RequestID:         order.OrderNo,
		RelatedOrderID:    order.ID,
		UnitPriceSnapshot: rechargePromotionSnapshot(paid, tier),
		Remark:            rechargePromotionRemark(tier, gift),
	}
	if err := rechargePromotionSession(tx).Create(&billingRecord).Error; err != nil {
		return nil, err
	}
	return &record, nil
}

// formatPromotionMoney 把金额按指定小数位格式化成稳定字符串（不带科学计数法）。
func formatPromotionMoney(amount float64, decimals int) string {
	return strconv.FormatFloat(util.RoundMoneyToDecimals(amount, decimals), 'f', decimals, 64)
}

// rechargePromotionSnapshot 生成赠送流水的单价快照。
//
// 沿用账单快照的 `key=value` 空格分隔格式（ExtractBillingSnapshotField 的读法），
// 值里不含空格，因此不会被截断。
func rechargePromotionSnapshot(paid float64, tier RechargePromotionTier) string {
	return "promotion=recharge_tier" +
		" paidAmount=" + formatPromotionMoney(paid, util.RechargePrecision) +
		" threshold=" + formatPromotionMoney(tier.Threshold, util.RechargePrecision) +
		" rate=" + formatPromotionMoney(tier.Rate, 2) +
		" policyVersion=" + RechargePromotionPolicyVersion
}

// rechargePromotionRemark 生成赠送流水的中文摘要（账单「项目/明细」列直接用）。
func rechargePromotionRemark(tier RechargePromotionTier, gift float64) string {
	return "充值赠送（满 " + formatPromotionMoney(tier.Threshold, util.RechargePrecision) +
		" 元赠 " + formatPromotionMoney(tier.Rate*100, 2) + "%，送 " +
		formatPromotionMoney(gift, util.MoneyPrecision) + " 元）"
}
