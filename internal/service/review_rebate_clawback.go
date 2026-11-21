package service

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ReviewRebateVerificationConfirmed    = "confirmed"
	ReviewRebateVerificationInvalid      = "invalid"
	ReviewRebateVerificationInconclusive = "inconclusive"
	ReviewRebateClawbackBillingType      = "review_rebate_clawback"
)

func RecordReviewRebateVerificationWithDB(db *gorm.DB, adminID, claimID int32, result, reason, evidence string) (entity.ReviewRebateVerification, error) {
	reason, evidence = strings.TrimSpace(reason), strings.TrimSpace(evidence)
	var row entity.ReviewRebateVerification
	switch result {
	case ReviewRebateVerificationConfirmed, ReviewRebateVerificationInvalid, ReviewRebateVerificationInconclusive:
	default:
		return row, ErrReviewRebateInvalidInput
	}
	if adminID <= 0 || reason == "" || evidence == "" || utf8.RuneCountInString(reason) > 255 || len(evidence) > 8192 {
		return row, ErrReviewRebateInvalidInput
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		_, award, err := lockReviewRebateClaimAward(tx, claimID)
		if err != nil {
			return err
		}
		row = entity.ReviewRebateVerification{ClaimID: claimID, AwardID: award.ID, AdminID: adminID, Result: result, Reason: reason, Evidence: evidence}
		return reviewRebateSession(tx).Create(&row).Error
	})
	return row, err
}

func lockReviewRebateClaimAward(tx *gorm.DB, claimID int32) (entity.ReviewRebateClaim, entity.ReviewRebateAward, error) {
	var claim entity.ReviewRebateClaim
	var award entity.ReviewRebateAward
	err := reviewRebateSession(tx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", claimID).Take(&claim).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return claim, award, ErrReviewRebateClaimNotFound
	}
	if err != nil {
		return claim, award, err
	}
	if claim.Status != entity.ReviewRebateStatusApproved && claim.Status != entity.ReviewRebateStatusClawedBack {
		return claim, award, ErrReviewRebateClaimState
	}
	err = reviewRebateSession(tx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("claim_id = ? AND user_id = ?", claim.ID, claim.UserID).Take(&award).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrReviewRebateClaimState
	}
	return claim, award, err
}

func ClawBackReviewRebateWithDB(db *gorm.DB, adminID, claimID, verificationID int32) (entity.ReviewRebateClawback, error) {
	var row entity.ReviewRebateClawback
	if adminID <= 0 || verificationID <= 0 {
		return row, ErrReviewRebateInvalidInput
	}
	original, err := GetReviewRebateClaimWithDB(db, claimID)
	if err != nil {
		return row, err
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		wallet, err := lockReviewRebateUser(tx, original.UserID)
		if err != nil {
			return err
		}
		claim, award, err := lockReviewRebateClaimAward(tx, claimID)
		if err != nil {
			return err
		}

		err = reviewRebateSession(tx).Where("award_id = ?", award.ID).Take(&row).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if claim.Status != entity.ReviewRebateStatusApproved {
			return ErrReviewRebateClaimState
		}

		var check entity.ReviewRebateVerification
		err = reviewRebateSession(tx).Where("claim_id = ? AND award_id = ?", claim.ID, award.ID).Order("id DESC").Take(&check).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if check.ID != verificationID || check.Result != ReviewRebateVerificationInvalid {
			return fmt.Errorf("%w: 请先记录该笔发放的最新无效核验结论", ErrReviewRebateClaimState)
		}
		amount := util.RoundMoney(award.Amount)
		if amount <= 0 {
			return ErrReviewRebateClaimState
		}
		deducted, after := DeductGiftBucket(wallet.GiftBalance, amount)
		gap := util.RoundMoney(amount - deducted)
		row = entity.ReviewRebateClawback{
			AwardID: award.ID, ClaimID: claim.ID, VerificationID: check.ID, UserID: wallet.ID, AdminID: adminID,
			RequestedAmount: amount, DeductedAmount: deducted, ShortfallAmount: gap,
		}
		if err := reviewRebateSession(tx).Create(&row).Error; err != nil {
			return err
		}
		if err := reviewRebateSession(tx).Model(&entity.User{}).Where("id = ?", wallet.ID).Update("gift_balance", after).Error; err != nil {
			return err
		}
		record := entity.BillingRecord{
			UserID: wallet.ID, Type: ReviewRebateClawbackBillingType, Provider: "system",
			Amount: deducted, BalanceBefore: wallet.Balance, BalanceAfter: wallet.Balance,
			UnitPriceSnapshot: referralGiftSnapshot(wallet.GiftBalance, after),
			RequestID:         fmt.Sprintf("%s_%d", ReviewRebateClawbackBillingType, award.ID),
			Remark:            fmt.Sprintf("评价返现追缴（发放 #%d，提交 #%d），应追缴 %.6f，实际扣回 %.6f，差额 %.6f", award.ID, claim.ID, amount, deducted, gap),
		}
		if err := reviewRebateSession(tx).Create(&record).Error; err != nil {
			return err
		}
		return reviewRebateSession(tx).Model(&entity.ReviewRebateClaim{}).Where("id = ?", claim.ID).
			Update("status", entity.ReviewRebateStatusClawedBack).Error
	})
	return row, err
}
