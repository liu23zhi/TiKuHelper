package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
)

var loadSystemSettingDetail = func() (service.SystemSettingDetail, error) {
	return service.GetSystemSettingDetailWithDB(manager.GetManager().GetDB())
}

var loadPublicAuthSettings = func() (service.PublicAuthSettings, error) {
	return service.GetPublicAuthSettingsWithDB(manager.GetManager().GetDB())
}

var updateSystemSettingDetail = func(input service.UpdateSystemSettingInput) (service.SystemSettingDetail, error) {
	return service.UpdateSystemSettingDetailWithDB(manager.GetManager().GetDB(), input)
}

func SystemSettingDetail(c *gin.Context) {
	detail, err := loadSystemSettingDetail()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "获取系统设置失败",
		})
		return
	}

	c.JSON(http.StatusOK, detail)
}

func PublicRegisterStatus(c *gin.Context) {
	settings, err := loadPublicAuthSettings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "获取注册状态失败",
		})
		return
	}

	c.JSON(http.StatusOK, settings)
}

func UpdateSystemSetting(c *gin.Context) {
	var input service.UpdateSystemSettingInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "参数错误",
		})
		return
	}

	detail, err := updateSystemSettingDetail(input)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "更新系统设置失败",
		})
		return
	}

	c.JSON(http.StatusOK, detail)
}
