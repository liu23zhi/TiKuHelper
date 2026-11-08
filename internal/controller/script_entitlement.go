package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

func GetScriptActivationCodePrice(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if _, ok := currentOperator(c); !ok {
		return
	}
	price, err := service.GetScriptActivationCodePriceWithDB(scriptDB())
	if errors.Is(err, service.ErrScriptActivationSalesDisabled) {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "price": 0})
		return
	}
	if err != nil {
		writeScriptActivationCodeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": true, "price": price})
}

func PutScriptActivationCodePrice(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	var input struct {
		Price json.RawMessage `json:"price"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		writeScriptActivationCodeError(c, service.ErrScriptActivationPriceInvalid)
		return
	}
	raw := string(input.Price)
	if strings.HasPrefix(raw, "\"") {
		if err := json.Unmarshal(input.Price, &raw); err != nil {
			writeScriptActivationCodeError(c, service.ErrScriptActivationPriceInvalid)
			return
		}
	}
	price, err := service.SetScriptActivationCodePriceWithDB(scriptDB(), operator.ID, raw)
	if err != nil {
		writeScriptActivationCodeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": price > 0, "price": price})
}

func PurchaseScriptActivationCode(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	db := scriptDB()
	signingKey, err := scriptActivationPurchaseSigningKeyWithDB(db)
	if err != nil {
		writeScriptActivationCodeError(c, err)
		return
	}
	var input struct {
		QuotedPrice *string `json:"quotedPrice"`
	}
	if err := c.ShouldBindJSON(&input); err != nil && !errors.Is(err, io.EOF) {
		writeScriptActivationCodeError(c, service.ErrScriptActivationPriceInvalid)
		return
	}
	var purchase service.ScriptActivationCodePurchase
	key := c.GetHeader("Idempotency-Key")
	if input.QuotedPrice == nil {
		purchase, err = service.PurchaseScriptActivationCodeWithDB(db, operator.ID, key, signingKey)
	} else {
		purchase, err = service.PurchaseScriptActivationCodeAtQuoteWithDB(db, operator.ID, key, signingKey, *input.QuotedPrice)
	}
	if err != nil {
		writeScriptActivationCodeError(c, err)
		return
	}
	c.JSON(http.StatusOK, purchase)
}

// Read the JWT secret that startup actually resolved and persisted rather
// than the unprocessed configuration field. Never generate or rotate a
// signing key in a purchase request. The service domain-separates its HMAC.
func scriptActivationPurchaseSigningKeyWithDB(db *gorm.DB) ([]byte, error) {
	if db == nil {
		return nil, service.ErrScriptActivationSalesDisabled
	}
	var setting entity.SystemSetting
	err := db.Session(&gorm.Session{NewDB: true}).Select("value").
		Where(map[string]any{"key": registry.JWTSecretSettingKey}).First(&setting).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, service.ErrScriptActivationSalesDisabled
	}
	if err != nil {
		return nil, err
	}
	secret := strings.TrimSpace(setting.Value)
	if err := util.ValidateJWTSecret(secret); err != nil {
		return nil, service.ErrScriptActivationSalesDisabled
	}
	return []byte(secret), nil
}

func RedeemScriptActivationCode(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		writeScriptActivationCodeError(c, service.ErrScriptActivationCodeInvalid)
		return
	}
	entitlement, err := service.RedeemScriptActivationCodeWithDB(scriptDB(), operator.ID, input.Code)
	if err != nil {
		writeScriptActivationCodeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": entitlement.ID, "redeemedAt": entitlement.RedeemedAt, "scriptId": entitlement.ScriptID})
}

func writeScriptActivationCodeError(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, "SCRIPT_ACTIVATION_UNAVAILABLE", "脚本激活码服务暂不可用"
	switch {
	case errors.Is(err, service.ErrScriptActivationSalesDisabled):
		status, code, message = http.StatusServiceUnavailable, "SCRIPT_ACTIVATION_SALES_DISABLED", service.ErrScriptActivationSalesDisabled.Error()
	case errors.Is(err, service.ErrScriptForbidden):
		status, code, message = http.StatusForbidden, "SCRIPT_ACTIVATION_FORBIDDEN", service.ErrScriptForbidden.Error()
	case errors.Is(err, service.ErrScriptActivationCodeInvalid):
		status, code, message = http.StatusBadRequest, "SCRIPT_ACTIVATION_CODE_INVALID", service.ErrScriptActivationCodeInvalid.Error()
	case errors.Is(err, service.ErrScriptActivationPriceInvalid):
		status, code, message = http.StatusBadRequest, "SCRIPT_ACTIVATION_PRICE_INVALID", service.ErrScriptActivationPriceInvalid.Error()
	case errors.Is(err, service.ErrScriptActivationPriceChanged):
		status, code, message = http.StatusConflict, "SCRIPT_ACTIVATION_PRICE_CHANGED", service.ErrScriptActivationPriceChanged.Error()
	case errors.Is(err, service.ErrIdempotencyKeyInvalid):
		status, code, message = http.StatusBadRequest, "IDEMPOTENCY_KEY_INVALID", service.ErrIdempotencyKeyInvalid.Error()
	case errors.Is(err, service.ErrScriptBalanceInsufficient):
		status, code, message = http.StatusConflict, "SCRIPT_ACTIVATION_BALANCE_INSUFFICIENT", service.ErrScriptBalanceInsufficient.Error()
	case errors.Is(err, service.ErrIdempotencyPending), errors.Is(err, service.ErrScriptActivationReceiptUnavailable):
		status, code, message = http.StatusConflict, "SCRIPT_ACTIVATION_RECEIPT_UNAVAILABLE", service.ErrScriptActivationReceiptUnavailable.Error()
	}
	c.JSON(status, gin.H{"code": code, "message": message})
}
