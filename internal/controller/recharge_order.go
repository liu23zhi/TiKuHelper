package controller

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
)

var createRechargeOrder = func(user entity.User, input service.CreateRechargeOrderInput) (service.CreateRechargeOrderResult, error) {
	return service.CreateRechargeOrderWithDB(manager.GetManager().GetDB(), user, input)
}

var handleRechargePaymentNotify = func(values url.Values) (bool, error) {
	return service.HandleRechargePaymentNotifyWithDB(manager.GetManager().GetDB(), values)
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

	created, err := createRechargeOrder(operator, input)
	if err != nil {
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
	case errors.Is(err, service.ErrRechargeAmountInvalid),
		errors.Is(err, service.ErrRechargePaymentMethodDisabled),
		errors.Is(err, service.ErrRechargePaymentConfigIncomplete):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrRechargePaymentMethodNotFound):
		status = http.StatusNotFound
		message = err.Error()
	}

	c.JSON(status, gin.H{"message": message})
}
