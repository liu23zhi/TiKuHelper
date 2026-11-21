package service

import (
	"crypto/hmac"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
)

type ScriptActivationCodePurchaseRecord struct {
	EntitlementID int64      `json:"entitlementId"`
	Code          string     `json:"code,omitempty"`
	Price         float64    `json:"price"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"createdAt"`
	RedeemedAt    *time.Time `json:"redeemedAt,omitempty"`
	FrozenUntil   *time.Time `json:"frozenUntil,omitempty"`
	ScriptBoundAt *time.Time `json:"scriptBoundAt,omitempty"`
	ReceiptError  string     `json:"receiptError,omitempty"`
}

type ScriptActivationCodePurchaseListResult struct {
	Items    []ScriptActivationCodePurchaseRecord `json:"items"`
	Total    int64                                `json:"total"`
	Page     int                                  `json:"page"`
	PageSize int                                  `json:"pageSize"`
}

// ListScriptActivationCodePurchasesWithDB projects wallet receipts for the
// purchaser. A missing historical signing key affects only code recovery.
func ListScriptActivationCodePurchasesWithDB(db *gorm.DB, userID int32, signingKey []byte, page, pageSize int) (ScriptActivationCodePurchaseListResult, error) {
	if db == nil || userID <= 0 {
		return ScriptActivationCodePurchaseListResult{}, ErrScriptForbidden
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.ScriptActivationEntitlement{}).
		Where("user_id = ? AND purchase_price > 0", userID)
	result := ScriptActivationCodePurchaseListResult{
		Items: make([]ScriptActivationCodePurchaseRecord, 0), Page: page, PageSize: pageSize,
	}
	if err := query.Count(&result.Total).Error; err != nil {
		return ScriptActivationCodePurchaseListResult{}, err
	}
	if int64(page-1) > result.Total/int64(pageSize) {
		return result, nil
	}
	var rows []entity.ScriptActivationEntitlement
	if err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return ScriptActivationCodePurchaseListResult{}, err
	}
	now := db.NowFunc().UTC()
	for _, row := range rows {
		item := ScriptActivationCodePurchaseRecord{
			EntitlementID: row.ID, Price: row.PurchasePrice, CreatedAt: row.CreatedAt, Status: entity.ScriptActivationCodeStatusUnused,
		}
		if row.ScriptID != nil {
			item.Status = entity.ScriptActivationCodeStatusUsed
			item.RedeemedAt = row.RedeemedAt
			item.ScriptBoundAt = row.ScriptBoundAt
		} else if until := activeScriptActivationFreeze(row, now); until != nil {
			item.Status = ScriptActivationCodeStatusFrozen
			item.RedeemedAt = row.RedeemedAt
			item.FrozenUntil = until
		}
		code, err := recoverScriptActivationCode(row, signingKey)
		if err != nil {
			item.ReceiptError = err.Error()
		} else {
			item.Code = code
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func recoverScriptActivationCode(row entity.ScriptActivationEntitlement, signingKey []byte) (string, error) {
	if len(signingKey) < 32 {
		return "", ErrScriptActivationReceiptUnavailable
	}
	code := deriveScriptActivationCode(signingKey, row.UserID, row.PurchaseNonce)
	if !hmac.Equal([]byte(hashScriptSecret(code)), []byte(row.CodeHash)) {
		return "", ErrScriptActivationReceiptUnavailable
	}
	return code, nil
}
