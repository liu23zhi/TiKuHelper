package service

import (
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type TicketRefundBalanceSummary struct {
	RefundableCents int64 `json:"refundableCents"`
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
		principal, err := RefundablePrincipalWithDB(tx, actor.ID, 0)
		if err != nil {
			return err
		}
		summary.RefundableCents = refundablePrincipalCentsCeil(principal)
		return nil
	})
	return summary, err
}
