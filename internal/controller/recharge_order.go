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
