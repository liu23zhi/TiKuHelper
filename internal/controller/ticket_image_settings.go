package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"net/http"
)

func TicketImageRetentionSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db := ticketImageCacheDB(c)
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "数据库未初始化"})
		return
	}
	if c.Request.Method == http.MethodGet {
		c.JSON(http.StatusOK, service.GetTicketAttachmentRetentionSettingsWithDB(db))
		return
	}
	var input service.TicketAttachmentRetentionSettings
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "清理策略参数无效"})
		return
	}
	settings, err := service.UpdateTicketImageRetentionSettingsWithDB(db, input)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	operator, _ := currentOperator(c)
	recordAccountAudit(c, &operator, "ticket", 2, "更新工单图片缓存策略", map[string]any{"mode": settings.Mode})
	c.JSON(http.StatusOK, settings)
}
