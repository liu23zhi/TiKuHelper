package controller

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/util"
)

var createRechargeOrder = func(user entity.User, input service.CreateRechargeOrderInput) (service.CreateRechargeOrderResult, error) {
	return service.CreateRechargeOrderWithDB(manager.GetManager().GetDB(), user, input)
}

var listRechargeOrders = func(user entity.User, query service.RechargeOrderListQuery, page int, pageSize int) (service.RechargeOrderListResult, error) {
	return service.ListRechargeOrdersWithDB(withDatabasePriority(manager.GetManager().GetDB(), context.Background(), dbqueue.PriorityLow), user, query, page, pageSize)
}

var handleRechargePaymentNotify = func(values url.Values) (bool, error) {
	return service.HandleRechargePaymentNotifyWithDB(manager.GetManager().GetDB(), values)
}

func RechargeOrderList(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}

	filter := service.RechargeOrderListQuery{
		Keyword:         util.NormalizeSearchKeyword(c.Query("keyword")),
		Status:          strings.TrimSpace(c.Query("status")),
		PaymentProvider: strings.TrimSpace(c.Query("paymentProvider")),
	}
	createdFrom, createdFromErr := parseListFilterTime(c.Query("createdFrom"), false)
	createdTo, createdToErr := parseListFilterTime(c.Query("createdTo"), true)
	paidFrom, paidFromErr := parseListFilterTime(c.Query("paidFrom"), false)
	paidTo, paidToErr := parseListFilterTime(c.Query("paidTo"), true)
	userID, userIDErr := parseOptionalPositiveInt32(c.Query("userId"))
	amountMin, amountMinErr := parseOptionalNonNegativeFloat(c.Query("amountMin"))
	amountMax, amountMaxErr := parseOptionalNonNegativeFloat(c.Query("amountMax"))
	if createdFromErr != nil || createdToErr != nil || paidFromErr != nil || paidToErr != nil || userIDErr != nil || amountMinErr != nil || amountMaxErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数格式无效"})
		return
	}
	if err := validateListFilterTimeRange(createdFrom, createdTo, "创建时间"); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if err := validateListFilterTimeRange(paidFrom, paidTo, "支付时间"); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if amountMin != nil && amountMax != nil && *amountMin > *amountMax {
		c.JSON(http.StatusBadRequest, gin.H{"message": "金额范围无效"})
		return
	}
	filter.CreatedFrom, filter.CreatedTo = createdFrom, createdTo
	filter.PaidFrom, filter.PaidTo = paidFrom, paidTo
	filter.UserID, filter.AmountMin, filter.AmountMax = userID, amountMin, amountMax

	result, err := listRechargeOrders(operator, filter, page, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "获取充值记录失败",
		})
		return
	}

	c.JSON(http.StatusOK, result)
}

func CreateRechargeOrder(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}

	var input service.CreateRechargeOrderInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	input.ClientIP = c.ClientIP()
	input.Device = detectRechargeDevice(c.GetHeader("User-Agent"))

	var created service.CreateRechargeOrderResult
	var err error
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		created, err = service.CreateRechargeOrderIdempotentWithDB(manager.GetManager().GetDB(), operator, input, key)
	} else {
		created, err = createRechargeOrder(operator, input)
	}
	if err != nil {
		if errors.Is(err, service.ErrIdempotencyPending) {
			c.JSON(http.StatusAccepted, gin.H{"status": "pending", "orderNo": created.Order.OrderNo, "orderStatus": created.Order.Status, "message": "支付网关结果待核对，请勿重复下单"})
			return
		}
		writeRechargeOrderError(c, err, "创建充值订单失败")
		return
	}

	c.JSON(http.StatusOK, created)
}

func RechargePaymentNotify(c *gin.Context) {
	if err := c.Request.ParseForm(); err != nil {
		c.String(http.StatusOK, "fail")
		return
	}

	acknowledged, err := handleRechargePaymentNotify(c.Request.Form)
	if err != nil || !acknowledged {
		c.String(http.StatusOK, "fail")
		return
	}

	c.String(http.StatusOK, "success")
}

func writeRechargeOrderError(c *gin.Context, err error, fallback string) {
	status := http.StatusInternalServerError
	message := fallback

	switch {
	case errors.Is(err, service.ErrIdempotencyPending):
		c.JSON(http.StatusAccepted, gin.H{"status": "pending", "message": "支付网关结果待核对，请勿重复下单"})
		return
	case errors.Is(err, service.ErrIdempotencyConflict):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, service.ErrIdempotencyKeyInvalid):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrRechargeAmountInvalid),
		errors.Is(err, service.ErrRechargeAmountPrecision),
		errors.Is(err, service.ErrRechargeAmountBelowMinimum),
		errors.Is(err, service.ErrRechargePaymentMethodDisabled),
		errors.Is(err, service.ErrRechargePaymentConfigIncomplete),
		errors.Is(err, service.ErrRechargePaymentCreateFailed):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrRechargePaymentMethodNotFound):
		status = http.StatusNotFound
		message = err.Error()
	}

	c.JSON(status, gin.H{"message": message})
}

func detectRechargeDevice(userAgent string) string {
	ua := strings.ToLower(strings.TrimSpace(userAgent))
	switch {
	case strings.Contains(ua, "micromessenger"):
		return "wechat"
	case strings.Contains(ua, "alipayclient"):
		return "alipay"
	case strings.Contains(ua, "qq/") || strings.Contains(ua, "mqqbrowser"):
		return "qq"
	case strings.Contains(ua, "aweme") || strings.Contains(ua, "douyin"):
		return "douyin"
	case strings.Contains(ua, "mobile") || strings.Contains(ua, "android") || strings.Contains(ua, "iphone") || strings.Contains(ua, "ipad"):
		return "mobile"
	default:
		return "pc"
	}
}

// updateRechargeOrderStatus 是可替换的服务入口（测试里换成假实现）。
var updateRechargeOrderStatus = func(actor entity.User, orderID int32, status string) (entity.RechargeOrder, error) {
	return service.UpdateRechargeOrderStatusWithDB(manager.GetManager().GetDB(), actor, orderID, status)
}

// UpdateRechargeOrderStatus 由管理员手工修正充值订单状态（2026-11-19）。
//
// 仅管理员；旧输入 paid 在待支付订单上只记录人工核对，不入账、不开放退款。
func UpdateRechargeOrderStatus(c *gin.Context) {
	actor, ok := currentOperator(c)
	if !ok {
		return
	}
	orderID, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 32)
	if err != nil || orderID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "充值订单 ID 无效"})
		return
	}
	var payload struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}

	row, err := updateRechargeOrderStatus(actor, int32(orderID), payload.Status)
	if err != nil {
		// 每种失败给独立文案：前端要据此提示，混成一句「操作失败」会让
		// 管理员不知道是权限问题还是状态本来就对。
		switch {
		case errors.Is(err, service.ErrRechargeOrderStatusUnchanged):
			// 幂等成功：目标状态与当前一致。返回 200 而不是错误 ——
			// 管理员重复点一次不该看到红字。
			c.JSON(http.StatusOK, gin.H{"message": "状态未变化", "unchanged": true})
			return
		case errors.Is(err, service.ErrRechargeSettlementReviewRequired):
			c.JSON(http.StatusConflict, gin.H{"message": err.Error()})
			return
		case errors.Is(err, service.ErrRechargeOrderStatusForbidden):
			c.JSON(http.StatusForbidden, gin.H{"message": "无权修改该充值订单"})
			return
		case errors.Is(err, service.ErrRechargeOrderStatusInvalid):
			c.JSON(http.StatusBadRequest, gin.H{"message": "状态值无效或该迁移不被允许"})
			return
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"message": "修改充值订单状态失败"})
			return
		}
	}
	row.CallbackPayload = "" // 状态修正响应不回传支付通知及签名。
	c.JSON(http.StatusOK, gin.H{"message": "运营标记已更新，不会入账或执行退款", "data": row})
}
