package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
)

func SearchAccountConfigList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	configs, err := service.ListSearchAccountConfigsWithDB(manager.GetManager().GetDB())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "获取默认账号配置失败",
		})
		return
	}

	c.JSON(http.StatusOK, service.RedactSearchAccountConfigViews(configs))
}

func UpdateSearchAccountConfig(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var input service.UpdateSearchAccountConfigInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "参数错误",
		})
		return
	}

	config, err := service.UpdateSearchAccountConfigWithDB(manager.GetManager().GetDB(), c.Param("provider"), input)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, service.ErrUnsupportedSearchAccountProvider) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, service.RedactSearchAccountConfigView(config))
}
