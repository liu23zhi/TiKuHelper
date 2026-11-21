package service

import (
	"strings"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type TicketRefundBalanceSummary struct {
	// BalanceCents 是账户当前可用余额（充值本金 + 赠送余额）。赠送余额计入
	// 可用余额，但始终不计入 RefundableCents。
	BalanceCents int64 `json:"balanceCents"`
	// RefundableCents 是可退充值本金，按用户口径向上取整到两位小数。
	RefundableCents int64 `json:"refundableCents"`
	// NonRefundableCents 是账户余额中当前不能退款的部分，包含赠送余额以及
	// 已消费赠送金额挤占的可退本金。
	NonRefundableCents  int64  `json:"nonRefundableCents"`
	NonRefundableReason string `json:"nonRefundableReason"`
}

// TicketRefundSummaryWithDB uses the same principal and rounding rules as refund
// submission. The authentication cache supplies identity only; the current
// wallet and consumed gifts are read together without changing the account.
func TicketRefundSummaryWithDB(db *gorm.DB, actor entity.User) (TicketRefundBalanceSummary, error) {
	if db == nil {
		return TicketRefundBalanceSummary{}, ErrTicketRefundInvalid
	}
	var summary TicketRefundBalanceSummary
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		if _, err := ticketRefundActor(tx, actor.ID, false, false); err != nil {
			return err
		}
		var wallet struct {
			Balance     float64 `gorm:"column:balance"`
			GiftBalance float64 `gorm:"column:gift_balance"`
		}
		if err := tx.Session(&gorm.Session{NewDB: true}).Model(&entity.User{}).
			Select("balance", "gift_balance").Where("id = ?", actor.ID).Take(&wallet).Error; err != nil {
			return err
		}
		giftConsumedMicros, err := giftConsumedMicrosWithDB(tx, actor.ID)
		if err != nil {
			return err
		}
		principal := computeRefundablePrincipal(yuanToMicros(wallet.Balance), giftConsumedMicros, 0)
		balanceCents := walletAmountCentsCeil(WalletAvailableTotal(wallet.Balance, wallet.GiftBalance))
		summary.RefundableCents = refundablePrincipalCentsCeil(principal)
		summary.BalanceCents = balanceCents
		summary.NonRefundableCents = balanceCents - summary.RefundableCents
		if summary.NonRefundableCents < 0 {
			summary.NonRefundableCents = 0
		}
		summary.NonRefundableReason = ticketRefundNonRefundableReason(wallet.Balance, wallet.GiftBalance, principal, summary.RefundableCents)
		return nil
	})
	return summary, err
}

func walletAmountCentsCeil(amount float64) int64 {
	micros := yuanToMicros(amount)
	if micros <= 0 {
		return 0
	}
	return (micros + ticketRefundMicrosPerCent - 1) / ticketRefundMicrosPerCent
}

func ticketRefundNonRefundableReason(balance, giftBalance float64, principal RefundablePrincipalBreakdown, refundableCents int64) string {
	parts := make([]string, 0, 3)
	if giftBalance > 0 {
		parts = append(parts, "赠送余额 "+formatMicrosYuan(yuanToMicros(giftBalance))+" 元不可退款")
	}
	if principal.GiftConsumedMicros > 0 {
		parts = append(parts, "赠金已消费 "+formatMicrosYuan(principal.GiftConsumedMicros)+" 元，实际抵减可退本金 "+formatMicrosYuan(principal.GiftDeductedMicros)+" 元")
	}
	if balance <= 0 {
		parts = append(parts, "当前没有可退款的充值本金")
	} else if refundableCents == 0 {
		parts = append(parts, "赠金消耗已抵扣全部可退本金")
	}
	if len(parts) == 0 {
		if refundableCents <= 0 {
			return "当前账户没有可退款的充值本金。"
		}
		return "当前余额均为可退款的充值本金。"
	}
	return strings.Join(parts, "；") + "。"
}
