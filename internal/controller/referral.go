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
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// referralDB 返回当前数据库句柄；未初始化时返回 nil（调用方负责降级）。
//
// 与 ticket_refund.go 的 ticketRefundDB 同构：单测里没有 manager 时不能 panic。
var referralDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

// 便于测试替换（与 recharge_order.go 的注入风格一致）。
var loadReferralSummary = func(userID int32) (service.ReferralSummary, error) {
	db := referralDB()
	if db == nil {
		return service.ReferralSummary{}, errReferralDBUnavailable
	}
	return service.GetReferralSummaryWithDB(withDatabasePriority(db, context.Background(), dbqueue.PriorityLow), userID)
}

var listReferralRebates = func(filter service.ReferralRebateListQuery, page int, pageSize int) (service.ReferralRebateListResult, error) {
	db := referralDB()
	if db == nil {
		return service.ReferralRebateListResult{}, errReferralDBUnavailable
	}
	return service.ListReferralRebatesWithDB(withDatabasePriority(db, context.Background(), dbqueue.PriorityLow), filter, page, pageSize)
}

var settleReferralRebate = func(admin entity.User, id int32) (entity.ReferralRebate, error) {
	db := referralDB()
	if db == nil {
		return entity.ReferralRebate{}, errReferralDBUnavailable
	}
	return service.SettleReferralRebateWithDB(db, admin, id)
}

var errReferralDBUnavailable = errors.New("数据库未初始化")

// ReferralSummary 返回「我的邀请」：邀请码、已邀请人数、返佣汇总。
//
// 权限：Auth（本人）。不接受 userId 参数 —— 永远只查当前登录账户，
// 避免出现「查别人邀请了多少人」的越权面。
func ReferralSummary(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	summary, err := loadReferralSummary(operator.ID)
	if err != nil {
		logger.SysError("读取邀请返佣汇总失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取邀请信息失败"})
		return
	}
	c.JSON(http.StatusOK, summary)
}

// ReferralRebateList 返回返佣列表（管理员，分页 + 状态/邀请人筛选）。
func ReferralRebateList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	inviterUserID, inviterErr := parseOptionalPositiveInt32(c.Query("inviterUserId"))
	invitedUserID, invitedErr := parseOptionalPositiveInt32(c.Query("invitedUserId"))
	if inviterErr != nil || invitedErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数格式无效"})
		return
	}
	filter := service.ReferralRebateListQuery{
		Status:        strings.TrimSpace(c.Query("status")),
		InviterUserID: inviterUserID,
		InvitedUserID: invitedUserID,
		OrderNo:       strings.TrimSpace(c.Query("orderNo")),
	}
	result, err := listReferralRebates(filter, page, pageSize)
	if err != nil {
		logger.SysError("读取返佣列表失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取返佣记录失败"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// SettleReferralRebate 结算一条 pending 返佣：**改状态 + 入余额，同一个事务**。
//
// 结算会把返佣金额加进邀请人的 users.balance / users.gift_balance 并写一条
// type=referral_rebate 的账单流水（16-*.md §2.5）。重复结算幂等，不会重复加钱；
// 已冲回（reversed）的记录返回 409。
func SettleReferralRebate(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "返佣记录参数错误"})
		return
	}
	rebate, err := settleReferralRebate(operator, int32(id))
	if err != nil {
		writeReferralRebateError(c, err)
		return
	}
	c.JSON(http.StatusOK, rebate)
}

func writeReferralRebateError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "操作返佣记录失败"
	switch {
	case errors.Is(err, service.ErrReferralRebateNotFound):
		status = http.StatusNotFound
		message = err.Error()
	case errors.Is(err, service.ErrReferralRebateState):
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, service.ErrReferralRebateDisabled):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrReferralRebateSchemaUnavailable):
		// 结构没就绪时**不能**假装成功：结算要真的发钱，降级成功会让管理员
		// 以为已经入账，事后对不上账。
		status = http.StatusServiceUnavailable
		message = "邀请返佣结算暂不可用，请稍后再试"
	case errors.Is(err, gorm.ErrRecordNotFound):
		status = http.StatusNotFound
		message = service.ErrReferralRebateNotFound.Error()
	}
	if status >= http.StatusInternalServerError {
		logger.SysError("操作返佣记录失败: " + logger.RedactSecrets(err.Error()))
	}
	c.JSON(status, gin.H{"message": message})
}
