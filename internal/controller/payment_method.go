package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
)

var listPaymentMethods = func(user entity.User) ([]entity.PaymentMethod, error) {
	return service.ListPaymentMethodsWithDB(withDatabasePriority(manager.GetManager().GetDB(), context.Background(), dbqueue.PriorityLow), isAdminUser(&user))
}

var createPaymentMethod = func(user entity.User, input service.CreatePaymentMethodInput) (entity.PaymentMethod, error) {
	return service.CreatePaymentMethodWithDB(manager.GetManager().GetDB(), input)
}

var updatePaymentMethod = func(user entity.User, paymentMethodID int32, input service.UpdatePaymentMethodInput) (entity.PaymentMethod, error) {
	return service.UpdatePaymentMethodWithDB(manager.GetManager().GetDB(), paymentMethodID, input)
}

var deletePaymentMethod = func(user entity.User, paymentMethodID int32) error {
	return service.DeletePaymentMethodWithDB(manager.GetManager().GetDB(), paymentMethodID)
}

func PaymentMethodList(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}

	methods, err := listPaymentMethods(operator)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取支付方式失败"})
		return
	}

	c.JSON(http.StatusOK, methods)
}

func CreatePaymentMethod(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !isAdminUser(&operator) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限操作支付方式"})
		return
	}

	var input service.CreatePaymentMethodInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	created, err := createPaymentMethod(operator, input)
	if err != nil {
		writePaymentMethodError(c, err, "创建支付方式失败")
		return
	}

	c.JSON(http.StatusOK, created)
}

func UpdatePaymentMethod(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !isAdminUser(&operator) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限操作支付方式"})
		return
	}

	paymentMethodID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	var input service.UpdatePaymentMethodInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	updated, err := updatePaymentMethod(operator, int32(paymentMethodID), input)
	if err != nil {
		writePaymentMethodError(c, err, "更新支付方式失败")
		return
	}

	c.JSON(http.StatusOK, updated)
}

func DeletePaymentMethod(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !isAdminUser(&operator) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限操作支付方式"})
		return
	}

	paymentMethodID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	if err := deletePaymentMethod(operator, int32(paymentMethodID)); err != nil {
		writePaymentMethodError(c, err, "删除支付方式失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

func writePaymentMethodError(c *gin.Context, err error, fallback string) {
	status := http.StatusInternalServerError
	message := fallback

	switch {
	case errors.Is(err, service.ErrPaymentMethodNameRequired), errors.Is(err, service.ErrPaymentMethodEPayTypeRequired), errors.Is(err, service.ErrPaymentMethodIconUnsafe):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrPaymentMethodNotFound):
		status = http.StatusNotFound
		message = err.Error()
	}

	c.JSON(status, gin.H{"message": message})
}
