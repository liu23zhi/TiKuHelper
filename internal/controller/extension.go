package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

type extensionPackageStore interface {
	Latest(string) (service.ExtensionRelease, error)
	Download(string, string) (service.ExtensionRelease, []byte, error)
	Refresh(context.Context, string) error
}

var extensionStore = func() (extensionPackageStore, error) {
	return service.DefaultExtensionStore()
}

var extensionSettingsStore = service.DefaultExtensionStore
var extensionSettingsDB = func() *gorm.DB { return manager.GetManager().GetDB() }

func ExtensionConfiguration(c *gin.Context) {
	store, err := extensionSettingsStore()
	if err != nil {
		writeExtensionError(c, err)
		return
	}
	c.JSON(http.StatusOK, store.Configuration())
}

func UpdateExtensionConfiguration(c *gin.Context) {
	var input struct {
		AutoUpdate      *bool   `json:"autoUpdate"`
		IntervalMinutes *int    `json:"intervalMinutes"`
		EdgeID          *string `json:"edgeId"`
		ChromeID        *string `json:"chromeId"`
	}
	if c.ShouldBindJSON(&input) != nil || input.AutoUpdate == nil || input.IntervalMinutes == nil || input.EdgeID == nil || input.ChromeID == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请提供完整有效的扩展更新设置"})
		return
	}
	store, err := extensionSettingsStore()
	if err != nil {
		writeExtensionError(c, err)
		return
	}
	configuration, err := store.UpdateSettingsWithDB(extensionSettingsDB(), service.ExtensionUpdateSettings{
		AutoUpdate: *input.AutoUpdate, IntervalMinutes: *input.IntervalMinutes, EdgeID: *input.EdgeID, ChromeID: *input.ChromeID,
	})
	if errors.Is(err, service.ErrInvalidExtensionSettings) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "检查间隔应至少为 1 分钟且不超过系统支持范围，扩展 ID 应为 32 位 a-p 字母或留空停用"})
		return
	}
	if err != nil {
		writeExtensionError(c, err)
		return
	}
	c.JSON(http.StatusOK, configuration)
}

func ExtensionLatest(c *gin.Context) {
	store, err := extensionStore()
	if err != nil {
		logger.SysError("扩展更新服务配置失败：" + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"message": "扩展更新服务不可用"})
		return
	}
	release, err := store.Latest(normalizeExtensionSource(c.Param("source")))
	if err != nil {
		writeExtensionError(c, err)
		return
	}
	c.JSON(http.StatusOK, release)
}

func ExtensionDownload(c *gin.Context) {
	store, err := extensionStore()
	if err != nil {
		logger.SysError("扩展更新服务配置失败：" + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"message": "扩展更新服务不可用"})
		return
	}
	source := normalizeExtensionSource(c.Param("source"))
	release, data, err := store.Download(source, strings.ToLower(strings.TrimSpace(c.Query("sha256"))))
	if err != nil {
		writeExtensionError(c, err)
		return
	}
	c.Header("Cache-Control", "public, max-age=300")
	c.Header("ETag", `"`+release.SHA256+`"`)
	c.Header("X-Extension-Id", release.ExtensionID)
	c.Header("X-Extension-Version", release.Version)
	c.Data(http.StatusOK, "application/x-chrome-extension", data)
}

func RefreshExtension(c *gin.Context) {
	store, err := extensionStore()
	if err != nil {
		logger.SysError("扩展更新服务配置失败：" + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"message": "扩展更新服务不可用"})
		return
	}
	source := normalizeExtensionSource(c.Param("source"))
	if err := store.Refresh(c.Request.Context(), source); err != nil {
		writeExtensionError(c, err)
		return
	}
	release, err := store.Latest(source)
	if err != nil {
		writeExtensionError(c, err)
		return
	}
	c.JSON(http.StatusOK, release)
}

func normalizeExtensionSource(value string) string {
	source := strings.ToLower(strings.TrimSpace(value))
	if source == "edge" || source == "chrome" {
		return source
	}
	return ""
}

func writeExtensionError(c *gin.Context, err error) {
	status := http.StatusBadGateway
	message := "扩展更新失败，请稍后重试"
	switch {
	case errors.Is(err, service.ErrExtensionSourceNotConfigured):
		status = http.StatusNotFound
		message = "扩展来源尚未配置"
	case errors.Is(err, service.ErrExtensionUnavailable):
		status = http.StatusServiceUnavailable
		message = "尚无已验证的扩展包，请稍后重试"
	case errors.Is(err, service.ErrExtensionChanged):
		status = http.StatusConflict
		message = "扩展版本已变更，请重新查询最新版本"
	default:
		logger.SysError("扩展更新失败：" + err.Error())
	}
	c.JSON(status, gin.H{"message": message})
}
