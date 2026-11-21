package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func freezeTicketRefundLines(tx *gorm.DB, row entity.TicketRefund, lines []entity.TicketRefundOrderLine, cents int64, verify TicketRefundOrderVerifier) error {
	remaining := cents
	for _, line := range lines {
		if line.Status != TicketRefundStatusPendingReview {
			return ErrTicketRefundState
		}
		if line.Cents <= 0 || line.Cents > remaining {
			return ErrTicketRefundAllocationMismatch
		}
		remaining -= line.Cents
	}
	if remaining != 0 {
		return ErrTicketRefundAllocationMismatch
	}
	for _, line := range lines {
		_, channel, err := verifyRefundLineOrder(tx, row, line, verify)
		if err != nil {
			return err
		}
		if err := updateTicketRefundLine(tx, &line, map[string]any{"status": TicketRefundStatusFrozen, "channel_ref": channel}); err != nil {
			return err
		}
	}
	return nil
}

func verifyRefundLineOrder(tx *gorm.DB, row entity.TicketRefund, line entity.TicketRefundOrderLine, verify TicketRefundOrderVerifier) (entity.RechargeOrder, string, error) {
	order, err := lockTicketRefundOrder(tx, row.UserID, line.OrderID)
	if err != nil {
		return order, "", err
	}
	channel, err := ticketRefundOriginalChannel(tx, order, verify, line.CreatedAt)
	if err != nil {
		return order, "", err
	}
	paid, _ := ticketRefundPaidOrderCents(order)
	if order.OrderNo != line.OrderNo || paid != line.PaidCents || line.Cents > paid ||
		ticketRefundOriginalTradeNo(order) != line.OriginalTradeNo ||
		(channel != line.ChannelRef && !(line.Status == TicketRefundStatusPendingReview && line.ChannelRef == "")) {
		return order, "", ErrTicketRefundOrderUnavailable
	}
	return order, channel, nil
}

// PrepareTicketRefundLineWithDB acquires each dispatch once. A claim whose
// caller timed out can only be queried through its recorded merchant number.
func PrepareTicketRefundLineWithDB(db *gorm.DB, admin entity.User, id int64, orderID int32, submit bool, verify TicketRefundOrderVerifier) (entity.TicketRefund, entity.TicketRefund, entity.RechargeOrder, error) {
	var local entity.TicketRefund
	var order entity.RechargeOrder
	parent, err := changeTicketRefundWithDB(db, admin, id, func(tx *gorm.DB, row *entity.TicketRefund) error {
		if row.Kind != TicketRefundKindOriginal ||
			(row.Status != TicketRefundStatusFrozen && row.Status != TicketRefundStatusProcessing && row.Status != TicketRefundStatusUnknown) {
			return ErrTicketRefundState
		}
		line, err := readTicketRefundLine(tx, id, orderID)
		if err != nil {
			return err
		}
		if !submit && line.Status != TicketRefundStatusProcessing && line.Status != TicketRefundStatusUnknown {
			return ErrTicketRefundState
		}
		if submit {
			if line.Status != TicketRefundStatusFrozen || line.StartedAt != nil {
				return ErrTicketRefundState
			}
			if _, err := ticketRefundHoldMicros(tx, *row); err != nil {
				return err
			}
		}
		order, _, err = verifyRefundLineOrder(tx, *row, line, verify)
		if err != nil {
			return err
		}
		if submit {
			now := time.Now()
			if err := updateTicketRefundLine(tx, &line, map[string]any{"status": TicketRefundStatusProcessing, "started_at": now}); err != nil {
				return err
			}
			if row.StartedAt == nil {
				if err := updateTicketRefund(tx, row, map[string]any{"status": TicketRefundStatusProcessing, "started_at": now}); err != nil {
					return err
				}
			}
		}
		local = entity.TicketRefund{UserID: row.UserID, Kind: row.Kind, OrderID: &line.OrderID, OrderNo: line.OrderNo,
			ApprovedCents: line.Cents, OriginalTradeNo: line.OriginalTradeNo, ChannelRef: line.ChannelRef,
			Status: line.Status, RefundNo: line.RefundNo}
		return nil
	})
	return parent, local, order, err
}

func ReconcileTicketRefundLineWithDB(db *gorm.DB, admin entity.User, id int64, orderID int32, outcome, reference string) (entity.TicketRefund, error) {
	if (outcome != TicketRefundStatusSucceeded && outcome != TicketRefundStatusFailed && outcome != TicketRefundStatusUnknown) ||
		(outcome != TicketRefundStatusUnknown && !validTicketRefundReference(reference, 128)) {
		return entity.TicketRefund{}, ErrTicketRefundInvalid
	}
	return changeTicketRefundWithDB(db, admin, id, func(tx *gorm.DB, row *entity.TicketRefund) error {
		if row.Kind != TicketRefundKindOriginal {
			return ErrTicketRefundState
		}
		line, err := readTicketRefundLine(tx, id, orderID)
		if err != nil {
			return err
		}
		if line.Status == outcome && line.OutcomeReference == reference {
			return nil
		}
		if line.Status != TicketRefundStatusProcessing && line.Status != TicketRefundStatusUnknown {
			return ErrTicketRefundState
		}
		return settleTicketRefundLine(tx, row, &line, admin.ID, outcome, reference)
	})
}

func CancelUnsentTicketRefundLineWithDB(db *gorm.DB, admin entity.User, id int64, orderID int32) (entity.TicketRefund, error) {
	return changeTicketRefundWithDB(db, admin, id, func(tx *gorm.DB, row *entity.TicketRefund) error {
		if row.Kind != TicketRefundKindOriginal {
			return ErrTicketRefundState
		}
		line, err := readTicketRefundLine(tx, id, orderID)
		if err != nil {
			return err
		}
		if line.Status == TicketRefundStatusFailed && line.OutcomeReference == TicketRefundOutcomeUnsentReference {
			return nil
		}
		if row.StartedAt == nil || line.Status != TicketRefundStatusFrozen || line.StartedAt != nil {
			return ErrTicketRefundState
		}
		return settleTicketRefundLine(tx, row, &line, admin.ID, TicketRefundStatusFailed, TicketRefundOutcomeUnsentReference)
	})
}

func settleTicketRefundLine(tx *gorm.DB, row *entity.TicketRefund, line *entity.TicketRefundOrderLine, adminID int32, outcome, reference string) error {
	if outcome == TicketRefundStatusSucceeded {
		if _, err := ReverseReferralRebateForOrderInTx(tx, line.OrderID, "原订单退款，返佣已冲回"); err != nil {
			return err
		}
	}
	if err := updateTicketRefundLine(tx, line, map[string]any{"status": outcome, "outcome_reference": reference}); err != nil {
		return err
	}
	lines, err := ticketRefundLines(tx, row.ID, true)
	if err != nil {
		return err
	}
	var settled, pending int64
	parentStatus := TicketRefundStatusProcessing
	for _, line := range lines {
		switch line.Status {
		case TicketRefundStatusSucceeded:
			settled += line.Cents
		case TicketRefundStatusFailed:
		case TicketRefundStatusUnknown:
			parentStatus = TicketRefundStatusUnknown
			pending++
		default:
			pending++
		}
	}
	if pending != 0 {
		if row.Status == parentStatus {
			return nil
		}
		return updateTicketRefund(tx, row, map[string]any{"status": parentStatus})
	}
	held, err := ticketRefundHoldMicros(tx, *row)
	if err != nil {
		return err
	}
	if settled == 0 || settled == row.ApprovedCents {
		status := TicketRefundStatusSucceeded
		if settled == 0 {
			status = TicketRefundStatusFailed
		}
		return finalizeTicketRefundInTx(tx, row, adminID, status, "", held)
	}
	return finalizePartialTicketRefundInTx(tx, row, adminID, min(held, settled*ticketRefundMicrosPerCent), held)
}

func readTicketRefundLine(tx *gorm.DB, refundID int64, orderID int32) (entity.TicketRefundOrderLine, error) {
	var line entity.TicketRefundOrderLine
	err := tx.Session(&gorm.Session{NewDB: true}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("refund_id = ? AND order_id = ?", refundID, orderID).Take(&line).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return line, ErrTicketRefundOrderUnavailable
	}
	return line, err
}

func updateTicketRefundLine(tx *gorm.DB, line *entity.TicketRefundOrderLine, updates map[string]any) error {
	result := tx.Model(&entity.TicketRefundOrderLine{}).Where("id = ? AND status = ?", line.ID, line.Status).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTicketRefundState
	}
	return tx.Where("id = ?", line.ID).Take(line).Error
}

func finalizePartialTicketRefundInTx(tx *gorm.DB, row *entity.TicketRefund, adminID int32, consumed, held int64) error {
	wallet, err := lockTicketRefundWallet(tx, row.UserID)
	if err != nil {
		return err
	}
	released := held - consumed
	if released <= 0 || wallet.micros > ticketRefundMaxWalletMicros-released {
		return ErrTicketRefundAmount
	}
	after := wallet.micros + released
	if err := updateTicketRefundWallet(tx, row.UserID, wallet, after); err != nil {
		return err
	}
	if err := insertTicketRefundLedger(tx, *row, adminID, ticketRefundLedgerConsume, consumed, wallet.micros, wallet.micros); err != nil {
		return err
	}
	if err := insertTicketRefundLedger(tx, *row, adminID, ticketRefundLedgerRelease, released, wallet.micros, after); err != nil {
		return err
	}
	return updateTicketRefund(tx, row, map[string]any{"status": TicketRefundStatusPartial, "resolved_at": time.Now(), "resolved_by": adminID})
}

func settleManualRefundLines(tx *gorm.DB, row entity.TicketRefund, actorID int32, outcome string, orderIDs *[]int32, previous []entity.TicketRefundOrderLine) error {
	lines := previous
	if outcome == TicketRefundStatusFailed && orderIDs != nil {
		return ErrTicketRefundInvalid
	}
	if orderIDs != nil {
		additionalOrderIDs := make([]int32, 0, len(*orderIDs))
		for _, id := range *orderIDs {
			if row.OrderID == nil || id != *row.OrderID {
				additionalOrderIDs = append(additionalOrderIDs, id)
			}
		}
		var err error
		lines, err = planTicketRefundRevisionLines(tx, row, TicketRefundRevisionInput{Kind: row.Kind, ManualOrderIDs: additionalOrderIDs}, row.ApprovedCents, nil, time.Now())
		if err != nil {
			return err
		}
		if len(previous) > 0 {
			if err := tx.Where("refund_id = ?", row.ID).Delete(&entity.TicketRefundOrderLine{}).Error; err != nil {
				return err
			}
		}
		if len(lines) > 0 {
			if err := tx.Create(&lines).Error; err != nil {
				return err
			}
		}
	}
	for _, line := range lines {
		if outcome == TicketRefundStatusSucceeded && (row.OrderID == nil || *row.OrderID != line.OrderID) {
			if _, err := ReverseReferralRebateForOrderInTx(tx, line.OrderID, "关联人工退款，返佣已冲回"); err != nil {
				return err
			}
		}
		if err := updateTicketRefundLine(tx, &line, map[string]any{"status": outcome}); err != nil {
			return err
		}
	}
	if orderIDs != nil && row.TicketID != nil {
		orders := make([]string, 0, len(lines))
		for _, line := range lines {
			orders = append(orders, line.OrderNo)
		}
		detail := "本次人工核销未新增充值订单退款标记"
		if len(orders) > 0 {
			detail = fmt.Sprintf("本次人工核销标记了 %d 笔充值订单已退款：%s", len(orders), strings.Join(orders, "、"))
		}
		return appendTicketRefundRevisionMessage(tx, row, actorID, true, detail, time.Now())
	}
	return nil
}
