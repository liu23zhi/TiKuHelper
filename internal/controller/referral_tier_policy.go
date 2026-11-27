package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

func ReferralAutoRulesGet(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	db := referralDB()
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "数据库暂不可用"})
		return
	}
	policy, err := service.GetReferralAutoRulesWithDB(db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取自动返佣规则失败"})
		return
	}
	c.JSON(http.StatusOK, policy)
}

func ReferralAutoRulesPut(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	var input struct {
		Enabled *bool                       `json:"enabled"`
		Rules   *[]service.ReferralAutoRule `json:"rules"`
	}
	if c.ShouldBindJSON(&input) != nil || input.Enabled == nil || input.Rules == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "自动返佣规则格式无效"})
		return
	}
	db := referralDB()
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "数据库暂不可用"})
		return
	}
	operator, _ := currentOperator(c)
	policy := service.ReferralAutoRules{Enabled: *input.Enabled, Rules: *input.Rules}
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := service.SaveReferralAutoRulesWithDB(tx, policy); err != nil {
			return err
		}
		return service.WriteAccountAuditWithDB(tx, service.AccountAuditEvent{
			UserID: operator.ID, Username: operator.Username, Module: "referral", Action: 1,
			Description: "管理员更新自动返佣规则", ClientIP: c.ClientIP(),
			Result: map[string]any{"enabled": policy.Enabled, "scope": "automatic_referral_rules"},
		})
	})
	if errors.Is(err, service.ErrReferralAutoRulesInvalid) {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存自动返佣规则失败"})
		return
	}
	c.JSON(http.StatusOK, policy)
}
