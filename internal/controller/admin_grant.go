package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 本文件是「管理员赠送 / 批量赠送 / 追缴式回滚」的 HTTP 入口。
//
// 九个 handler **全部**先过 requireAdmin：赠送是直接产生钱的入口，任何一个漏了
// 管理员校验都等于给普通用户开了印钞机。服务层另有 isAdminUser + 二次确认兜底，
// 两层都保留 —— controller 的校验负责给出 403/428 的正确响应码，服务层的校验
// 负责挡住「有人从别的入口直接调 service」。
//
// 路由注册由 internal/api/router.go 负责（本任务不改那个文件）。

// adminGrantConfirmHTTPStatus 是「需要二次确认」的响应码。
//
// 用 428 Precondition Required 而不是 400：前端据此区分「填错了」与「请再确认一次」，
// 直接复用 400 会让界面只能靠 message 文案判断，文案一改字确认流程就静默失效。
const adminGrantConfirmHTTPStatus = http.StatusPreconditionRequired

// AdminGrant 处理单个用户的赠送（>10 元需二次确认）。
//
//	POST /admin/user/:id/grant
//	{ "amount": 10.0, "reason": "补偿", "confirmed": false, "anchor": "" }
func AdminGrant(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !requireAdmin(c) {
		return
	}
	userID, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "用户参数错误"})
		return
	}
	var payload struct {
		Amount    *float64 `json:"amount"`
		Reason    string   `json:"reason"`
		Confirmed bool     `json:"confirmed"`
		// Anchor 允许调用方自带（重试时必须传同一个），空值时服务端生成单次锚点。
		Anchor string `json:"anchor"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || payload.Amount == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请输入赠送金额"})
		return
	}
	result, err := service.AdminGrant(dao.User.UnderlyingDB(), service.AdminGrantRequest{
		Operator: operator, UserID: int32(userID), Amount: *payload.Amount,
		Reason: payload.Reason, Anchor: payload.Anchor, Confirmed: payload.Confirmed,
		ClientIP: c.ClientIP(),
	})
	if err != nil {
		writeAdminGrantError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"userId": result.UserID, "amount": result.Amount, "debtOffset": result.DebtOffset,
		"balance": result.BalanceAfter, "giftBalance": result.GiftAfter,
		"balanceBefore": result.BalanceBefore, "giftBalanceBefore": result.GiftBefore,
		"requestId": result.RequestID, "billingRecordId": result.BillingRecordID,
		"alreadyGranted": result.AlreadyGranted,
	})
}

// AdminGrantReverse 处理单个用户的追缴式回滚。
//
//	POST /admin/user/:id/grant-reversal
//	{ "amount": 10.0, "reason": "撤销误发", "anchor": "batch-7f3a" }
func AdminGrantReverse(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !requireAdmin(c) {
		return
	}
	userID, err := strconv.ParseInt(c.Param("id"), 10, 32)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "用户参数错误"})
		return
	}
	var payload struct {
		Amount *float64 `json:"amount"`
		Reason string   `json:"reason"`
		// Anchor 必须与赠送时一致，否则两笔流水的幂等键对不上原批次（118-*.md §5.3）。
		Anchor string `json:"anchor"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || payload.Amount == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请输入追缴金额"})
		return
	}
	result, err := service.AdminGrantReverse(dao.User.UnderlyingDB(), service.AdminGrantReverseRequest{
		Operator: operator, UserID: int32(userID), Amount: *payload.Amount,
		Anchor: payload.Anchor, Reason: payload.Reason, ClientIP: c.ClientIP(),
	})
	if err != nil {
		writeAdminGrantError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"userId": result.UserID,
		// 回滚必须回报三列：应扣 / 实扣 / 无法扣回（差额转本金）。
		// 只回「应扣」会让管理员以为追缴失败 —— 用户实际被扣的少于应扣额是设计如此。
		"expected": result.Expected, "fromGift": result.FromGift, "fromBalance": result.FromBalance,
		"shortfall": result.Shortfall, "balance": result.BalanceAfter, "giftBalance": result.GiftAfter,
		"giftBalanceBefore": result.GiftBefore, "balanceBefore": result.BalanceBefore,
		"giftRecordId": result.GiftRecordID, "principalRecordId": result.PrincipalRecordID,
		"skipped": result.Skipped,
	})
}

// AdminGrantBatchPreview 处理批量赠送的预览（只读，不产生任何资金动作）。
//
//	POST /admin/user/grant/batch/preview
//	{ "selector": { "userIds": [1,2,3] }, "amount": 10 }
func AdminGrantBatchPreview(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var payload struct {
		Selector batchGrantSelectorPayload `json:"selector"`
		Amount   *float64                  `json:"amount"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || payload.Amount == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请输入赠送金额"})
		return
	}
	selector, err := payload.Selector.toServiceSelector()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	result, err := service.BatchGrantPreview(dao.User.UnderlyingDB(), selector)
	if err != nil {
		writeAdminGrantError(c, err)
		return
	}
	// 预览是「只读」的：金额口径（每人 / 合计）与确认原因由 controller 补齐 ——
	// 服务层的 BatchGrantPreview 不知道管理员准备送多少，只知道「这些人可不可以收」。
	amount := *payload.Amount
	result.UnitAmount = amount
	result.TotalAmount = 0
	for range result.Eligible {
		result.TotalAmount += amount
	}
	result.ConfirmReasons = service.BatchGrantConfirmReasons(
		dao.User.UnderlyingDB(), amount, result.Total, selector.All)
	result.ConfirmRequired = len(result.ConfirmReasons) > 0
	c.JSON(http.StatusOK, result)
}

// AdminGrantBatchExecute 处理批量赠送的执行（>100 人自动转异步）。
//
//	POST /admin/user/grant/batch
//	{ "batchId": "", "selector": {...}, "amount": 10, "reason": "补偿", "confirmed": true }
func AdminGrantBatchExecute(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !requireAdmin(c) {
		return
	}
	var payload struct {
		BatchID    string                    `json:"batchId"`
		Selector   batchGrantSelectorPayload `json:"selector"`
		Amount     *float64                  `json:"amount"`
		Reason     string                    `json:"reason"`
		Confirmed  bool                      `json:"confirmed"`
		OnlyFailed bool                      `json:"onlyFailed"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || payload.Amount == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请输入赠送金额"})
		return
	}
	selector, err := payload.Selector.toServiceSelector()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	progress, err := service.BatchGrantExecute(dao.User.UnderlyingDB(), service.BatchGrantExecuteRequest{
		Operator: operator, BatchID: payload.BatchID, Selector: selector,
		Amount: *payload.Amount, Reason: payload.Reason, Confirmed: payload.Confirmed,
		OnlyFailed: payload.OnlyFailed, ClientIP: c.ClientIP(),
	})
	if err != nil {
		writeAdminGrantError(c, err)
		return
	}
	// 异步批次返回 202：前端据此轮询 /grant/batch/:batchId，不要当成本地已完成。
	status := http.StatusOK
	if progress.Status == "running" {
		status = http.StatusAccepted
	}
	c.JSON(status, progress)
}

// AdminGrantBatchRollback 处理整批的追缴式回滚（不可中断）。
//
//	POST /admin/user/grant/batch/:batchId/rollback
func AdminGrantBatchRollback(c *gin.Context) {
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	if !requireAdmin(c) {
		return
	}
	batchID := strings.TrimSpace(c.Param("batchId"))
	if batchID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "批次号不能为空"})
		return
	}
	var payload struct {
		Reason string `json:"reason"`
	}
	// 允许空 body（回滚不强制填原因），因此解析失败不算错误。
	_ = c.ShouldBindJSON(&payload)
	result, err := service.BatchGrantRollback(dao.User.UnderlyingDB(), service.BatchGrantRollbackRequest{
		Operator: operator, BatchID: batchID, Reason: payload.Reason, ClientIP: c.ClientIP(),
	})
	if err != nil {
		writeAdminGrantError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// AdminGrantBatchProgress 查询批次进度。
//
//	GET /admin/user/grant/batch/:batchId
func AdminGrantBatchProgress(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	batchID := strings.TrimSpace(c.Param("batchId"))
	progress, err := service.BatchGrantProgressWithDB(dao.User.UnderlyingDB(), batchID)
	if err != nil {
		writeAdminGrantError(c, err)
		return
	}
	c.JSON(http.StatusOK, progress)
}

// AdminGrantBatchPause 暂停批次（只停止后续处理，已赠送的不回滚）。
func AdminGrantBatchPause(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	if err := service.BatchGrantPause(strings.TrimSpace(c.Param("batchId"))); err != nil {
		writeAdminGrantError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "paused"})
}

// AdminGrantBatchAbort 中止批次（已赠送的不回滚）。
func AdminGrantBatchAbort(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	if err := service.BatchGrantAbort(strings.TrimSpace(c.Param("batchId"))); err != nil {
		writeAdminGrantError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "aborted"})
}

// AdminGrantConfirmThreshold 返回当前的二次确认阈值（前端据此决定是否弹确认框）。
func AdminGrantConfirmThreshold(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"threshold": service.AdminGrantConfirmThresholdWithDB(dao.User.UnderlyingDB()),
	})
}

// batchGrantSelectorPayload 是批量赠送用户来源的请求体。
type batchGrantSelectorPayload struct {
	UserIDs  []int32  `json:"userIds"`
	All      bool     `json:"all"`
	Statuses []string `json:"statuses"`
	LevelID  int32    `json:"levelId"`
	// 余额区间按**可用合计**（balance + gift_balance）过滤，与服务的筛选口径一致。
	MinBalance     *float64 `json:"minBalance"`
	MaxBalance     *float64 `json:"maxBalance"`
	RegisteredFrom string   `json:"registeredFrom"`
	RegisteredTo   string   `json:"registeredTo"`
	LastLoginFrom  string   `json:"lastLoginFrom"`
	LastLoginTo    string   `json:"lastLoginTo"`
}

// toServiceSelector 把请求体折成服务层选择器，时间字段按 RFC3339 / 日期两种格式解析。
func (p batchGrantSelectorPayload) toServiceSelector() (service.BatchGrantSelector, error) {
	selector := service.BatchGrantSelector{
		UserIDs: p.UserIDs, All: p.All, Statuses: p.Statuses, LevelID: p.LevelID,
		MinBalance: p.MinBalance, MaxBalance: p.MaxBalance,
	}
	fields := []struct {
		raw  string
		dest **time.Time
	}{
		{p.RegisteredFrom, &selector.RegisteredFrom},
		{p.RegisteredTo, &selector.RegisteredTo},
		{p.LastLoginFrom, &selector.LastLoginFrom},
		{p.LastLoginTo, &selector.LastLoginTo},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.raw) == "" {
			continue
		}
		parsed, err := parseAdminGrantTime(field.raw)
		if err != nil {
			return selector, err
		}
		*field.dest = parsed
	}
	return selector, nil
}

// parseAdminGrantTime 解析时间参数：优先 RFC3339，其次纯日期（按服务器本地时区零点）。
func parseAdminGrantTime(raw string) (*time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		local := parsed.Local()
		return &local, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02", trimmed, time.Local); err == nil {
		return &parsed, nil
	}
	return nil, errors.New("时间格式错误，请使用 RFC3339 或 YYYY-MM-DD")
}

// writeAdminGrantError 把服务层错误映射成 HTTP 状态码。
func writeAdminGrantError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrAdminGrantConfirmRequired), errors.Is(err, service.ErrBatchGrantConfirmRequired):
		c.JSON(adminGrantConfirmHTTPStatus, gin.H{"message": err.Error(), "confirmRequired": true})
	case errors.Is(err, service.ErrInvalidAdminGrant), errors.Is(err, service.ErrBatchGrantInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
	case errors.Is(err, service.ErrAdminGrantUserUnavailable):
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
	case errors.Is(err, service.ErrBatchGrantNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": err.Error()})
	case errors.Is(err, service.ErrBalanceAdjustmentForbidden):
		c.JSON(http.StatusForbidden, gin.H{"message": err.Error()})
	case errors.Is(err, service.ErrBalanceChanged):
		c.JSON(http.StatusConflict, gin.H{"message": err.Error()})
	case errors.Is(err, service.ErrIdempotencyConflict):
		c.JSON(http.StatusConflict, gin.H{"message": err.Error()})
	case errors.Is(err, service.ErrIdempotencyPending):
		c.JSON(http.StatusAccepted, gin.H{"status": "pending", "message": err.Error()})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "用户不存在"})
	default:
		logger.SysError("管理员赠送或追缴失败：" + logger.RedactSecrets(err.Error()))
		c.JSON(http.StatusInternalServerError, gin.H{"message": "操作失败，未保存任何变更，请稍后重试"})
	}
}
