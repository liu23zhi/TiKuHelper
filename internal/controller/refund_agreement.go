package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
)

// 退款承诺书的读写（2026-11-19 用户要求）。
//
//   GET  —— 任何已登录用户可读：签署前必须让用户看到全文。
//   PUT  —— 仅管理员可写：条款措辞是平台对外的责任声明，不能让普通用户改。

// GetRefundAgreement 返回当前的退款承诺书全文。
//
// 响应刻意用 no-store：协议可能在两分钟内被管理员改掉，
// 缓存住会让不同用户读到不同版本，而签署记录上无法体现这一点。
func GetRefundAgreement(c *gin.Context) {
	db := manager.GetManager().GetDB()
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "服务暂不可用"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"text":        service.GetRefundAgreementTextWithDB(db),
		"isDefault":   service.IsDefaultRefundAgreement(db),
		"maxRunes":    service.RefundAgreementMaxRunes,
		"defaultText": service.DefaultRefundAgreementText,
	})
}

// UpdateRefundAgreement 更新退款承诺书全文（管理员）。
func UpdateRefundAgreement(c *gin.Context) {
	db := manager.GetManager().GetDB()
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "服务暂不可用"})
		return
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	if err := service.SetRefundAgreementTextWithDB(db, payload.Text); err != nil {
		if errors.Is(err, service.ErrRefundAgreementInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{
				"message": "协议文本不能为空，且不能超过 20000 字",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存协议文本失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":   "协议已更新",
		"text":      service.GetRefundAgreementTextWithDB(db),
		"isDefault": service.IsDefaultRefundAgreement(db),
	})
}
