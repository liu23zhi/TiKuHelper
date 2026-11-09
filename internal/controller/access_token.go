package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
)

var listAccessTokens = func(user entity.User, includeAll bool) ([]service.AccessTokenListItem, error) {
	if includeAll {
		return service.ListAllAccessTokensWithDB(withDatabasePriority(manager.GetManager().GetDB(), context.Background(), dbqueue.PriorityLow), user)
	}
	return service.ListAccessTokensWithDB(withDatabasePriority(manager.GetManager().GetDB(), context.Background(), dbqueue.PriorityLow), user)
}

var createAccessToken = func(user entity.User, input service.CreateAccessTokenInput) (service.CreatedAccessToken, error) {
	return service.CreateAccessTokenWithDB(manager.GetManager().GetDB(), user, input)
}

var updateAccessToken = func(user entity.User, tokenID int32, input service.UpdateAccessTokenInput) (entity.AccessToken, error) {
	return service.UpdateAccessTokenWithDB(manager.GetManager().GetDB(), user, tokenID, input)
}

var deleteAccessToken = func(user entity.User, tokenID int32, clientIP string) error {
	return service.DeleteAccessTokenWithAuditWithDB(manager.GetManager().GetDB(), user, tokenID, clientIP)
}

// resetAccessTokenQuotaWindow 是重置额度周期的取库点（与其它 CRUD 同构，便于测试替换）。
var resetAccessTokenQuotaWindow = func(user entity.User, tokenID int32, clientIP string) (service.ResetAccessTokenQuotaWindowResult, error) {
	return service.ResetAccessTokenQuotaWindowWithDB(manager.GetManager().GetDB(), user, tokenID, clientIP)
}

func AccessTokenList(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}

	includeAll := strings.EqualFold(strings.TrimSpace(c.Query("scope")), "all") && isAdminUser(&operator)
	tokens, err := listAccessTokens(operator, includeAll)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": "获取访问令牌失败",
		})
		return
	}

	c.JSON(http.StatusOK, tokens)
}

func CreateAccessToken(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}

	var input service.CreateAccessTokenInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "参数错误",
		})
		return
	}

	input.ClientIP = strings.TrimSpace(c.ClientIP())
	created, err := createAccessToken(operator, input)
	if err != nil {
		writeAccessTokenError(c, err, "创建访问令牌失败")
		return
	}

	c.JSON(http.StatusOK, created)
}

func UpdateAccessToken(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}

	tokenID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "参数错误",
		})
		return
	}

	var input service.UpdateAccessTokenInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "参数错误",
		})
		return
	}

	input.ClientIP = strings.TrimSpace(c.ClientIP())
	updated, err := updateAccessToken(operator, int32(tokenID), input)
	if err != nil {
		writeAccessTokenError(c, err, "更新访问令牌失败")
		return
	}

	c.JSON(http.StatusOK, updated)
}

func DeleteAccessToken(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}

	tokenID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "参数错误",
		})
		return
	}

	if err := deleteAccessToken(operator, int32(tokenID), strings.TrimSpace(c.ClientIP())); err != nil {
		writeAccessTokenError(c, err, "删除访问令牌失败")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "ok",
	})
}

// ResetAccessTokenQuotaWindow 重置访问令牌的额度周期（P14）。
//
// 语义：本轮已用 = total_amount_used - baseline_amount。
// 重置只追加一条 epoch 行，**不清零 total_amount_used**（历史累计保留），
// 也不动钱包（users.balance 与 billing_records 全都不碰）。
func ResetAccessTokenQuotaWindow(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}

	tokenID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "参数错误",
		})
		return
	}

	result, err := resetAccessTokenQuotaWindow(operator, int32(tokenID), strings.TrimSpace(c.ClientIP()))
	if err != nil {
		writeAccessTokenError(c, err, "重置额度周期失败")
		return
	}

	c.JSON(http.StatusOK, result)
}

func currentOperator(c *gin.Context) (entity.User, bool) {
	value, exists := c.Get("user")
	if !exists || value == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message": "请重新登录",
		})
		return entity.User{}, false
	}

	user, ok := value.(*entity.User)
	if !ok || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"message": "请重新登录",
		})
		return entity.User{}, false
	}

	return *user, true
}

func writeAccessTokenError(c *gin.Context, err error, fallback string) {
	status := http.StatusInternalServerError
	message := fallback

	switch {
	case errors.Is(err, service.ErrAccessTokenNameRequired):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrAccessTokenNameInvalid):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrAccessTokenQuotaLimitInvalid):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrAccessTokenNotFound):
		status = http.StatusNotFound
		message = err.Error()
	case errors.Is(err, service.ErrAccessTokenForbidden):
		status = http.StatusForbidden
		message = err.Error()
	case errors.Is(err, service.ErrAccessTokenQuotaEpochAdminRequired):
		status = http.StatusForbidden
		message = err.Error()
	case errors.Is(err, service.ErrAccessTokenQuotaEpochConflict):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, service.ErrAccessTokenQuotaWindowEmpty):
		status = http.StatusConflict
		message = err.Error()
	}

	c.JSON(status, gin.H{
		"message": message,
	})
}
