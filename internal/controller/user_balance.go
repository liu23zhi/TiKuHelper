package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

func UpdateUserBalance(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "用户参数错误"})
		return
	}
	var payload struct {
		Mode            string   `json:"mode"`
		Amount          *float64 `json:"amount"`
		ShowInStatement *bool    `json:"showInStatement"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || payload.Amount == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请输入余额调整金额"})
		return
	}
	input := service.UserBalanceAdjustmentInput{
		Mode: payload.Mode, Amount: *payload.Amount, ShowInStatement: payload.ShowInStatement, ClientIP: c.ClientIP(),
	}
	var user entity.User
	if key := c.GetHeader("Idempotency-Key"); key != "" {
		user, err = service.AdjustUserBalanceIdempotentWithDB(dao.User.UnderlyingDB(), operator, int32(userID), input, key)
	} else {
		user, err = service.AdjustUserBalanceWithDB(dao.User.UnderlyingDB(), operator, int32(userID), input)
	}
	if err != nil {
		writeBalanceOperationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": user.ID, "balance": user.Balance})
}

func UpdateBillingStatementVisibility(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "账单参数错误"})
		return
	}
	var payload struct {
		ShowInStatement *bool `json:"showInStatement"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || payload.ShowInStatement == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请选择是否在账单流水显示"})
		return
	}
	if err := service.SetBillingStatementVisibilityWithDB(dao.User.UnderlyingDB(), operator, int32(id), *payload.ShowInStatement, c.ClientIP()); err != nil {
		writeBalanceOperationError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "showInStatement": *payload.ShowInStatement})
}

func writeBalanceOperationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrIdempotencyConflict):
		c.JSON(http.StatusConflict, gin.H{"message": err.Error()})
	case errors.Is(err, service.ErrIdempotencyKeyInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
	case errors.Is(err, service.ErrIdempotencyPending):
		c.JSON(http.StatusAccepted, gin.H{"status": "pending", "message": err.Error()})
	case errors.Is(err, service.ErrBalanceAdjustmentForbidden):
		c.JSON(http.StatusForbidden, gin.H{"message": err.Error()})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "用户或账单不存在"})
	case errors.Is(err, service.ErrBalanceChanged):
		c.JSON(http.StatusConflict, gin.H{"message": err.Error()})
	case errors.Is(err, service.ErrInvalidBalanceAdjustment):
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
	default:
		logger.SysError("余额或账单更新失败：" + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "修改失败，未保存任何变更，请稍后重试"})
	}
}
