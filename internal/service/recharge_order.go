package service

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrRechargeAmountInvalid = errors.New("充值金额不合法")
var ErrRechargePaymentConfigIncomplete = errors.New("支付配置不完整")
var ErrRechargePaymentMethodNotFound = errors.New("支付方式不存在")
var ErrRechargePaymentMethodDisabled = errors.New("支付方式未启用")
var ErrRechargeNotifyInvalidSignature = errors.New("充值回调签名校验失败")
var ErrRechargeNotifyAmountMismatch = errors.New("充值回调金额不匹配")

const rechargeOrderStatusPending = "pending"
const rechargeOrderStatusPaid = "paid"
const rechargeOrderTradeStatusSuccess = "TRADE_SUCCESS"
const rechargeBillingRecordType = "recharge"
const rechargeOrderName = "账户充值"
const rechargeOrderSignType = "RSA"

type CreateRechargeOrderInput struct {
	Amount          float64 `json:"amount"`
	PaymentMethodID int32   `json:"paymentMethodId"`
}

type RechargePaymentConfig struct {
	PaymentProvider     string
	PaymentMethod       string
	GatewayURL          string
	PartnerID           string
	PlatformPublicKey   string
	MerchantPrivateKey  string
	NotifyURL           string
	ReturnURL           string
}

type RechargePayRequest struct {
	OutTradeNo string
	Name       string
	Money      string
	Type       string
}

type CreateRechargeOrderResult struct {
	Order  entity.RechargeOrder `json:"order"`
	PayURL string               `json:"payUrl"`
}

var buildRechargePayURL = func(config RechargePaymentConfig, request RechargePayRequest) (string, error) {
	params := url.Values{}
	params.Set("pid", strings.TrimSpace(config.PartnerID))
	params.Set("type", strings.TrimSpace(request.Type))
	params.Set("notify_url", strings.TrimSpace(config.NotifyURL))
	params.Set("return_url", strings.TrimSpace(config.ReturnURL))
	params.Set("out_trade_no", strings.TrimSpace(request.OutTradeNo))
	params.Set("name", strings.TrimSpace(request.Name))
	params.Set("money", strings.TrimSpace(request.Money))
	params.Set("timestamp", strconv.FormatInt(time.Now().Unix(), 10))

	signature, err := signRechargeValues(params, config.MerchantPrivateKey)
	if err != nil {
		return "", err
	}
	params.Set("sign", signature)
	params.Set("sign_type", rechargeOrderSignType)

	gatewayURL := strings.TrimRight(strings.TrimSpace(config.GatewayURL), "/")
	if gatewayURL == "" {
		return "", ErrRechargePaymentConfigIncomplete
	}
	return gatewayURL + "/api/pay/submit?" + params.Encode(), nil
}

func CreateRechargeOrderWithDB(db *gorm.DB, user entity.User, input CreateRechargeOrderInput) (CreateRechargeOrderResult, error) {
	amount := normalizeRechargeAmount(input.Amount)
	if amount <= 0 {
		return CreateRechargeOrderResult{}, ErrRechargeAmountInvalid
	}

	paymentMethod, err := loadPaymentMethodByID(db, input.PaymentMethodID)
	if err != nil {
		if errors.Is(err, ErrPaymentMethodNotFound) {
			return CreateRechargeOrderResult{}, ErrRechargePaymentMethodNotFound
		}
		return CreateRechargeOrderResult{}, err
	}
	if !paymentMethod.Enabled {
		return CreateRechargeOrderResult{}, ErrRechargePaymentMethodDisabled
	}

	settings, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		return CreateRechargeOrderResult{}, err
	}

	config := RechargePaymentConfig{
		PaymentProvider:     DefaultPaymentProvider,
		GatewayURL:          strings.TrimSpace(settings.EPayGatewayURL),
		PartnerID:           strings.TrimSpace(settings.EPayPartnerID),
		PlatformPublicKey:   strings.TrimSpace(settings.EPayPlatformPublicKey),
		MerchantPrivateKey:  strings.TrimSpace(settings.EPayMerchantPrivateKey),
		NotifyURL:           strings.TrimSpace(settings.EPayNotifyURL),
		ReturnURL:           strings.TrimSpace(settings.EPayReturnURL),
	}
	if !isRechargePaymentConfigComplete(config) {
		return CreateRechargeOrderResult{}, ErrRechargePaymentConfigIncomplete
	}

	order := entity.RechargeOrder{
		UserID:                    user.ID,
		OrderNo:                   generateRechargeOrderNo(),
		PaymentProvider:           DefaultPaymentProvider,
		PaymentMethodNameSnapshot: strings.TrimSpace(paymentMethod.Name),
		Amount:                    amount,
		Status:                    rechargeOrderStatusPending,
	}

	payURL, err := buildRechargePayURL(config, RechargePayRequest{
		OutTradeNo: order.OrderNo,
		Name:       rechargeOrderName,
		Money:      formatRechargeAmount(amount),
		Type:       strings.TrimSpace(paymentMethod.EPayType),
	})
	if err != nil {
		return CreateRechargeOrderResult{}, err
	}

	if err := db.Create(&order).Error; err != nil {
		return CreateRechargeOrderResult{}, err
	}

	return CreateRechargeOrderResult{
		Order:  order,
		PayURL: payURL,
	}, nil
}

func HandleRechargePaymentNotifyWithDB(db *gorm.DB, values url.Values) (bool, error) {
	settings, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		return false, err
	}

	config := RechargePaymentConfig{
		PaymentProvider:   DefaultPaymentProvider,
		PartnerID:         strings.TrimSpace(settings.EPayPartnerID),
		PlatformPublicKey: strings.TrimSpace(settings.EPayPlatformPublicKey),
	}
	if strings.TrimSpace(config.PartnerID) == "" || strings.TrimSpace(config.PlatformPublicKey) == "" {
		return false, ErrRechargePaymentConfigIncomplete
	}

	if !verifyRechargeValuesSignature(values, config.PlatformPublicKey) {
		return false, ErrRechargeNotifyInvalidSignature
	}

	orderNo := strings.TrimSpace(values.Get("out_trade_no"))
	if orderNo == "" {
		return false, gorm.ErrRecordNotFound
	}

	callbackPayload := values.Encode()
	acknowledged := true
	tradeStatus := strings.TrimSpace(values.Get("trade_status"))
	tradeNo := strings.TrimSpace(values.Get("trade_no"))
	paidAmount := normalizeRechargeAmount(parseRechargeAmount(values.Get("money")))

	err = db.Transaction(func(tx *gorm.DB) error {
		var order entity.RechargeOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).First(&order).Error; err != nil {
			return err
		}

		updates := map[string]any{
			"callback_payload": callbackPayload,
		}
		if tradeNo != "" {
			updates["third_party_order_no"] = tradeNo
		}
		if err := tx.Model(&entity.RechargeOrder{}).Where("id = ?", order.ID).Updates(updates).Error; err != nil {
			return err
		}

		if order.Status == rechargeOrderStatusPaid {
			return nil
		}
		if tradeStatus != rechargeOrderTradeStatusSuccess {
			return nil
		}
		if paidAmount <= 0 || normalizeRechargeAmount(order.Amount) != paidAmount {
			return ErrRechargeNotifyAmountMismatch
		}

		var user entity.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", order.UserID).First(&user).Error; err != nil {
			return err
		}

		balanceBefore := normalizeRechargeAmount(user.Balance)
		balanceAfter := normalizeRechargeAmount(balanceBefore + order.Amount)
		now := time.Now()

		if err := tx.Model(&entity.RechargeOrder{}).Where("id = ?", order.ID).Updates(map[string]any{
			"status":               rechargeOrderStatusPaid,
			"third_party_order_no": tradeNo,
			"callback_payload":     callbackPayload,
			"paid_at":              &now,
		}).Error; err != nil {
			return err
		}

		if err := tx.Model(&entity.User{}).Where("id = ?", user.ID).Update("balance", balanceAfter).Error; err != nil {
			return err
		}

		billingRecord := entity.BillingRecord{
			UserID:            user.ID,
			Type:              rechargeBillingRecordType,
			Provider:          DefaultPaymentProvider,
			Amount:            order.Amount,
			BalanceBefore:     balanceBefore,
			BalanceAfter:      balanceAfter,
			RequestID:         order.OrderNo,
			RelatedOrderID:    order.ID,
			UnitPriceSnapshot: callbackPayload,
			Remark:            "账户充值",
		}
		if err := tx.Create(&billingRecord).Error; err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return false, err
	}

	return acknowledged, nil
}

func isRechargePaymentConfigComplete(config RechargePaymentConfig) bool {
	return strings.TrimSpace(config.PaymentProvider) != "" &&
		strings.TrimSpace(config.GatewayURL) != "" &&
		strings.TrimSpace(config.PartnerID) != "" &&
		strings.TrimSpace(config.PlatformPublicKey) != "" &&
		strings.TrimSpace(config.MerchantPrivateKey) != "" &&
		strings.TrimSpace(config.NotifyURL) != "" &&
		strings.TrimSpace(config.ReturnURL) != ""
}

func generateRechargeOrderNo() string {
	return "rcg_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func normalizeRechargeAmount(amount float64) float64 {
	return math.Round(amount*100) / 100
}

func formatRechargeAmount(amount float64) string {
	return strconv.FormatFloat(normalizeRechargeAmount(amount), 'f', 2, 64)
}

func signRechargeValues(values url.Values, privateKey string) (string, error) {
	signContent := buildRechargeSignContent(values)
	privateDER, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privateKey))
	if err != nil {
		return "", err
	}

	parsedKey, err := x509.ParsePKCS8PrivateKey(privateDER)
	if err != nil {
		return "", err
	}

	rsaKey, ok := parsedKey.(*rsa.PrivateKey)
	if !ok {
		return "", errors.New("invalid rsa private key")
	}

	hash := sha256.Sum256([]byte(signContent))
	signature, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func verifyRechargeValuesSignature(values url.Values, publicKey string) bool {
	signatureBase64 := strings.TrimSpace(values.Get("sign"))
	if signatureBase64 == "" {
		return false
	}
	signature, err := base64.StdEncoding.DecodeString(signatureBase64)
	if err != nil {
		return false
	}

	publicDER, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKey))
	if err != nil {
		return false
	}
	parsedKey, err := x509.ParsePKIXPublicKey(publicDER)
	if err != nil {
		return false
	}
	rsaKey, ok := parsedKey.(*rsa.PublicKey)
	if !ok {
		return false
	}

	hash := sha256.Sum256([]byte(buildRechargeSignContent(values)))
	return rsa.VerifyPKCS1v15(rsaKey, crypto.SHA256, hash[:], signature) == nil
}

func parseRechargeAmount(value string) float64 {
	amount, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return amount
}

func buildRechargeSignContent(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key, list := range values {
		if key == "sign" || key == "sign_type" {
			continue
		}
		value := ""
		if len(list) > 0 {
			value = list[0]
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}
	return strings.Join(parts, "&")
}
