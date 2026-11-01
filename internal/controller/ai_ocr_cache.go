package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
)

func AIOCRCacheStatus(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	status, err := service.GetAIOCRCacheStatusWithDB(withDatabasePriority(manager.GetManager().GetDB(), databaseRequestContext(c), dbqueue.PriorityLow))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取图片 OCR 缓存状态失败"})
		return
	}
	c.JSON(http.StatusOK, status)
}

func ClearAIOCRCache(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	deleted, err := service.ClearAIOCRCacheWithDB(withDatabasePriority(manager.GetManager().GetDB(), databaseRequestContext(c), dbqueue.PriorityLow))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "清空图片 OCR 缓存失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}
