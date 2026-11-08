package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

func GetScriptEscrowPolicy(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if _, ok := currentOperator(c); !ok {
		return
	}
	policy, err := service.GetScriptEscrowPolicyWithDB(scriptDB())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "脚本冻结金策略暂不可用"})
		return
	}
	c.JSON(http.StatusOK, policy)
}

func PutScriptEscrowPolicy(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !requireAdmin(c) {
		return
	}
	var policy service.ScriptEscrowPolicy
	if err := c.ShouldBindJSON(&policy); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "脚本冻结金策略参数错误"})
		return
	}
	if err := service.UpdateScriptEscrowPolicyWithDB(scriptDB(), operator.ID, policy); err != nil {
		writeScriptError(c, err, "保存脚本冻结金策略失败")
		return
	}
	c.JSON(http.StatusOK, policy)
}
