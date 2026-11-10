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
	"github.com/itihey/tikuAdapter/pkg/logger"
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

// rechargeOrderStatusRefunded 是订单被运营改成「已退款」后的状态。
//
// 它**不参与**加款幂等判定：status 是可变的运营字段，用它当唯一判据正是
// 「订单退款后旧成功回调重复加款」这个漏洞的成因。幂等锚点是 payment_events
// 的 order_no 唯一事件（见 internal/entity/payment_event.go）。
const rechargeOrderStatusRefunded = "refunded"
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
	// UserID 精确匹配所属用户（管理员筛选；普通用户仍受权限范围限制）。
	UserID int32
	// CreatedFrom/CreatedTo filter order creation time; PaidFrom/PaidTo filter payment time.
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	PaidFrom    *time.Time
	PaidTo      *time.Time
	AmountMin   *float64
	AmountMax   *float64
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
	if filter.UserID > 0 {
		query = query.Where("recharge_orders.user_id = ?", filter.UserID)
	}
	if filter.CreatedFrom != nil {
		query = query.Where("recharge_orders.created_at >= ?", filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		query = query.Where("recharge_orders.created_at < ?", filter.CreatedTo)
	}
	if filter.PaidFrom != nil {
		query = query.Where("recharge_orders.paid_at >= ?", filter.PaidFrom)
	}
	if filter.PaidTo != nil {
		query = query.Where("recharge_orders.paid_at < ?", filter.PaidTo)
	}
	if filter.AmountMin != nil {
		query = query.Where("recharge_orders.amount >= ?", *filter.AmountMin)
	}
	if filter.AmountMax != nil {
		query = query.Where("recharge_orders.amount <= ?", *filter.AmountMax)
	}
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
	config, order, paymentType, err := prepareRechargeOrderWithDB(db, user, input)
	if err != nil {
		writeRechargeCreateFailureAudit(db, user, input, order, paymentType, err)
		return CreateRechargeOrderResult{}, err
	}
	paymentResult, err := createRechargePaymentRequest(config, rechargePayRequest(order, paymentType, input))
	if err != nil {
		_ = writeRechargeCreateAudit(db, user, input, order, paymentType, "pending", "PAYMENT_RESULT_UNKNOWN", "支付下单结果待核对，请勿重复下单")
		return CreateRechargeOrderResult{}, err
	}
	if strings.TrimSpace(paymentResult.ThirdPartyOrderNo) != "" {
		order.ThirdPartyOrderNo = strings.TrimSpace(paymentResult.ThirdPartyOrderNo)
	}
	if err := db.Create(&order).Error; err != nil {
		_ = writeRechargeCreateAudit(db, user, input, order, paymentType, "pending", "ORDER_RESULT_UNKNOWN", "支付订单状态待核对，请勿重复下单")
		return CreateRechargeOrderResult{}, err
	}
	// The gateway already created this legacy order. Audit failures must not
	// discard the local record needed to reconcile a later payment callback.
	if err := writeRechargeCreateAudit(db, user, input, order, paymentType, "success", "", "充值订单创建成功"); err != nil {
		logger.SysError("充值下单成功，但操作日志写入失败: " + logger.RedactSecrets(err.Error()))
	}
	return rechargeResult(order, paymentResult), nil
}

func prepareRechargeOrderWithDB(db *gorm.DB, user entity.User, input CreateRechargeOrderInput) (RechargePaymentConfig, entity.RechargeOrder, string, error) {
	// 充值金额只接受 2 位小数，避免把无法收款的多余精度写进订单。
	if err := validateRechargeAmount(input.Amount); err != nil {
		return RechargePaymentConfig{}, entity.RechargeOrder{}, "", err
	}
	amount := normalizeRechargeAmount(input.Amount)
	minimumAmount := GetMinRechargeAmountWithDB(db)
	if amount < minimumAmount {
		return RechargePaymentConfig{}, entity.RechargeOrder{}, "", fmt.Errorf("%w：%.2f 元", ErrRechargeAmountBelowMinimum, minimumAmount)
	}

	paymentMethod, err := loadPaymentMethodByID(db, input.PaymentMethodID)
	if err != nil {
		if errors.Is(err, ErrPaymentMethodNotFound) {
			return RechargePaymentConfig{}, entity.RechargeOrder{}, "", ErrRechargePaymentMethodNotFound
		}
		return RechargePaymentConfig{}, entity.RechargeOrder{}, "", err
	}
	if !paymentMethod.Enabled {
		return RechargePaymentConfig{}, entity.RechargeOrder{}, "", ErrRechargePaymentMethodDisabled
	}

	settings, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		return RechargePaymentConfig{}, entity.RechargeOrder{}, "", err
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
		return RechargePaymentConfig{}, entity.RechargeOrder{}, "", ErrRechargePaymentConfigIncomplete
	}

	order := entity.RechargeOrder{
		UserID:                    user.ID,
		OrderNo:                   generateRechargeOrderNo(user, amount),
		PaymentProvider:           DefaultPaymentProvider,
		PaymentMethodNameSnapshot: strings.TrimSpace(paymentMethod.Name),
		RefundChannelFingerprint:  refundChannelFingerprint(config.GatewayURL, config.PartnerID, config.PlatformPublicKey),
		Amount:                    amount,
		Status:                    rechargeOrderStatusPending,
	}

	return config, order, strings.TrimSpace(paymentMethod.EPayType), nil
}

func rechargePayRequest(order entity.RechargeOrder, paymentType string, input CreateRechargeOrderInput) RechargePayRequest {
	return RechargePayRequest{
		OutTradeNo: order.OrderNo,
		Name:       rechargeOrderName,
		Money:      formatRechargeAmount(order.Amount),
		Type:       paymentType,
		ClientIP:   input.ClientIP,
		Device:     input.Device,
	}
}

func rechargeResult(order entity.RechargeOrder, paymentResult RechargePaymentResult) CreateRechargeOrderResult {
	return CreateRechargeOrderResult{
		Order:             order,
		PayURL:            paymentResult.PayURL,
		PayType:           paymentResult.PayType,
		PayInfo:           paymentResult.PayInfo,
		QRCodeContent:     paymentResult.QRCodeContent,
		ThirdPartyOrderNo: paymentResult.ThirdPartyOrderNo,
	}
}

func CreateRechargeOrderIdempotentWithDB(db *gorm.DB, user entity.User, input CreateRechargeOrderInput, key string) (CreateRechargeOrderResult, error) {
	request, err := json.Marshal(struct {
		Amount          float64 `json:"amount"`
		PaymentMethodID int32   `json:"paymentMethodId"`
	}{Amount: input.Amount, PaymentMethodID: input.PaymentMethodID})
	if err != nil {
		return CreateRechargeOrderResult{}, err
	}
	record, fresh, err := ClaimIdempotencyWithDB(db, fmt.Sprintf("user:%d", user.ID), "recharge-order", key, request)
	if err != nil {
		return CreateRechargeOrderResult{}, err
	}
	if !fresh {
		var previous CreateRechargeOrderResult
		if record.ResponseJSON != "" {
			if err := json.Unmarshal([]byte(record.ResponseJSON), &previous); err != nil {
				return CreateRechargeOrderResult{}, err
			}
		}
		if record.Status == IdempotencyStatusSucceeded {
			return previous, nil
		}
		if previous.Order.ID > 0 {
			var current entity.RechargeOrder
			if err := db.First(&current, previous.Order.ID).Error; err == nil && current.UserID == user.ID {
				current.CallbackPayload = ""
				previous.Order = current
			}
		}
		return previous, ErrIdempotencyPending
	}
	config, order, paymentType, err := prepareRechargeOrderWithDB(db, user, input)
	if err != nil {
		_ = ReleaseIdempotencyWithDB(db, record.ID)
		writeRechargeCreateFailureAudit(db, user, input, order, paymentType, err)
		return CreateRechargeOrderResult{}, err
	}
	if err := db.Create(&order).Error; err != nil {
		_ = MarkIdempotencyUnknownWithDB(db, record.ID)
		writeRechargeCreateFailureAudit(db, user, input, order, paymentType, err)
		return CreateRechargeOrderResult{}, err
	}
	progress, _ := json.Marshal(CreateRechargeOrderResult{Order: order})
	if err := SaveIdempotencyProgressWithDB(db, record.ID, progress); err != nil {
		_ = MarkIdempotencyUnknownWithDB(db, record.ID)
		_ = writeRechargeCreateAudit(db, user, input, order, paymentType, "pending", "ORDER_RESULT_UNKNOWN", "支付订单状态待核对，请勿重复下单")
		return CreateRechargeOrderResult{Order: order}, ErrIdempotencyPending
	}
	paymentResult, err := createRechargePaymentRequest(config, rechargePayRequest(order, paymentType, input))
	if err != nil {
		_ = MarkIdempotencyUnknownWithDB(db, record.ID)
		_ = writeRechargeCreateAudit(db, user, input, order, paymentType, "pending", "PAYMENT_RESULT_UNKNOWN", "支付下单结果待核对，请勿重复下单")
		return CreateRechargeOrderResult{Order: order}, ErrIdempotencyPending
	}
	var result CreateRechargeOrderResult
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, order.ID).Error; err != nil {
			return err
		}
		if order.ThirdPartyOrderNo == "" && strings.TrimSpace(paymentResult.ThirdPartyOrderNo) != "" {
			order.ThirdPartyOrderNo = strings.TrimSpace(paymentResult.ThirdPartyOrderNo)
			if err := tx.Model(&entity.RechargeOrder{}).Where("id = ?", order.ID).Update("third_party_order_no", order.ThirdPartyOrderNo).Error; err != nil {
				return err
			}
		}
		order.CallbackPayload = ""
		result = rechargeResult(order, paymentResult)
		response, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err := CompleteIdempotencyWithDB(tx, record.ID, http.StatusOK, response); err != nil {
			return err
		}
		return writeRechargeCreateAudit(tx, user, input, order, paymentType, "success", "", "充值订单创建成功")
	}); err != nil {
		_ = MarkIdempotencyUnknownWithDB(db, record.ID)
		_ = writeRechargeCreateAudit(db, user, input, order, paymentType, "pending", "ORDER_RESULT_UNKNOWN", "支付订单状态待核对，请勿重复下单")
		return CreateRechargeOrderResult{Order: order}, ErrIdempotencyPending
	}
	return result, nil
}

func HandleRechargePaymentNotifyWithDB(db *gorm.DB, values url.Values) (bool, error) {
	settings, err := GetSystemSettingDetailWithDB(db)
	if err != nil {
		return false, err
	}

	config := RechargePaymentConfig{
		PaymentProvider:   DefaultPaymentProvider,
		GatewayURL:        strings.TrimSpace(settings.EPayGatewayURL),
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
	var settledAudit *AccountAuditEvent

	err = db.Transaction(func(tx *gorm.DB) error {
		var order entity.RechargeOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).First(&order).Error; err != nil {
			return err
		}
		// Paid is immutable. In particular a replay cannot replace the trade
		// reference later used to prove the original refund channel.
		//
		// 这只是「已结算」的快速通道，**不是**加款幂等的唯一判据：status 会被运营
		// 改成 refunded，此时同一条回调会绕过这个判断。真正的锚点是下面的
		// payment_events 唯一事件（见该处的注释）。
		if order.Status == rechargeOrderStatusPaid {
			// 顺带补记事件：覆盖「历史回填尚未成功 / 回填后被漏掉」的订单。
			// 已 paid 说明钱已经到账，这里只补锚点、绝不加款；补记失败也只记日志，
			// 不能因为事件表缺失就把一个正确的「已结算」回调判成失败。
			if tradeStatus == rechargeOrderTradeStatusSuccess {
				if _, claimErr := claimRechargePaymentEventInTx(tx, order, tradeNo, tradeStatus); claimErr != nil {
					logger.SysWarn(fmt.Sprintf("已结算充值订单补记支付事件失败（不影响本次 ACK）orderNo=%s: %v",
						order.OrderNo, claimErr))
				}
			}
			return nil
		}

		updates := map[string]any{
			"callback_payload": callbackPayload,
		}
		if tradeNo != "" {
			updates["third_party_order_no"] = tradeNo
		}
		if tradeStatus != rechargeOrderTradeStatusSuccess {
			// 非成功状态只留痕，不入账、也不占事件（同一订单后面还可能来成功回调）。
			if err := tx.Model(&entity.RechargeOrder{}).Where("id = ?", order.ID).Updates(updates).Error; err != nil {
				return err
			}
			return nil
		}
		if paidAmount <= 0 || normalizeRechargeAmount(order.Amount) != paidAmount {
			return ErrRechargeNotifyAmountMismatch
		}

		// 加款幂等的**唯一分岔判据**：先尝试记入「支付已入账」唯一事件。
		//
		// 插入成功（firstCredit=true）= 这笔网关订单的首次入账，继续加款；
		// 插入被 OnConflict{DoNothing} 忽略 = 早已入过账，本次是重复回调，
		// 直接 ACK 且**一行都不改、一分钱都不加**。
		//
		// 这一段必须挡在钱包/账单之前：上面的 `status == paid` 只是快速通道，
		// 管理员把订单改成 refunded 后它就不再成立，届时只有这里能拦住重复加款。
		firstCredit, err := claimRechargePaymentEventInTx(tx, order, tradeNo, tradeStatus)
		if err != nil {
			return err
		}
		if !firstCredit {
			return nil
		}

		var user entity.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", order.UserID).First(&user).Error; err != nil {
			return err
		}

		// 余额是 6 位精度，这里必须用钱包口径，不能用充值口径（2 位）。
		balanceBefore := normalizeWalletAmount(user.Balance)
		balanceAfter := normalizeWalletAmount(balanceBefore + order.Amount)
		now := time.Now()

		paidUpdates := map[string]any{
			"status":               rechargeOrderStatusPaid,
			"third_party_order_no": tradeNo,
			"callback_payload":     callbackPayload,
			"paid_at":              &now,
		}
		fingerprint := refundChannelFingerprint(config.GatewayURL, config.PartnerID, config.PlatformPublicKey)
		paidCents, exact := parseRefundGatewayCents(values.Get("money"))
		if exact && float64(paidCents)/100 == order.Amount && validRefundGatewayID(tradeNo) &&
			values.Get("pid") == config.PartnerID && order.RefundChannelFingerprint != "" && order.RefundChannelFingerprint == fingerprint {
			paidUpdates["refund_original_trade_no"] = tradeNo
		}
		if err := tx.Model(&entity.RechargeOrder{}).Where("id = ?", order.ID).Updates(paidUpdates).Error; err != nil {
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

		// 充值阶梯赠送：充值入账落完之后，在同一事务里按档位产生赠金。
		// 开关关闭 / 未达门槛 / 表未迁移 → 返回 nil（降级，不影响到账）。
		// 幂等由 recharge_promotion_grants.recharge_order_id 唯一索引保证 ——
		// 重复回调走不到这里（上面 paid 已提前 return），即使走到了也只会拿到既有记录。
		promotion, err := GrantRechargePromotionInTx(tx, order, balanceAfter)
		if err != nil {
			return err
		}

		// 邀请返佣：充值账单写完后，在同一事务里产生一条待结算奖励。
		// 幂等由 referral_rebates.recharge_order_id 的唯一索引保证 ——
		// 重复回调走不到这里（上面 paid 已提前 return），即使走到了也只会拿到既有记录。
		// 返佣**不写 users.balance / gift_balance**，只进返佣账本（settled 时才入
		// 受益人赠送余额，见 SettleReferralRebateWithDB）。
		// 也不读 gift_balance：返佣基数只认实付本金（referralRebateBaseAmount）。
		rebate, err := CreateReferralRebateInTx(tx, order)
		if err != nil {
			return err
		}

		settledAudit = &AccountAuditEvent{
			UserID: user.ID, Username: user.Username, Module: "recharge", Action: 41,
			Description: "充值到账", CorrelationID: order.OrderNo,
			Request: map[string]any{"orderNo": order.OrderNo, "amount": order.Amount, "paymentType": values.Get("type")},
			Result: map[string]any{
				"status": "success", "message": "充值已到账", "orderNo": order.OrderNo,
				"amount": order.Amount, "provider": order.PaymentProvider,
				"paymentType": values.Get("type"), "billingId": billingRecord.ID,
				"referralRebateId":    referralRebateAuditID(rebate),
				"promotionGrantId":    rechargePromotionAuditID(promotion),
				"promotionGiftAmount": rechargePromotionAuditGiftAmount(promotion),
			},
		}
		return nil
	})
	if err != nil {
		return false, err
	}

	// Financial state is committed before this best-effort audit. A repeated paid
	// callback creates no event and never credits or attempts the same audit twice.
	if settledAudit != nil {
		if auditErr := WriteAccountAuditWithDB(db, *settledAudit); auditErr != nil {
			logger.SysWarn(fmt.Sprintf("充值到账审计写入失败 orderNo=%s: %v", orderNo, auditErr))
		}
	}
	return acknowledged, nil
}

// referralRebateAuditID 把可能为 nil 的返佣记录转成审计里的 ID（0 = 本次未产生返佣）。
//
// 只记 ID，不记比例/金额：返佣金额在结算时才进受益人赠送余额，充值到账审计
// 保持「本次充值让钱包增加了多少」的口径。
func referralRebateAuditID(rebate *entity.ReferralRebate) int32 {
	if rebate == nil {
		return 0
	}
	return rebate.ID
}

// rechargePromotionAuditID 把可能为 nil 的赠送记录转成审计里的 ID（0 = 本次未赠送）。
func rechargePromotionAuditID(grant *entity.RechargePromotionGrant) int32 {
	if grant == nil {
		return 0
	}
	return grant.ID
}

// rechargePromotionAuditGiftAmount 是审计里记录的赠金金额（0 = 本次未赠送）。
//
// 只记赠金本身，不记到账总额：充值到账审计的 `amount` 字段保持「实付进了钱包多少」
// 的口径，赠金单独一个字段，避免把「送的钱」混进实付统计。
func rechargePromotionAuditGiftAmount(grant *entity.RechargePromotionGrant) float64 {
	if grant == nil {
		return 0
	}
	return grant.GiftAmount
}

func writeRechargeCreateAudit(db *gorm.DB, user entity.User, input CreateRechargeOrderInput, order entity.RechargeOrder, paymentType, status, code, message string) error {
	description := "充值下单失败"
	if status == "success" {
		description = "充值下单成功"
	} else if status == "pending" {
		description = "充值下单结果待确认"
	}
	return WriteAccountAuditWithDB(db, AccountAuditEvent{
		UserID: user.ID, Username: user.Username, Module: "recharge", Action: 40,
		Description: description, ClientIP: input.ClientIP, CorrelationID: order.OrderNo,
		Request: map[string]any{"amount": input.Amount, "paymentType": paymentType},
		Result: map[string]any{
			"status": status, "message": message, "code": code, "orderNo": order.OrderNo,
			"amount": input.Amount, "paymentType": paymentType, "provider": order.PaymentProvider,
		},
	})
}

func writeRechargeCreateFailureAudit(db *gorm.DB, user entity.User, input CreateRechargeOrderInput, order entity.RechargeOrder, paymentType string, err error) {
	code, message := "ORDER_CREATE_FAILED", "创建充值订单失败"
	switch {
	case errors.Is(err, ErrRechargeAmountInvalid), errors.Is(err, ErrRechargeAmountPrecision), errors.Is(err, ErrRechargeAmountBelowMinimum):
		code, message = "INVALID_AMOUNT", "充值金额不符合要求"
	case errors.Is(err, ErrRechargePaymentConfigIncomplete):
		code, message = "PAYMENT_CONFIG_INCOMPLETE", "支付配置不完整"
	case errors.Is(err, ErrRechargePaymentMethodDisabled):
		code, message = "PAYMENT_METHOD_DISABLED", "支付方式未启用"
	case errors.Is(err, ErrRechargePaymentMethodNotFound):
		code, message = "PAYMENT_METHOD_NOT_FOUND", "支付方式不存在"
	}
	_ = writeRechargeCreateAudit(db, user, input, order, paymentType, "failed", code, message)
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
