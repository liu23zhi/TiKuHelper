package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"strconv"
	"strings"
)

func ReferralSettingsGet(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	db := referralDB()
	if db == nil {
		c.JSON(503, gin.H{"message": "数据库暂不可用"})
		return
	}
	c.JSON(200, service.GetReferralSettingsWithDB(db))
}
func ReferralSettingsPut(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var payload struct {
		Enabled *bool    `json:"enabled"`
		Rate    *float64 `json:"rate"`
	}
	if c.ShouldBindJSON(&payload) != nil || payload.Enabled == nil || payload.Rate == nil {
		c.JSON(400, gin.H{"message": "配置格式无效"})
		return
	}
	input := service.ReferralSettings{Enabled: *payload.Enabled, Rate: *payload.Rate}
	db := referralDB()
	if db == nil {
		c.JSON(503, gin.H{"message": "数据库暂不可用"})
		return
	}
	if err := service.SaveReferralSettingsWithDB(db, input); err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	admin, _ := currentOperator(c)
	recordAccountAudit(c, &admin, "referral", 1, "管理员更新邀请返佣配置", map[string]any{"enabled": input.Enabled, "rate": input.Rate})
	c.JSON(200, service.GetReferralSettingsWithDB(db))
}
func ReferralInviteAdminList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	page, _ := strconv.Atoi(c.Query("page"))
	size, _ := strconv.Atoi(c.Query("pageSize"))
	db := referralDB()
	if db == nil {
		c.JSON(503, gin.H{"message": "数据库暂不可用"})
		return
	}
	result, err := service.ListReferralInvitesWithDB(db, strings.TrimSpace(c.Query("keyword")), page, size)
	if err != nil {
		c.JSON(500, gin.H{"message": "读取邀请码失败"})
		return
	}
	c.JSON(200, result)
}
