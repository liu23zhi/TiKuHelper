package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TicketRefundRevisionInput struct {
	Manage                bool                          `json:"manage"`
	UnpaidConfirmed       bool                          `json:"unpaidConfirmed"`
	Kind                  string                        `json:"kind"`
	RequestedCents        int64                         `json:"requestedCents"`
	Reason                string                        `json:"reason"`
	Allocations           []TicketRefundOrderAllocation `json:"allocations"`
	ManualOrderIDs        []int32                       `json:"manualOrderIds"`
	PayoutQrAttachmentID  *int64                        `json:"payoutQrAttachmentId"`
	SignatureAttachmentID *int64                        `json:"signatureAttachmentId"`
	AgreementText         string                        `json:"agreementText"`
	PhoneSMSCode          string                        `json:"phoneSmsCode"`
}

type TicketRefundRevisionOrder struct {
	TicketRefundOrder
	ManualSupported bool `json:"manualSupported"`
}

func TicketRefundLinesWithDB(db *gorm.DB, id int64) ([]entity.TicketRefundOrderLine, error) {
	return ticketRefundLines(db, id, false)
}

func ticketRefundLines(db *gorm.DB, id int64, lock bool) ([]entity.TicketRefundOrderLine, error) {
	lines := make([]entity.TicketRefundOrderLine, 0)
	if !db.Migrator().HasTable(&entity.TicketRefundOrderLine{}) {
		return lines, nil
	}
	query := db.Session(&gorm.Session{NewDB: true}).Where("refund_id = ?", id).Order("id")
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Find(&lines).Error
	return lines, err
}

func TicketRefundRevisionOrdersWithDB(db *gorm.DB, actor entity.User, id int64, manage bool) ([]TicketRefundRevisionOrder, error) {
	fresh, err := ticketRefundActor(db, actor.ID, manage, false)
	if err != nil {
		return nil, err
	}
	row, err := GetTicketRefundWithDB(db, fresh, id)
	if err != nil {
		return nil, err
	}
	if !manage && row.UserID != actor.ID {
		return nil, ErrTicketRefundNotFound
	}
	var orders []entity.RechargeOrder
	if err := db.Where("user_id = ?", row.UserID).Order("id DESC").Find(&orders).Error; err != nil {
		return nil, err
	}
	usedIDs, err := ticketRefundUsedOrderIDs(db, row.UserID, row.ID)
	if err != nil {
		return nil, err
	}
	refundedIDs, err := ticketRefundSucceededOrderIDs(db, row.UserID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	result := make([]TicketRefundRevisionOrder, 0, len(orders))
	for _, order := range orders {
		if !manage && order.Status == rechargeOrderStatusPending {
			continue
		}
		paid, paidErr := ticketRefundPaidOrderCents(order)
		amount, _ := ticketRefundOrderAmountCents(order.Amount)
		reason := ticketRefundOrderUnavailableReason(order, now)
		if refundedIDs[order.ID] {
			order.Status = rechargeOrderStatusRefunded
			reason = "订单已退款，每笔充值订单只能退款一次"
		} else if usedIDs[order.ID] {
			reason = "该订单已关联退款，每笔充值订单只能退款一次"
		}
		var paidAt time.Time
		if order.PaidAt != nil {
			paidAt = *order.PaidAt
		}
		result = append(result, TicketRefundRevisionOrder{
			TicketRefundOrder: TicketRefundOrder{
				ID: order.ID, OrderNo: order.OrderNo, PaidCents: amount, PaidAt: paidAt, CreatedAt: order.CreatedAt, Status: order.Status,
				PaymentMethodNameSnapshot: order.PaymentMethodNameSnapshot, OriginalSupported: reason == "", OriginalUnsupportedReason: reason,
			},
			ManualSupported: manage && paidErr == nil && paid > 0 && !paidAt.After(now) && !usedIDs[order.ID] && !refundedIDs[order.ID],
		})
	}
	return result, nil
}

func ticketRefundSucceededOrderIDs(db *gorm.DB, userID int32) (map[int32]bool, error) {
	var legacy, lines []int32
	session := db.Session(&gorm.Session{NewDB: true})
	if err := session.Model(&entity.TicketRefund{}).Where("user_id = ? AND status = ? AND order_id IS NOT NULL",
		userID, TicketRefundStatusSucceeded).Pluck("order_id", &legacy).Error; err != nil {
		return nil, err
	}
	if db.Migrator().HasTable(&entity.TicketRefundOrderLine{}) {
		if err := session.Model(&entity.TicketRefundOrderLine{}).
			Joins("JOIN ticket_refunds ON ticket_refunds.id = ticket_refund_order_lines.refund_id").
			Where("ticket_refunds.user_id = ? AND ticket_refund_order_lines.status = ?", userID, TicketRefundStatusSucceeded).
			Pluck("ticket_refund_order_lines.order_id", &lines).Error; err != nil {
			return nil, err
		}
	}
	result := make(map[int32]bool, len(legacy)+len(lines))
	for _, id := range append(legacy, lines...) {
		result[id] = true
	}
	return result, nil
}

func createGroupedOriginalTicketRefundWithDB(db *gorm.DB, actor entity.User, input CreateTicketRefundInput) (entity.TicketRefund, error) {
	if db == nil || (input.PayoutQrAttachmentID != nil && *input.PayoutQrAttachmentID <= 0) ||
		(input.SignatureAttachmentID != nil && *input.SignatureAttachmentID <= 0) {
		return entity.TicketRefund{}, ErrTicketRefundInvalid
	}
	if !validTicketRefundCents(input.RequestedCents) {
		return entity.TicketRefund{}, ErrTicketRefundAmount
	}
	now := time.Now()
	row := entity.TicketRefund{UserID: actor.ID, Kind: TicketRefundKindOriginal, Status: TicketRefundStatusPendingReview,
		RequestedCents: input.RequestedCents, Reason: strings.TrimSpace(input.Reason), RefundNo: "tr-" + uuid.NewString(),
		CreatedAt: now, UpdatedAt: now, PayoutQrAttachmentID: input.PayoutQrAttachmentID, SignatureAttachmentID: input.SignatureAttachmentID}
	err := ticketRefundTransaction(db, func(tx *gorm.DB) error {
		lines, err := planTicketRefundRevisionLines(tx, row, TicketRefundRevisionInput{Kind: row.Kind, Allocations: input.Allocations},
			input.RequestedCents, nil, now)
		if err != nil {
			return err
		}
		fresh, err := ticketRefundActor(tx, actor.ID, false, true)
		if err != nil {
			return err
		}
		if err := validateRefundAttachmentsInTx(tx, fresh, input, &row, now); err != nil {
			return err
		}
		breakdown, err := RefundablePrincipalWithDB(tx, actor.ID, 0)
		if err != nil {
			return err
		}
		if input.RequestedCents > refundablePrincipalCentsCeil(breakdown) {
			return ErrTicketRefundExceedsRefundablePrincipal
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		for i := range lines {
			lines[i].RefundID = row.ID
		}
		if err := tx.Create(&lines).Error; err != nil {
			return err
		}
		if err := CreateTicketRefundShellInTx(tx, row, now); err != nil {
			return err
		}
		row.TicketID, err = readTicketRefundTicketID(tx, row.ID)
		if err != nil {
			return err
		}
		if row.TicketID == nil {
			return ErrTicketRefundTicketLink
		}
		return nil
	})
	return row, err
}

func ticketRefundUsedOrderIDs(db *gorm.DB, userID int32, except int64) (map[int32]bool, error) {
	var legacy, lines []int32
	session := db.Session(&gorm.Session{NewDB: true})
	if err := session.Model(&entity.TicketRefund{}).Where("user_id = ? AND id <> ? AND order_id IS NOT NULL", userID, except).Pluck("order_id", &legacy).Error; err != nil {
		return nil, err
	}
	if db.Migrator().HasTable(&entity.TicketRefundOrderLine{}) {
		if err := session.Model(&entity.TicketRefundOrderLine{}).
			Joins("JOIN ticket_refunds ON ticket_refunds.id = ticket_refund_order_lines.refund_id").
			Where("ticket_refunds.user_id = ? AND ticket_refunds.id <> ?", userID, except).
			Pluck("ticket_refund_order_lines.order_id", &lines).Error; err != nil {
			return nil, err
		}
	}
	result := make(map[int32]bool, len(legacy)+len(lines))
	for _, id := range append(legacy, lines...) {
		result[id] = true
	}
	return result, nil
}

func EditTicketRefundWithDB(db *gorm.DB, actor entity.User, id int64, input TicketRefundRevisionInput, verify TicketRefundOrderVerifier) (entity.TicketRefund, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	if db == nil || id <= 0 || !validTicketRefundCents(input.RequestedCents) ||
		!utf8.ValidString(input.Reason) || len(input.Reason) > 2000 || utf8.RuneCountInString(input.Reason) > 500 ||
		(input.Kind != TicketRefundKindManual && input.Kind != TicketRefundKindOriginal) ||
		(input.Kind == TicketRefundKindOriginal && len(input.ManualOrderIDs) != 0) ||
		(input.Kind == TicketRefundKindManual && len(input.Allocations) != 0) || (!input.Manage && len(input.ManualOrderIDs) != 0) {
		return entity.TicketRefund{}, ErrTicketRefundInvalid
	}
	var row entity.TicketRefund
	err := ticketRefundTransaction(db, func(tx *gorm.DB) error {
		fresh, err := ticketRefundActor(tx, actor.ID, input.Manage, false)
		if err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTicketRefundNotFound
			}
			return err
		}
		if !input.Manage && row.UserID != actor.ID {
			return ErrTicketRefundNotFound
		}
		if row.TicketID == nil || row.StartedAt != nil || row.ResolvedAt != nil ||
			(row.Status != TicketRefundStatusPendingReview && !(input.Manage && row.Status == TicketRefundStatusFrozen && input.UnpaidConfirmed)) {
			return ErrTicketRefundState
		}
		if input.RequestedCents != row.RequestedCents && row.Status != TicketRefundStatusPendingReview {
			return ErrTicketRefundAmount
		}
		now := time.Now()
		target := input.RequestedCents
		if row.Status == TicketRefundStatusFrozen {
			target = row.ApprovedCents
		}
		before, err := ticketRefundLines(tx, row.ID, true)
		if err != nil {
			return err
		}
		next, err := planTicketRefundRevisionLines(tx, row, input, target, verify, now)
		if err != nil {
			return err
		}
		if row.Status == TicketRefundStatusPendingReview {
			wallet, err := lockTicketRefundWallet(tx, row.UserID)
			if err != nil {
				return err
			}
			if !wallet.enabled {
				return ErrTicketRefundForbidden
			}
			giftConsumed, err := giftConsumedMicrosWithDB(tx, row.UserID)
			if err != nil {
				return err
			}
			if target > refundablePrincipalCentsCeil(computeRefundablePrincipal(wallet.micros, giftConsumed, 0)) {
				return ErrTicketRefundExceedsRefundablePrincipal
			}
		}
		var attachmentIDs []int64
		updates := map[string]any{
			"kind": input.Kind, "requested_cents": input.RequestedCents, "reason": input.Reason,
			"order_id": nil, "order_no": "", "order_paid_cents": 0, "original_trade_no": "", "channel_ref": "",
		}
		if row.Status == TicketRefundStatusFrozen {
			const auditPrefix = "【可退本金核算】"
			if index := strings.LastIndex(row.Reason, auditPrefix); index >= 0 {
				note := row.Reason[index:]
				if !strings.Contains(input.Reason, note) {
					updates["reason"] = strings.TrimSpace(input.Reason + "\n" + note)
				}
			}
		}
		if !input.Manage && row.Kind != TicketRefundKindManual && input.Kind == TicketRefundKindManual {
			if (input.PayoutQrAttachmentID != nil && *input.PayoutQrAttachmentID <= 0) ||
				(input.SignatureAttachmentID != nil && *input.SignatureAttachmentID <= 0) {
				return ErrTicketRefundInvalid
			}
			evidence := CreateTicketRefundInput{Kind: input.Kind, PayoutQrAttachmentID: input.PayoutQrAttachmentID,
				SignatureAttachmentID: input.SignatureAttachmentID, AgreementText: input.AgreementText, PhoneSMSCode: input.PhoneSMSCode}
			var signed entity.TicketRefund
			if err := validateRefundAttachmentsInTx(tx, fresh, evidence, &signed, now); err != nil {
				return err
			}
			updates["payout_qr_attachment_id"], updates["signature_attachment_id"] = evidence.PayoutQrAttachmentID, evidence.SignatureAttachmentID
			updates["agreement_text"], updates["agreement_signed_at"] = signed.AgreementText, signed.AgreementSignedAt
			attachmentIDs = []int64{*evidence.PayoutQrAttachmentID, *evidence.SignatureAttachmentID}
		}
		previous := describeTicketRefundRevision(row.Kind, row.RequestedCents, row.Reason, row.OrderNo, before)
		current := describeTicketRefundRevision(input.Kind, input.RequestedCents, updates["reason"].(string), "", next)
		if previous == current {
			return nil
		}
		if len(before) > 0 {
			if err := tx.Where("refund_id = ?", row.ID).Delete(&entity.TicketRefundOrderLine{}).Error; err != nil {
				return err
			}
		}
		if len(next) > 0 {
			if err := tx.Create(&next).Error; err != nil {
				if errors.Is(err, gorm.ErrDuplicatedKey) {
					return ErrTicketRefundOrderUnavailable
				}
				return err
			}
		}
		if err := updateTicketRefund(tx, &row, updates); err != nil {
			return err
		}
		return appendTicketRefundRevisionMessage(tx, row, fresh.ID, input.Manage, "修改前："+previous+"\n修改后："+current, now, attachmentIDs...)
	})
	return row, err
}

func planTicketRefundRevisionLines(tx *gorm.DB, row entity.TicketRefund, input TicketRefundRevisionInput, target int64, verify TicketRefundOrderVerifier, now time.Time) ([]entity.TicketRefundOrderLine, error) {
	const maxRefundOrders = 50
	allocations := append([]TicketRefundOrderAllocation(nil), input.Allocations...)
	for _, id := range input.ManualOrderIDs {
		allocations = append(allocations, TicketRefundOrderAllocation{OrderID: id})
	}
	if len(allocations) > maxRefundOrders || (input.Kind == TicketRefundKindOriginal && len(allocations) == 0) {
		return nil, ErrTicketRefundAmount
	}
	sort.Slice(allocations, func(i, j int) bool { return allocations[i].OrderID < allocations[j].OrderID })
	lines := make([]entity.TicketRefundOrderLine, 0, len(allocations))
	remaining := target
	for i, allocation := range allocations {
		if allocation.OrderID <= 0 || (i > 0 && allocations[i-1].OrderID == allocation.OrderID) {
			return nil, ErrTicketRefundInvalid
		}
		order, err := lockTicketRefundOrder(tx, row.UserID, allocation.OrderID)
		if err != nil {
			return nil, err
		}
		if err := ensureTicketRefundOrderUnusedExcept(tx, order.ID, row.ID); err != nil {
			return nil, err
		}
		paid, err := ticketRefundPaidOrderCents(order)
		if err != nil || order.PaidAt.After(now) {
			return nil, ErrTicketRefundOrderUnavailable
		}
		line := entity.TicketRefundOrderLine{RefundID: row.ID, OrderID: order.ID, OrderNo: order.OrderNo, PaidCents: paid,
			Cents: allocation.Cents, Status: row.Status, RefundNo: "tr-" + uuid.NewString(), CreatedAt: now}
		if input.Kind == TicketRefundKindOriginal {
			if !validTicketRefundCents(allocation.Cents) || allocation.Cents > paid || allocation.Cents > remaining {
				return nil, ErrTicketRefundAmount
			}
			if !ticketRefundOrderMeetsBasicCriteria(order, now) {
				return nil, ErrTicketRefundOrderUnavailable
			}
			if verify != nil || row.Status == TicketRefundStatusFrozen {
				line.ChannelRef, err = ticketRefundOriginalChannel(tx, order, verify, now)
				if err != nil {
					return nil, err
				}
			}
			line.OriginalTradeNo = ticketRefundOriginalTradeNo(order)
			remaining -= allocation.Cents
		}
		lines = append(lines, line)
	}
	if input.Kind == TicketRefundKindOriginal && remaining != 0 {
		return nil, ErrTicketRefundAmount
	}
	return lines, nil
}

func describeTicketRefundRevision(kind string, cents int64, reason, oldOrder string, lines []entity.TicketRefundOrderLine) string {
	method := "人工退回"
	if kind == TicketRefundKindOriginal {
		method = "原路退回"
	}
	orders := make([]string, 0, len(lines)+1)
	if oldOrder != "" {
		orders = append(orders, oldOrder)
	}
	for _, line := range lines {
		label := line.OrderNo
		if line.Cents > 0 {
			label += fmt.Sprintf("（¥%d.%02d）", line.Cents/100, line.Cents%100)
		}
		orders = append(orders, label)
	}
	return fmt.Sprintf("%s，申请金额 ¥%d.%02d，关联订单 [%s]，申请理由：%s", method, cents/100, cents%100, strings.Join(orders, "、"), reason)
}

func appendTicketRefundRevisionMessage(tx *gorm.DB, row entity.TicketRefund, actorID int32, admin bool, detail string, now time.Time, attachmentIDs ...int64) error {
	var actor entity.User
	if err := tx.Select("id", "username", "nickname").Where("id = ?", actorID).Take(&actor).Error; err != nil {
		return err
	}
	role, prefix := entity.TicketAuthorRoleUser, "我修改的工单"
	if admin {
		role, prefix = entity.TicketAuthorRoleAdmin, "我修改了工单。"
	}
	message := entity.TicketMessage{TicketID: *row.TicketID, UserID: actor.ID, AuthorName: ticketUserName(&actor),
		Body: prefix + "（" + detail + "）", AuthorRole: role, CreatedAt: now}
	if err := tx.Create(&message).Error; err != nil {
		return err
	}
	if err := BindDraftTicketAttachmentsInTx(tx, &actor, *row.TicketID, message.ID, attachmentIDs); err != nil {
		return err
	}
	return tx.Model(&entity.Ticket{}).Where("id = ?", *row.TicketID).Update("last_message_at", now).Error
}
