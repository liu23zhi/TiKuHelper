package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
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
	if !requireAdmin(c) {
		return
	}
	detail, err := loadSystemSettingDetail()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "获取系统设置失败",
		})
		return
	}

	c.JSON(http.StatusOK, service.RedactSystemSettingDetail(detail))
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
	if !requireAdmin(c) {
		return
	}
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
	if err := logger.SetMaxTotalLogBytes(detail.MaxLogSizeBytes); err != nil {
		logger.SysWarn("应用新的日志大小设置失败：" + err.Error())
	}
	// UAPI 密钥控制 AI 联网搜索能力；设置变化后不能继续复用旧的无搜索答案。
	invalidateSearchResultCaches("update_system_settings")

	c.JSON(http.StatusOK, service.RedactSystemSettingDetail(detail))
}
