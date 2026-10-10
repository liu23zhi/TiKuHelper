package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
)

var listAIModelPrices = func(includeDisabled bool) ([]entity.AIModelPrice, error) {
	return service.ListAIModelPricesWithDB(manager.GetManager().GetDB(), includeDisabled)
}

var createAIModelPrice = func(input service.CreateAIModelPriceInput) (entity.AIModelPrice, error) {
	return service.CreateAIModelPriceWithDB(manager.GetManager().GetDB(), input)
}

var updateAIModelPrice = func(priceID int32, input service.UpdateAIModelPriceInput) (entity.AIModelPrice, error) {
	return service.UpdateAIModelPriceWithDB(manager.GetManager().GetDB(), priceID, input)
}

var deleteAIModelPrice = func(priceID int32) error {
	return service.DeleteAIModelPriceWithDB(manager.GetManager().GetDB(), priceID)
}

var resetAIModelPrices = func() error {
	return service.ResetAIModelPricesWithDB(manager.GetManager().GetDB())
}

// AIModelPriceList 返回模型价格表。管理员可以看到停用项，普通用户只能看到启用项。
func AIModelPriceList(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}

	items, err := listAIModelPrices(isAdminUser(&operator))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取模型价格表失败"})
		return
	}
	if items == nil {
		items = []entity.AIModelPrice{}
	}

	c.JSON(http.StatusOK, items)
}

func CreateAIModelPrice(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}

	var input service.CreateAIModelPriceInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	created, err := createAIModelPrice(input)
	if err != nil {
		writeAIModelPriceError(c, err, "创建模型价格失败")
		return
	}

	c.JSON(http.StatusOK, created)
}

func UpdateAIModelPrice(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}

	priceID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	var input service.UpdateAIModelPriceInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	updated, err := updateAIModelPrice(int32(priceID), input)
	if err != nil {
		writeAIModelPriceError(c, err, "更新模型价格失败")
		return
	}

	c.JSON(http.StatusOK, updated)
}

func DeleteAIModelPrice(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}

	priceID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	if err := deleteAIModelPrice(int32(priceID)); err != nil {
		writeAIModelPriceError(c, err, "删除模型价格失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// ResetAIModelPriceDefaults 恢复内置默认价格表，用于管理员误改后回滚。
func ResetAIModelPriceDefaults(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}

	if err := resetAIModelPrices(); err != nil {
		writeAIModelPriceError(c, err, "恢复默认模型价格失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

func writeAIModelPriceError(c *gin.Context, err error, fallback string) {
	status := http.StatusInternalServerError
	message := fallback

	switch {
	case errors.Is(err, service.ErrAIModelPriceNameRequired):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrAIModelPriceNotFound):
		status = http.StatusNotFound
		message = err.Error()
	}

	c.JSON(status, gin.H{"message": message})
}
