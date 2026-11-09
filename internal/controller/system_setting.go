package controller

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
)

var loadSystemSettingDetail = func() (service.SystemSettingDetail, error) {
	return service.GetSystemSettingDetailWithDB(withDatabasePriority(manager.GetManager().GetDB(), context.Background(), dbqueue.PriorityLow))
}

var loadPublicAuthSettings = func() (service.PublicAuthSettings, error) {
	return service.GetPublicAuthSettingsWithDB(withDatabasePriority(manager.GetManager().GetDB(), context.Background(), dbqueue.PriorityLow))
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
		status := http.StatusInternalServerError
		message := "更新系统设置失败"
		if errors.Is(err, service.ErrInvalidMinRechargeAmount) || errors.Is(err, service.ErrInvalidAISearchCount) {
			status = http.StatusBadRequest
			message = err.Error()
		}
		c.JSON(status, gin.H{"message": message})
		return
	}
	if err := logger.SetMaxTotalLogBytes(detail.MaxLogSizeBytes); err != nil {
		logger.SysWarn("应用新的日志大小设置失败：" + err.Error())
	}
	// UAPI 密钥控制 AI 联网搜索能力；设置变化后不能继续复用旧的无搜索答案。
	invalidateSearchResultCaches("update_system_settings")
	// 黑名单开关/规则改动必须立即生效（46-*.md §4「管理员增删改 → 立即失效并重载」）。
	// 刷新失败不影响保存结果：旧快照仍然生效，不会出现「规则整批失效」的窗口。
	if mgr := manager.GetManager(); mgr != nil {
		if err := service.ReloadDefaultBlacklistCache(mgr.GetDB()); err != nil {
			logger.SysWarn("重载黑名单缓存失败（沿用旧快照，规则未失效）：" + err.Error())
		}
	}

	c.JSON(http.StatusOK, service.RedactSystemSettingDetail(detail))
}
