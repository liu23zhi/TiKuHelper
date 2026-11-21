package service

import (
	"errors"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func checkScriptActivationRedeemer(tx *gorm.DB, userID int32) error {
	var user entity.User
	err := tx.Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND status = ?", userID, "enabled").First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrScriptActivationCodeInvalid
	}
	if err != nil {
		return err
	}
	banned, err := IsBanned(tx, userID)
	if err != nil {
		return err
	}
	if banned {
		return ErrScriptActivationCodeInvalid
	}
	return nil
}

func scriptActivationRedeemedBy(userID int32) func(*gorm.DB) *gorm.DB {
	return func(tx *gorm.DB) *gorm.DB {
		return tx.Where("(redeemer_id = ? OR (redeemer_id IS NULL AND user_id = ?))", userID, userID)
	}
}

func activationEntitlementRedeemer(row entity.ScriptActivationEntitlement) int32 {
	if row.RedeemerID != nil {
		return *row.RedeemerID
	}
	return row.UserID
}

func activeScriptActivationFreeze(row entity.ScriptActivationEntitlement, now time.Time) *time.Time {
	if row.RedeemedAt == nil || row.ScriptID != nil {
		return nil
	}
	deadline := row.RedeemedAt.Add(ScriptActivationCodeFreezeDuration)
	if row.RedeemExpiresAt != nil {
		deadline = *row.RedeemExpiresAt
	}
	if !deadline.After(now) {
		return nil
	}
	return &deadline
}

// reserveScriptActivationEntitlementInTx expects a locked row. The previous
// redemption is also included in the update predicate to prevent overwrites.
func reserveScriptActivationEntitlementInTx(tx *gorm.DB, row *entity.ScriptActivationEntitlement, userID int32, now time.Time) error {
	if row.ScriptID != nil {
		if activationEntitlementRedeemer(*row) == userID {
			return nil
		}
		return ErrAdminScriptActivationCodeUsed
	}
	if deadline := activeScriptActivationFreeze(*row, now); deadline != nil {
		if activationEntitlementRedeemer(*row) != userID {
			return ErrScriptActivationCodeFrozen
		}
		row.FrozenUntil = deadline
		return nil
	}
	deadline := now.Add(ScriptActivationCodeFreezeDuration)
	query := tx.Model(&entity.ScriptActivationEntitlement{}).Where("id = ? AND script_id IS NULL", row.ID)
	if row.RedeemedAt == nil {
		query = query.Where("redeemed_at IS NULL")
	} else {
		query = query.Where("redeemed_at = ?", row.RedeemedAt)
	}
	updated := query.Updates(map[string]any{
		"redeemer_id": userID, "redeemed_at": now, "redeem_expires_at": deadline,
	})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrIdempotencyPending
	}
	row.RedeemerID = &userID
	row.RedeemedAt = &now
	row.RedeemExpiresAt = &deadline
	row.FrozenUntil = &deadline
	return nil
}
