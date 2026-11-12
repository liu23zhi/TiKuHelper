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

var createManualReferralRebate = func(input service.ManualReferralRebateInput) (entity.ReferralRebate, error) {
	db := referralDB()
	if db == nil {
		return entity.ReferralRebate{}, errReferralDBUnavailable
	}
	return service.CreateManualReferralRebateWithDB(db, input)
}

var listGiftRecords = func(filter service.GiftRecordListQuery, page int, pageSize int) (service.GiftRecordListResult, error) {
	db := referralDB()
	if db == nil {
		return service.GiftRecordListResult{}, errReferralDBUnavailable
	}
	return service.ListGiftRecordsWithDB(withDatabasePriority(db, context.Background(), dbqueue.PriorityLow), filter, page, pageSize)
}

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

// manualReferralRebateRequest 是「管理员手动补录返佣」的请求体。
type manualReferralRebateRequest struct {
	// BeneficiaryUserID 是**受益人**（拿钱的一方），不是下单人。
	BeneficiaryUserID int32 `json:"beneficiaryUserId"`
	// Amount 是本笔返佣金额。
	Amount float64 `json:"amount"`
	// Day 是补录归属日（YYYY-MM-DD，空 = 今天），同时参与幂等键。
	Day string `json:"day"`
	// Remark 是附言。
	Remark string `json:"remark"`
}

// CreateReferralRebate 让管理员手动补录一笔返佣，**创建即入账**。
//
// 与「结算」的区别：结算是对已有 pending 行改状态 + 入账；补录是凭空建一行
// 并立刻入账（source=manual）。两者入账口径完全一致（进 gift_balance、
// 欠款先抵），复用同一条 creditReferralGiftInTx。
//
// 重复提交返回 409 而不是静默成功：管理员点两次必须被告知「这笔已经记过」，
// 否则会以为刚记的是另一笔，接着去核对钱包余额、最后发现对不上。
func CreateReferralRebate(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	var body manualReferralRebateRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式错误"})
		return
	}
	rebate, err := createManualReferralRebate(service.ManualReferralRebateInput{
		BeneficiaryUserID: body.BeneficiaryUserID,
		Amount:            body.Amount,
		Day:               strings.TrimSpace(body.Day),
		Remark:            strings.TrimSpace(body.Remark),
		OperatorUsername:  operator.Username,
	})
	if err != nil {
		writeManualReferralRebateError(c, err)
		return
	}
	c.JSON(http.StatusOK, rebate)
}

// writeManualReferralRebateError 把 service 层错误映射成 HTTP 状态。
func writeManualReferralRebateError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "补录返佣失败"
	switch {
	case errors.Is(err, service.ErrReferralRebateManualInvalid):
		// 入参问题（金额/日期）必须回原文，管理员要据此改输入。
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrReferralRebateDuplicate):
		// 幂等命中：不是错误操作，但也不能返回 200 让管理员以为又记了一笔。
		status = http.StatusConflict
		message = err.Error()
	case errors.Is(err, service.ErrReferralRebateDisabled):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrReferralRebateSchemaUnavailable):
		// 结构没就绪时不能假装成功：补录要真的发钱。
		status = http.StatusServiceUnavailable
		message = "邀请返佣补录暂不可用，请稍后再试"
	case errors.Is(err, service.ErrReferralRebateNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		// lockReferralWalletInTx 找不到受益人时返回的是 ErrReferralRebateNotFound
		// （它把 gorm.ErrRecordNotFound 换成了自己的哨兵错误），两个都要匹配，
		// 否则「受益人打错 ID」会变成 500「补录返佣失败」，管理员不知道该改哪个字段。
		status = http.StatusNotFound
		message = "受益人不存在"
	}
	if status >= http.StatusInternalServerError {
		logger.SysError("补录返佣失败: " + logger.RedactSecrets(err.Error()))
	}
	c.JSON(status, gin.H{"message": message})
}

// GiftRecordList 返回「赠送记录」：四类赠送来源的聚合账本（管理员）。
//
// 只读、不改任何钱包。四类的入账规则各不相同（注册赠送/评价返现先抵欠款、
// 邀请返佣直接进赠金、充值赠金按阶梯），但**都会写一条 billing_records**，
// 所以聚合落在账单表上而不是四张源表 UNION（理由见 service/gift_record_aggregate.go）。
func GiftRecordList(c *gin.Context) {
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
	userID, userErr := parseOptionalPositiveInt32(c.Query("userId"))
	if userErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "受益账户筛选格式无效"})
		return
	}
	from, fromErr := parseListFilterTime(c.Query("createdFrom"), false)
	to, toErr := parseListFilterTime(c.Query("createdTo"), true)
	if fromErr != nil || toErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "时间筛选格式无效"})
		return
	}
	// 与其它管理端列表口径一致：显式给出区间时必须 start < end。
	if rangeErr := validateListFilterTimeRange(from, to, "入账时间"); rangeErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": rangeErr.Error()})
		return
	}
	filter := service.GiftRecordListQuery{
		Source:      strings.TrimSpace(c.Query("source")),
		UserID:      userID,
		CreatedFrom: from,
		CreatedTo:   to,
	}
	result, err := listGiftRecords(filter, page, pageSize)
	if err != nil {
		// 来源值非法是入参问题，回 400 并把可选值告诉前端（否则管理员不知道该填什么）。
		if errors.Is(err, service.ErrGiftRecordSourceInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		logger.SysError("读取赠送记录失败: " + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取赠送记录失败"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// GiftRecordSourceList 返回四类赠送来源的标识与中文名。
//
// 前端不该自己维护一份来源文案 —— 两处各写一份必然改一处漏一处。
func GiftRecordSourceList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": service.GiftRecordSourceOptions()})
}
