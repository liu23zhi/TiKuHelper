package service

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrRechargeAmountInvalid = errors.New("充值金额不合法")
var ErrRechargeAmountPrecision = errors.New("充值金额最多支持两位小数")
var ErrRechargeAmountBelowMinimum = errors.New("充值金额低于最低充值金额")
var ErrRechargePaymentConfigIncomplete = errors.New("支付配置不完整")
var ErrRechargePaymentMethodNotFound = errors.New("支付方式不存在")
var ErrRechargePaymentMethodDisabled = errors.New("支付方式未启用")
var ErrRechargeNotifyInvalidSignature = errors.New("充值回调签名校验失败")
var ErrRechargeNotifyAmountMismatch = errors.New("充值回调金额不匹配")
var ErrRechargePaymentCreateFailed = errors.New("支付下单失败")

const rechargeOrderStatusPending = "pending"
const rechargeOrderStatusPaid = "paid"
const rechargeOrderTradeStatusSuccess = "TRADE_SUCCESS"
const rechargeBillingRecordType = "recharge"
const rechargeOrderName = "账户充值"
const rechargeOrderSignType = "RSA"

type CreateRechargeOrderInput struct {
	Amount          float64 `json:"amount"`
	PaymentMethodID int32   `json:"paymentMethodId"`
	ClientIP        string  `json:"-"`
	Device          string  `json:"-"`
}

type RechargePaymentConfig struct {
	PaymentProvider    string
	PaymentMethod      string
	GatewayURL         string
	PartnerID          string
	PlatformPublicKey  string
	MerchantPrivateKey string
	NotifyURL          string
	ReturnURL          string
}

type RechargePayRequest struct {
	OutTradeNo string
	Name       string
	Money      string
	Type       string
	ClientIP   string
	Device     string
}

type CreateRechargeOrderResult struct {
	Order             entity.RechargeOrder `json:"order"`
	PayURL            string               `json:"payUrl,omitempty"`
	PayType           string               `json:"payType"`
	PayInfo           string               `json:"payInfo"`
	QRCodeContent     string               `json:"qrcodeContent,omitempty"`
	ThirdPartyOrderNo string               `json:"thirdPartyOrderNo,omitempty"`
}

type RechargePaymentResult struct {
	PayURL            string `json:"payUrl,omitempty"`
	PayType           string `json:"payType"`
	PayInfo           string `json:"payInfo"`
	QRCodeContent     string `json:"qrcodeContent,omitempty"`
	ThirdPartyOrderNo string `json:"thirdPartyOrderNo,omitempty"`
}

type rechargePaymentCreateError struct {
	message string
}

func (err rechargePaymentCreateError) Error() string {
	message := strings.TrimSpace(err.message)
	if message == "" || message == ErrRechargePaymentCreateFailed.Error() {
		return ErrRechargePaymentCreateFailed.Error()
	}
	return ErrRechargePaymentCreateFailed.Error() + "：" + message
}

func (err rechargePaymentCreateError) Unwrap() error {
	return ErrRechargePaymentCreateFailed
}

type RechargeOrderListItem struct {
	ID                        int32      `json:"id"`
	UserID                    int32      `json:"userId"`
	Username                  string     `json:"username"`
	OrderNo                   string     `json:"orderNo"`
	PaymentProvider           string     `json:"paymentProvider"`
	PaymentMethodNameSnapshot string     `json:"paymentMethodNameSnapshot"`
	Amount                    float64    `json:"amount"`
	Status                    string     `json:"status"`
	ThirdPartyOrderNo         string     `json:"thirdPartyOrderNo"`
	PaidAt                    *time.Time `json:"paidAt"`
	CreatedAt                 time.Time  `json:"createdAt"`
	UpdatedAt                 time.Time  `json:"updatedAt"`
}

// RechargeOrderListSummary 是充值记录列表的金额统计。
//
// TotalAmount 覆盖全部符合权限与筛选条件的订单，CurrentPageAmount 只覆盖当前页。
// 两者使用同一套基础查询，避免分页或筛选导致顶部统计口径漂移。
type RechargeOrderListSummary struct {
	TotalAmount       float64 `json:"totalAmount"`
	CurrentPageAmount float64 `json:"currentPageAmount"`
}

type RechargeOrderListResult struct {
	Data     []RechargeOrderListItem  `json:"data"`
	Total    int64                    `json:"total"`
	Page     int                      `json:"page"`
	PageSize int                      `json:"pageSize"`
	Summary  RechargeOrderListSummary `json:"summary"`
}

var rechargeHTTPClient = &http.Client{Timeout: 15 * time.Second}

var createRechargePaymentRequest = func(config RechargePaymentConfig, request RechargePayRequest) (RechargePaymentResult, error) {
	params := url.Values{}
	params.Set("pid", strings.TrimSpace(config.PartnerID))
	params.Set("method", "web")
	params.Set("device", normalizeRechargeDevice(request.Device))
	params.Set("type", strings.TrimSpace(request.Type))
	params.Set("notify_url", strings.TrimSpace(config.NotifyURL))
	params.Set("return_url", strings.TrimSpace(config.ReturnURL))
	params.Set("out_trade_no", strings.TrimSpace(request.OutTradeNo))
	params.Set("name", strings.TrimSpace(request.Name))
	params.Set("money", strings.TrimSpace(request.Money))
	params.Set("clientip", normalizeRechargeClientIP(request.ClientIP))
	params.Set("timestamp", strconv.FormatInt(time.Now().Unix(), 10))

	signature, err := signRechargeValues(params, config.MerchantPrivateKey)
	if err != nil {
		return RechargePaymentResult{}, err
	}
	params.Set("sign", signature)
	params.Set("sign_type", rechargeOrderSignType)

	gatewayURL := strings.TrimRight(strings.TrimSpace(config.GatewayURL), "/")
	if gatewayURL == "" {
		return RechargePaymentResult{}, ErrRechargePaymentConfigIncomplete
	}

	apiURL := gatewayURL + "/api/pay/create"
	httpReq, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewBufferString(params.Encode()))
	if err != nil {
		return RechargePaymentResult{}, wrapRechargePaymentCreateError("支付网关地址无效")
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := rechargeHTTPClient.Do(httpReq)
	if err != nil {
		return RechargePaymentResult{}, wrapRechargePaymentCreateError("支付下单接口请求失败：" + err.Error())
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return RechargePaymentResult{}, wrapRechargePaymentCreateError("读取支付下单响应失败")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return RechargePaymentResult{}, wrapRechargePaymentCreateError("支付下单接口响应异常（HTTP " + strconv.Itoa(resp.StatusCode) + "）")
	}

	var payload struct {
		Code      int    `json:"code"`
		Msg       string `json:"msg"`
		TradeNo   string `json:"trade_no"`
		PayType   string `json:"pay_type"`
		PayInfo   string `json:"pay_info"`
		PayURL    string `json:"payurl"`
		URL       string `json:"url"`
		QRCode    string `json:"qrcode"`
		Timestamp string `json:"timestamp"`
		Sign      string `json:"sign"`
		SignType  string `json:"sign_type"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		finalURL := ""
		if resp.Request != nil && resp.Request.URL != nil {
			finalURL = strings.TrimSpace(resp.Request.URL.String())
		}
		if finalURL != "" && finalURL != apiURL {
			return RechargePaymentResult{
				PayURL:  finalURL,
				PayType: "jump",
				PayInfo: finalURL,
			}, nil
		}
		return RechargePaymentResult{}, wrapRechargePaymentCreateError("支付下单接口响应格式异常")
	}
	if payload.Code != 0 {
		message := strings.TrimSpace(payload.Msg)
		if message == "" {
			message = "支付下单失败"
		}
		return RechargePaymentResult{}, wrapRechargePaymentCreateError(message)
	}

	payType := strings.ToLower(strings.TrimSpace(payload.PayType))
	payInfo := firstNonEmptyRechargePaymentValue(payload.PayInfo, payload.PayURL, payload.URL, payload.QRCode)
	if payInfo == "" {
		return RechargePaymentResult{}, wrapRechargePaymentCreateError("易支付API响应异常，缺少支付信息")
	}
	if payType == "" {
		if strings.TrimSpace(payload.PayURL) != "" || strings.TrimSpace(payload.URL) != "" {
			payType = "jump"
		} else {
			payType = "qrcode"
		}
	}
	result := RechargePaymentResult{
		PayType:           payType,
		PayInfo:           payInfo,
		ThirdPartyOrderNo: strings.TrimSpace(payload.TradeNo),
	}
	switch payType {
	case "qrcode":
		result.QRCodeContent = payInfo
	case "jump", "urlscheme":
		result.PayURL = payInfo
	}
	return result, nil
}

func firstNonEmptyRechargePaymentValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// RechargeOrderListQuery 是充值记录的筛选条件。
type RechargeOrderListQuery struct {
	// Keyword 同时匹配订单号、第三方订单号、用户名、支付方式与支付渠道。
	Keyword string
	// Status 精确匹配订单状态（pending / paid）。
	Status string
	// PaymentProvider 精确匹配支付渠道。
	PaymentProvider string
}

func ListRechargeOrdersWithDB(db *gorm.DB, operator entity.User, query RechargeOrderListQuery, page int, pageSize int) (RechargeOrderListResult, error) {
	baseQuery := applyRechargeOrderFilters(buildRechargeOrderListQuery(db, operator), query)

	var total int64
	if err := baseQuery.Count(&total).Error; err != nil {
		return RechargeOrderListResult{}, err
	}

	// 金额汇总必须复用同一套权限和筛选条件，但不能带分页。
	var totalAmount float64
	if err := baseQuery.Session(&gorm.Session{}).
		Select("COALESCE(SUM(recharge_orders.amount), 0)").
		Scan(&totalAmount).Error; err != nil {
		return RechargeOrderListResult{}, err
	}

	var records []RechargeOrderListItem
	offset := (page - 1) * pageSize
	if err := baseQuery.
		Select("recharge_orders.id, recharge_orders.user_id, users.username, recharge_orders.order_no, recharge_orders.payment_provider, recharge_orders.payment_method_name_snapshot, recharge_orders.amount, recharge_orders.status, recharge_orders.third_party_order_no, recharge_orders.paid_at, recharge_orders.created_at, recharge_orders.updated_at").
		Order("recharge_orders.id desc").
		Limit(pageSize).
		Offset(offset).
		Find(&records).Error; err != nil {
		return RechargeOrderListResult{}, err
	}

	currentPageAmount := 0.0
	for _, record := range records {
		currentPageAmount += record.Amount
	}

	return RechargeOrderListResult{
		Data:     records,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
		Summary: RechargeOrderListSummary{
			TotalAmount:       normalizeRechargeAmount(totalAmount),
			CurrentPageAmount: normalizeRechargeAmount(currentPageAmount),
		},
	}, nil
}

// buildRechargeOrderListQuery 构造充值记录的基础查询。
//
// JOIN 必须在这里完成：关键词筛选会用到 users.username，
// 若只在取数据时才 JOIN，Count 会因为缺列而报错（或算出与数据不一致的 total）。
func buildRechargeOrderListQuery(db *gorm.DB, operator entity.User) *gorm.DB {
	baseQuery := db.Table(entity.TableNameRechargeOrder + " AS recharge_orders").
		Joins("LEFT JOIN " + entity.TableNameUser + " AS users ON users.id = recharge_orders.user_id")
	if !isAdminUser(operator) {
		baseQuery = baseQuery.Where("recharge_orders.user_id = ?", operator.ID)
	}
	return baseQuery
}

// applyRechargeOrderFilters 把关键词、状态与支付渠道筛选应用到充值记录查询上。
func applyRechargeOrderFilters(query *gorm.DB, filter RechargeOrderListQuery) *gorm.DB {
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("recharge_orders.status = ?", status)
	}
	if provider := strings.TrimSpace(filter.PaymentProvider); provider != "" {
		query = query.Where("recharge_orders.payment_provider = ?", provider)
	}

	// 关键词按「纯子串」匹配（不使用 LIKE 通配符），见 util.BuildInstrCondition。
	condition, args := util.BuildInstrCondition(filter.Keyword,
		"COALESCE(recharge_orders.order_no, '')",
		"COALESCE(recharge_orders.third_party_order_no, '')",
		"COALESCE(users.username, '')",
		"COALESCE(recharge_orders.payment_method_name_snapshot, '')",
		"COALESCE(recharge_orders.payment_provider, '')",
	)
	if condition == "" {
		return query
	}
	return query.Where(condition, args...)
}

func CreateRechargeOrderWithDB(db *gorm.DB, user entity.User, input CreateRechargeOrderInput) (CreateRechargeOrderResult, error) {
	// 充值金额只接受 2 位小数，避免把无法收款的多余精度写进订单。
	if err := validateRechargeAmount(input.Amount); err != nil {
		return CreateRechargeOrderResult{}, err
	}
	amount := normalizeRechargeAmount(input.Amount)
	minimumAmount := GetMinRechargeAmountWithDB(db)
	if amount < minimumAmount {
		return CreateRechargeOrderResult{}, fmt.Errorf("%w：%.2f 元", ErrRechargeAmountBelowMinimum, minimumAmount)
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
		PaymentProvider:    DefaultPaymentProvider,
		GatewayURL:         strings.TrimSpace(settings.EPayGatewayURL),
		PartnerID:          strings.TrimSpace(settings.EPayPartnerID),
		PlatformPublicKey:  strings.TrimSpace(settings.EPayPlatformPublicKey),
		MerchantPrivateKey: strings.TrimSpace(settings.EPayMerchantPrivateKey),
		NotifyURL:          strings.TrimSpace(settings.EPayNotifyURL),
		ReturnURL:          strings.TrimSpace(settings.EPayReturnURL),
	}
	if !isRechargePaymentConfigComplete(config) {
		return CreateRechargeOrderResult{}, ErrRechargePaymentConfigIncomplete
	}

	order := entity.RechargeOrder{
		UserID:                    user.ID,
		OrderNo:                   generateRechargeOrderNo(user, amount),
		PaymentProvider:           DefaultPaymentProvider,
		PaymentMethodNameSnapshot: strings.TrimSpace(paymentMethod.Name),
		Amount:                    amount,
		Status:                    rechargeOrderStatusPending,
	}

	paymentResult, err := createRechargePaymentRequest(config, RechargePayRequest{
		OutTradeNo: order.OrderNo,
		Name:       rechargeOrderName,
		Money:      formatRechargeAmount(amount),
		Type:       strings.TrimSpace(paymentMethod.EPayType),
		ClientIP:   input.ClientIP,
		Device:     input.Device,
	})
	if err != nil {
		return CreateRechargeOrderResult{}, err
	}
	if strings.TrimSpace(paymentResult.ThirdPartyOrderNo) != "" {
		order.ThirdPartyOrderNo = strings.TrimSpace(paymentResult.ThirdPartyOrderNo)
	}

	if err := db.Create(&order).Error; err != nil {
		return CreateRechargeOrderResult{}, err
	}

	return CreateRechargeOrderResult{
		Order:             order,
		PayURL:            paymentResult.PayURL,
		PayType:           paymentResult.PayType,
		PayInfo:           paymentResult.PayInfo,
		QRCodeContent:     paymentResult.QRCodeContent,
		ThirdPartyOrderNo: paymentResult.ThirdPartyOrderNo,
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

		// 余额是 6 位精度，这里必须用钱包口径，不能用充值口径（2 位）。
		balanceBefore := normalizeWalletAmount(user.Balance)
		balanceAfter := normalizeWalletAmount(balanceBefore + order.Amount)
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

func normalizeRechargeClientIP(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "127.0.0.1"
	}
	return trimmed
}

func normalizeRechargeDevice(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "mobile", "qq", "wechat", "alipay", "douyin":
		return normalized
	default:
		return "pc"
	}
}

func generateRechargeOrderNo(user entity.User, amount float64) string {
	return buildRechargeOrderNo(user, amount, time.Now(), strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
}

func buildRechargeOrderNo(user entity.User, amount float64, now time.Time, suffix string) string {
	username := normalizeRechargeOrderUsername(user.Username)
	if username == "" {
		username = "user" + strconv.FormatInt(int64(user.ID), 10)
	}
	if len(username) > 18 {
		username = username[:18]
	}
	amountCents := int64(math.Round(normalizeRechargeAmount(amount) * 100))
	normalizedSuffix := normalizeRechargeOrderUsername(suffix)
	if len(normalizedSuffix) > 8 {
		normalizedSuffix = normalizedSuffix[:8]
	}
	if normalizedSuffix == "" {
		normalizedSuffix = "00000000"
	}
	return "rcg_" + username + "_" + now.Format("20060102150405") + "_" + strconv.FormatInt(amountCents, 10) + "_" + normalizedSuffix
}

func normalizeRechargeOrderUsername(value string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if r <= unicode.MaxASCII && ((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// normalizeRechargeAmount 只用于充值单金额：第三方支付渠道只支持「分」，
// 因此充值金额限制为 2 位小数（超精度由校验拒绝，见 validateRechargeAmount）。
func normalizeRechargeAmount(amount float64) float64 {
	return util.RoundMoneyToDecimals(amount, util.RechargePrecision)
}

// normalizeWalletAmount 用于余额与账单金额：保持与余额一致的 6 位精度。
// 绝不能复用 normalizeRechargeAmount，否则会把余额的小数位截断成 2 位。
func normalizeWalletAmount(amount float64) float64 {
	return util.RoundMoney(amount)
}

func validateRechargeAmount(amount float64) error {
	if amount <= 0 {
		return ErrRechargeAmountInvalid
	}
	if util.HasMoreDecimalsThan(amount, util.RechargePrecision) {
		return ErrRechargeAmountPrecision
	}
	return nil
}

func formatRechargeAmount(amount float64) string {
	return strconv.FormatFloat(normalizeRechargeAmount(amount), 'f', 2, 64)
}

func signRechargeValues(values url.Values, privateKey string) (string, error) {
	signContent := buildRechargeSignContent(values)
	rsaKey, err := parseRechargePrivateKey(privateKey)
	if err != nil {
		return "", err
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

	rsaKey, err := parseRechargePublicKey(publicKey)
	if err != nil {
		return false
	}

	hash := sha256.Sum256([]byte(buildRechargeSignContent(values)))
	return rsa.VerifyPKCS1v15(rsaKey, crypto.SHA256, hash[:], signature) == nil
}

func parseRechargePrivateKey(value string) (*rsa.PrivateKey, error) {
	keyText := strings.TrimSpace(value)
	if keyText == "" {
		return nil, errors.New("empty rsa private key")
	}
	if block, _ := pem.Decode([]byte(keyText)); block != nil {
		if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			if rsaKey, ok := key.(*rsa.PrivateKey); ok {
				return rsaKey, nil
			}
		}
		if rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			return rsaKey, nil
		}
		return nil, errors.New("invalid rsa private key")
	}

	privateDER, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil {
		return nil, err
	}
	if key, err := x509.ParsePKCS8PrivateKey(privateDER); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	if rsaKey, err := x509.ParsePKCS1PrivateKey(privateDER); err == nil {
		return rsaKey, nil
	}
	return nil, errors.New("invalid rsa private key")
}

func parseRechargePublicKey(value string) (*rsa.PublicKey, error) {
	keyText := strings.TrimSpace(value)
	if keyText == "" {
		return nil, errors.New("empty rsa public key")
	}
	if block, _ := pem.Decode([]byte(keyText)); block != nil {
		if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
			if rsaKey, ok := key.(*rsa.PublicKey); ok {
				return rsaKey, nil
			}
		}
		if rsaKey, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
			return rsaKey, nil
		}
		return nil, errors.New("invalid rsa public key")
	}

	publicDER, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil {
		return nil, err
	}
	if key, err := x509.ParsePKIXPublicKey(publicDER); err == nil {
		if rsaKey, ok := key.(*rsa.PublicKey); ok {
			return rsaKey, nil
		}
	}
	if rsaKey, err := x509.ParsePKCS1PublicKey(publicDER); err == nil {
		return rsaKey, nil
	}
	return nil, errors.New("invalid rsa public key")
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

func wrapRechargePaymentCreateError(message string) error {
	normalized := strings.TrimSpace(message)
	if normalized == "" {
		normalized = ErrRechargePaymentCreateFailed.Error()
	}
	return rechargePaymentCreateError{message: normalized}
}
