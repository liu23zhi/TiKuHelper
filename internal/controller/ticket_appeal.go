package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/service"
)

const ticketAppealVerificationPath = "/api/tickets/appeal-verification"

func VerifyTicketAppealAccount(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	actor, ok := currentOperator(c)
	if !ok {
		return
	}
	if !isUserEnabled(&actor) {
		c.JSON(http.StatusForbidden, gin.H{"message": "当前账号无法核验其他账号"})
		return
	}
	result, err := service.VerifyTicketAppealAccountWithDB(ticketControllerDB().WithContext(c.Request.Context()), actor, input.Username, input.Password, c.ClientIP())
	switch {
	case err == nil:
		c.JSON(http.StatusOK, gin.H{"data": result})
	case errors.Is(err, service.ErrTicketAppealCredentials):
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
	case errors.Is(err, service.ErrTicketAppealRateLimited):
		c.JSON(http.StatusTooManyRequests, gin.H{"message": err.Error()})
	case errors.Is(err, service.ErrTicketForbidden):
		c.JSON(http.StatusForbidden, gin.H{"message": "请重新登录后验证账号"})
	default:
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "账号验证暂不可用，请稍后重试"})
	}
}
