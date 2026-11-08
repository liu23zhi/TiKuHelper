package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ScriptActivationCodePriceSettingKey = "script_activation_code_price"
	ScriptActivationPurchaseBillingType = "script_activation_purchase"
	scriptActivationCodePrefix          = "sac_"
	scriptActivationMicrosPerYuan       = 1_000_000
	// Keep decimal fractions distinguishable at the binary64 boundary used
	// by the existing wallet model. All calculation is in integer micros.
	scriptActivationMoneyMax = 1_000_000_000
)

var (
	ErrScriptActivationSalesDisabled          = errors.New("激活码购买未开放")
	ErrScriptActivationPriceInvalid           = errors.New("激活码价格无效，请填写非负金额（最多六位小数）")
	ErrScriptActivationPriceChanged           = errors.New("激活码售价已变化，请刷新后确认新售价")
	ErrScriptActivationCodeInvalid            = errors.New("激活码无效或不属于当前账号")
	ErrScriptActivationEntitlementUnavailable = errors.New("脚本激活资格不可用或已使用")
	ErrScriptActivationRunConsumed            = errors.New("此脚本的一次运行资格已使用")
	ErrScriptActivationReceiptUnavailable     = errors.New("激活码购买凭据暂不可恢复，请勿重复购买")
	ErrScriptActivationWalletInvalid          = errors.New("余额数据异常，未扣款")
)

type ScriptActivationCodePurchase struct {
	EntitlementID int64   `json:"entitlementId"`
	Code          string  `json:"code"`
	Price         float64 `json:"price"`
}

// GetScriptActivationCodePriceWithDB is fail-closed. No unconfigured, free,
// noncanonical or sub-micro amount can open the wallet purchase route.
func GetScriptActivationCodePriceWithDB(db *gorm.DB) (float64, error) {
	price, err := scriptActivationCodePriceMicrosWithDB(db)
	return float64(price) / scriptActivationMicrosPerYuan, err
}

func scriptActivationCodePriceMicrosWithDB(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, ErrScriptActivationSalesDisabled
	}
	setting, found, err := getSystemSettingByKey(db, ScriptActivationCodePriceSettingKey)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrScriptActivationSalesDisabled
	}
	micros, err := parseScriptActivationCodePriceMicros(setting.Value)
	if err != nil || micros <= 0 {
		return 0, ErrScriptActivationSalesDisabled
	}
	return micros, nil
}

// SetScriptActivationCodePriceWithDB persists the validated price. Zero closes
// sales without deleting historical purchases; only a currently enabled
// administrator can change the setting.
func SetScriptActivationCodePriceWithDB(db *gorm.DB, operatorID int32, raw string) (float64, error) {
	if db == nil || operatorID <= 0 {
		return 0, ErrScriptForbidden
	}
	micros, err := parseScriptActivationCodePriceMicros(raw)
	if err != nil {
		return 0, err
	}
	price := float64(micros) / scriptActivationMicrosPerYuan
	err = db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var operator entity.User
		err := tx.Select("id", "role").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", operatorID, "enabled").First(&operator).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrScriptForbidden
		}
		if err != nil {
			return err
		}
		if !isAdminUser(operator) {
			return ErrScriptForbidden
		}
		value := strconv.FormatFloat(price, 'f', 6, 64)
		setting := entity.SystemSetting{Key: ScriptActivationCodePriceSettingKey, Value: value, Remark: "一次性脚本激活码钱包购买价格"}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.Assignments(map[string]any{"value": value, "updated_at": time.Now()}),
		}).Create(&setting).Error
	})
	if err != nil {
		return 0, err
	}
	return price, nil
}

func parseScriptActivationCodePriceMicros(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	whole, fraction, hasFraction := strings.Cut(value, ".")
	if whole == "" || len(whole) > 9 || (hasFraction && (fraction == "" || len(fraction) > 6)) ||
		strings.Trim(whole, "0123456789") != "" || strings.Trim(fraction, "0123456789") != "" {
		return 0, ErrScriptActivationPriceInvalid
	}
	wholeMicros, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, ErrScriptActivationPriceInvalid
	}
	fraction += strings.Repeat("0", 6-len(fraction))
	fractionMicros, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, ErrScriptActivationPriceInvalid
	}
	micros := wholeMicros*scriptActivationMicrosPerYuan + fractionMicros
	if micros >= scriptActivationMoneyMax*scriptActivationMicrosPerYuan {
		return 0, ErrScriptActivationPriceInvalid
	}
	return micros, nil
}

// PurchaseScriptActivationCodeWithDB debits only existing wallet funds. No
// payment provider is contacted and no phone/WeChat proof is required here.
// A repeated authenticated request using the same purchase key reproduces the
// same code even if prices have since changed; it never debits again. Use a
// stable server signing key of at least 32 bytes and send grants with no-store.
func PurchaseScriptActivationCodeWithDB(db *gorm.DB, userID int32, idempotencyKey string, signingKey []byte) (ScriptActivationCodePurchase, error) {
	return purchaseScriptActivationCodeWithDB(db, userID, idempotencyKey, signingKey, nil)
}

// PurchaseScriptActivationCodeAtQuoteWithDB never charges a price other than
// the one explicitly accepted by the buyer. Replays compare with the original
// receipt, not with the current configuration.
func PurchaseScriptActivationCodeAtQuoteWithDB(db *gorm.DB, userID int32, idempotencyKey string, signingKey []byte, quotedPrice string) (ScriptActivationCodePurchase, error) {
	micros, err := parseScriptActivationCodePriceMicros(quotedPrice)
	if err != nil || micros <= 0 {
		return ScriptActivationCodePurchase{}, ErrScriptActivationPriceInvalid
	}
	return purchaseScriptActivationCodeWithDB(db, userID, idempotencyKey, signingKey, &micros)
}

func purchaseScriptActivationCodeWithDB(db *gorm.DB, userID int32, idempotencyKey string, signingKey []byte, quotedMicros *int64) (ScriptActivationCodePurchase, error) {
	if db == nil || userID <= 0 {
		return ScriptActivationCodePurchase{}, ErrScriptForbidden
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 256 {
		return ScriptActivationCodePurchase{}, ErrIdempotencyKeyInvalid
	}
	if len(signingKey) < 32 {
		return ScriptActivationCodePurchase{}, ErrScriptActivationSalesDisabled
	}
	purchaseKeyHash := hashIdempotencyValue(idempotencyKey)
	var result ScriptActivationCodePurchase
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var user entity.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", userID, "enabled").First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrScriptForbidden
			}
			return err
		}

		var row entity.ScriptActivationEntitlement
		err := tx.Where("user_id = ? AND purchase_key_hash = ?", user.ID, purchaseKeyHash).First(&row).Error
		if err == nil {
			if quotedMicros != nil && row.PurchasePrice != float64(*quotedMicros)/scriptActivationMicrosPerYuan {
				return ErrScriptActivationPriceChanged
			}
			code := deriveScriptActivationCode(signingKey, user.ID, row.PurchaseNonce)
			if !hmac.Equal([]byte(hashScriptSecret(code)), []byte(row.CodeHash)) {
				return ErrScriptActivationReceiptUnavailable
			}
			result = ScriptActivationCodePurchase{EntitlementID: row.ID, Code: code, Price: row.PurchasePrice}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		priceMicros, err := scriptActivationCodePriceMicrosWithDB(tx)
		if err != nil {
			return err
		}
		if quotedMicros != nil && *quotedMicros != priceMicros {
			return ErrScriptActivationPriceChanged
		}
		if math.IsNaN(user.Balance) || math.IsInf(user.Balance, 0) || math.Abs(user.Balance) >= scriptActivationMoneyMax {
			return ErrScriptActivationWalletInvalid
		}
		balanceMicros := int64(math.Round(user.Balance * scriptActivationMicrosPerYuan))
		// The wallet is a decimal(18,6). Reject malformed values instead of
		// implicitly rounding a different amount as part of a purchase.
		if float64(balanceMicros)/scriptActivationMicrosPerYuan != user.Balance {
			return ErrScriptActivationWalletInvalid
		}
		if balanceMicros < priceMicros {
			return ErrScriptBalanceInsufficient
		}

		nonce, err := randomScriptSecret(32)
		if err != nil {
			return err
		}
		price := float64(priceMicros) / scriptActivationMicrosPerYuan
		code := deriveScriptActivationCode(signingKey, user.ID, nonce)
		row = entity.ScriptActivationEntitlement{
			UserID: user.ID, PurchaseKeyHash: purchaseKeyHash, PurchaseNonce: nonce,
			CodeHash: hashScriptSecret(code), PurchasePrice: price,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		after := float64(balanceMicros-priceMicros) / scriptActivationMicrosPerYuan
		updated := tx.Model(&entity.User{}).Where("id = ? AND status = ? AND balance = ?", user.ID, "enabled", user.Balance).
			Update("balance", after)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrIdempotencyPending
		}
		bill := entity.BillingRecord{
			UserID: user.ID, Type: ScriptActivationPurchaseBillingType, Provider: "script",
			Amount: price, BalanceBefore: user.Balance, BalanceAfter: after,
			RequestID: fmt.Sprintf("script_activation_purchase_%d", row.ID), Remark: "购买一次性脚本激活码",
		}
		if err := tx.Create(&bill).Error; err != nil {
			return err
		}
		result = ScriptActivationCodePurchase{EntitlementID: row.ID, Code: code, Price: price}
		return nil
	})
	if err != nil {
		return ScriptActivationCodePurchase{}, err
	}
	return result, nil
}

func deriveScriptActivationCode(signingKey []byte, userID int32, nonce string) string {
	mac := hmac.New(sha256.New, signingKey)
	_, _ = fmt.Fprintf(mac, "tikuhelper:script-activation-purchase:v1:%d:%s", userID, nonce)
	return scriptActivationCodePrefix + hex.EncodeToString(mac.Sum(nil))
}

func validScriptActivationCode(code string) bool {
	digest := strings.TrimPrefix(code, scriptActivationCodePrefix)
	return len(code) == len(scriptActivationCodePrefix)+64 && len(digest) == 64 && strings.Trim(digest, "0123456789abcdef") == ""
}

// RedeemScriptActivationCodeWithDB grants an account exactly one entitlement.
// No wallet/phone/WeChat test is applied here. Replaying redemption as the
// purchasing owner returns the same row and cannot produce a second grant.
func RedeemScriptActivationCodeWithDB(db *gorm.DB, userID int32, code string) (entity.ScriptActivationEntitlement, error) {
	code = strings.TrimSpace(code)
	if db == nil || userID <= 0 || !validScriptActivationCode(code) {
		return entity.ScriptActivationEntitlement{}, ErrScriptActivationCodeInvalid
	}
	var row entity.ScriptActivationEntitlement
	err := db.Session(&gorm.Session{NewDB: true}).Transaction(func(tx *gorm.DB) error {
		var user entity.User
		err := tx.Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status = ?", userID, "enabled").First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrScriptActivationCodeInvalid
		}
		if err != nil {
			return err
		}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND code_hash = ?", user.ID, hashScriptSecret(code)).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrScriptActivationCodeInvalid
		}
		if err != nil || row.RedeemedAt != nil {
			return err
		}
		now := time.Now().UTC().Truncate(time.Second)
		updated := tx.Model(&entity.ScriptActivationEntitlement{}).Where("id = ? AND user_id = ? AND redeemed_at IS NULL", row.ID, userID).Update("redeemed_at", now)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrIdempotencyPending
		}
		row.RedeemedAt = &now
		return nil
	})
	if err != nil {
		return entity.ScriptActivationEntitlement{}, err
	}
	return row, nil
}

func GetScriptActivationEntitlementByScriptWithDB(db *gorm.DB, userID, scriptID int32) (entity.ScriptActivationEntitlement, bool, error) {
	if db == nil || userID <= 0 || scriptID <= 0 {
		return entity.ScriptActivationEntitlement{}, false, ErrScriptActivationEntitlementUnavailable
	}
	var row entity.ScriptActivationEntitlement
	err := db.Session(&gorm.Session{NewDB: true}).Where("user_id = ? AND script_id = ? AND redeemed_at IS NOT NULL", userID, scriptID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return entity.ScriptActivationEntitlement{}, false, nil
	}
	return row, err == nil, err
}

// BindScriptActivationEntitlementInTx must be called in the script creation
// transaction, after the caller has freshly checked both account bindings.
// The persisted script is scoped to the owner; no other script can consume
// the same entitlement, including concurrent requests or deleted scripts.
func BindScriptActivationEntitlementInTx(tx *gorm.DB, userID int32, entitlementID int64, scriptID int32) error {
	if tx == nil || userID <= 0 || entitlementID <= 0 || scriptID <= 0 {
		return ErrScriptActivationEntitlementUnavailable
	}
	var script entity.UserScript
	err := tx.Select("id").Where("id = ? AND user_id = ? AND deleted_at IS NULL", scriptID, userID).First(&script).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrScriptActivationEntitlementUnavailable
	}
	if err != nil {
		return err
	}
	var row entity.ScriptActivationEntitlement
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND redeemed_at IS NOT NULL", entitlementID, userID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrScriptActivationEntitlementUnavailable
	}
	if err != nil {
		return err
	}
	if row.ScriptID != nil {
		if *row.ScriptID == scriptID {
			return nil
		}
		return ErrScriptActivationEntitlementUnavailable
	}
	updated := tx.Model(&entity.ScriptActivationEntitlement{}).
		Where("id = ? AND user_id = ? AND script_id IS NULL AND redeemed_at IS NOT NULL", entitlementID, userID).
		Updates(map[string]any{"script_id": scriptID, "script_bound_at": time.Now()})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrScriptActivationEntitlementUnavailable
	}
	return nil
}

// ResolveScriptActivationEntitlementRunInTx locks the bound entitlement so
// an activation transaction can choose between first use and exact retry.
// A nonzero return identifies a prior session; the caller must still verify
// original identity, geo, ticket, expiry and session state before re-delivery.
func ResolveScriptActivationEntitlementRunInTx(tx *gorm.DB, userID, scriptID int32, activationTicketID int64) (int64, error) {
	if tx == nil || userID <= 0 || scriptID <= 0 || activationTicketID <= 0 {
		return 0, ErrScriptActivationEntitlementUnavailable
	}
	var row entity.ScriptActivationEntitlement
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND script_id = ? AND redeemed_at IS NOT NULL", userID, scriptID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, ErrScriptActivationEntitlementUnavailable
	}
	if err != nil {
		return 0, err
	}
	if row.ActivationTicketID == nil && row.RunSessionID == nil && row.RunConsumedAt == nil {
		return 0, nil
	}
	if row.ActivationTicketID != nil && *row.ActivationTicketID == activationTicketID &&
		row.RunSessionID != nil && *row.RunSessionID > 0 && row.RunConsumedAt != nil {
		return *row.RunSessionID, nil
	}
	return 0, ErrScriptActivationRunConsumed
}

// ConsumeScriptActivationEntitlementRunInTx records successful activation
// after a session insert and in the same transaction. Rolling that transaction
// back also restores the right to run. The result is the already-recorded
// session for an exact retry, or the newly-bound session ID.
func ConsumeScriptActivationEntitlementRunInTx(tx *gorm.DB, userID, scriptID int32, activationTicketID, runSessionID int64) (int64, error) {
	if runSessionID <= 0 {
		return 0, ErrScriptActivationEntitlementUnavailable
	}
	oldSession, err := ResolveScriptActivationEntitlementRunInTx(tx, userID, scriptID, activationTicketID)
	if err != nil || oldSession != 0 {
		return oldSession, err
	}
	var ticket entity.ScriptExchangeToken
	err = tx.Select("id").Where("id = ? AND script_id = ? AND purpose = ?", activationTicketID, scriptID, "activation").First(&ticket).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, ErrScriptTokenInvalid
	}
	if err != nil {
		return 0, err
	}
	var session entity.ScriptRunSession
	err = tx.Select("id").Where("id = ? AND script_id = ? AND user_id = ? AND revoked_at IS NULL", runSessionID, scriptID, userID).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, ErrScriptTokenInvalid
	}
	if err != nil {
		return 0, err
	}
	updated := tx.Model(&entity.ScriptActivationEntitlement{}).
		Where("user_id = ? AND script_id = ? AND activation_ticket_id IS NULL AND run_session_id IS NULL AND run_consumed_at IS NULL", userID, scriptID).
		Updates(map[string]any{"activation_ticket_id": activationTicketID, "run_session_id": runSessionID, "run_consumed_at": time.Now()})
	if updated.Error != nil {
		return 0, updated.Error
	}
	if updated.RowsAffected != 1 {
		return 0, ErrScriptActivationRunConsumed
	}
	return runSessionID, nil
}
