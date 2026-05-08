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

var listAIModelArchitectures = func(user entity.User) ([]entity.AIModelArchitecture, error) {
	return service.ListAIModelArchitecturesWithDB(manager.GetManager().GetDB(), isAdminUser(&user))
}

var createAIModelArchitecture = func(user entity.User, input service.CreateAIModelArchitectureInput) (entity.AIModelArchitecture, error) {
	return service.CreateAIModelArchitectureWithDB(manager.GetManager().GetDB(), input)
}

var updateAIModelArchitecture = func(user entity.User, architectureID int32, input service.UpdateAIModelArchitectureInput) (entity.AIModelArchitecture, error) {
	return service.UpdateAIModelArchitectureWithDB(manager.GetManager().GetDB(), architectureID, input)
}

var deleteAIModelArchitecture = func(user entity.User, architectureID int32) error {
	return service.DeleteAIModelArchitectureWithDB(manager.GetManager().GetDB(), architectureID)
}

func AIModelArchitectureList(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}

	items, err := listAIModelArchitectures(operator)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取模型架构失败"})
		return
	}

	c.JSON(http.StatusOK, items)
}

func CreateAIModelArchitecture(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !isAdminUser(&operator) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限操作模型架构"})
		return
	}

	var input service.CreateAIModelArchitectureInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	created, err := createAIModelArchitecture(operator, input)
	if err != nil {
		writeAIModelPricingError(c, err, "创建模型架构失败")
		return
	}

	c.JSON(http.StatusOK, created)
}

func UpdateAIModelArchitecture(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !isAdminUser(&operator) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限操作模型架构"})
		return
	}

	architectureID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	var input service.UpdateAIModelArchitectureInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	updated, err := updateAIModelArchitecture(operator, int32(architectureID), input)
	if err != nil {
		writeAIModelPricingError(c, err, "更新模型架构失败")
		return
	}

	c.JSON(http.StatusOK, updated)
}

func DeleteAIModelArchitecture(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !isAdminUser(&operator) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限操作模型架构"})
		return
	}

	architectureID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	if err := deleteAIModelArchitecture(operator, int32(architectureID)); err != nil {
		writeAIModelPricingError(c, err, "删除模型架构失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

func writeAIModelPricingError(c *gin.Context, err error, fallback string) {
	status := http.StatusInternalServerError
	message := fallback

	switch {
	case errors.Is(err, service.ErrAIModelArchitectureNameRequired):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrAIModelArchitectureNotFound):
		status = http.StatusNotFound
		message = err.Error()
	}

	c.JSON(status, gin.H{"message": message})
}
