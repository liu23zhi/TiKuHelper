package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

var ticketRefundDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

var refundManualReferencePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,96}$`)

type ticketRefundResponse struct {
	entity.TicketRefund
	HasProof bool `json:"hasProof"`
}

func ticketRefundRequest(c *gin.Context, admin bool) (*gorm.DB, entity.User, bool) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	operator, ok := currentOperator(c)
	if !ok || (admin && !requireAdmin(c)) {
		return nil, entity.User{}, false
	}
	db := ticketRefundDB()
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "REFUND_UNAVAILABLE", "message": "退款工单暂不可用"})
		return nil, entity.User{}, false
	}
	return db.WithContext(c.Request.Context()), operator, true
}

func ticketRefundID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		writeTicketRefundError(c, service.ErrTicketRefundInvalid)
		return 0, false
	}
	return id, true
}

func ticketRefundVerifier(db *gorm.DB) service.TicketRefundOrderVerifier {
	cfg, err := service.LoadTicketRefundEPayConfigWithDB(db)
	if err != nil {
		return nil
	}
	return cfg.VerifyOriginalOrder
}

func withTicketRefundProofFlags(db *gorm.DB, refunds []entity.TicketRefund) ([]ticketRefundResponse, error) {
	result := make([]ticketRefundResponse, 0, len(refunds))
	ids := make([]int64, 0, len(refunds))
	for _, refund := range refunds {
		if refund.Kind == service.TicketRefundKindManual {
			ids = append(ids, refund.ID)
		}
	}
	hasProof := make(map[int64]int32)
	if len(ids) > 0 {
		var rows []struct {
			RefundID int64
			UserID   int32
		}
		if err := db.Model(&entity.TicketRefundProof{}).Select("refund_id", "user_id").Where("refund_id IN ?", ids).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			hasProof[row.RefundID] = row.UserID
		}
	}
	for _, refund := range refunds {
		owner, found := hasProof[refund.ID]
		result = append(result, ticketRefundResponse{TicketRefund: refund, HasProof: found && owner == refund.UserID})
	}
	return result, nil
}

func respondTicketRefund(c *gin.Context, db *gorm.DB, code int, refund entity.TicketRefund) {
	rows, err := withTicketRefundProofFlags(db, []entity.TicketRefund{refund})
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	c.JSON(code, rows[0])
}

func EligibleTicketRefundOrders(c *gin.Context) {
	db, actor, ok := ticketRefundRequest(c, false)
	if !ok {
		return
	}
	orders, err := service.ListEligibleTicketRefundOrdersWithDB(db, actor, ticketRefundVerifier(db))
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	if orders == nil {
		orders = []service.TicketRefundOrder{}
	}
	c.JSON(http.StatusOK, orders)
}

func TicketRefundList(c *gin.Context) {
	db, actor, ok := ticketRefundRequest(c, false)
	if !ok {
		return
	}
	refunds, err := service.ListTicketRefundsWithDB(db, actor)
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	rows, err := withTicketRefundProofFlags(db, refunds)
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	c.JSON(http.StatusOK, rows)
}

func CreateTicketRefund(c *gin.Context) {
	db, actor, ok := ticketRefundRequest(c, false)
	if !ok {
		return
	}
	var input service.CreateTicketRefundInput
	if c.ShouldBindJSON(&input) != nil {
		writeTicketRefundError(c, service.ErrTicketRefundInvalid)
		return
	}
	// 多选提交（2026-11-18）：orderIds 非空时走批量入口，每个订单各生成一张退款单。
	//
	// 单张退款单只挂一个订单是数据约束（order_id 有 uniqueIndex，防重复退款），
	// 所以多选在服务端展开成多张单，但对外仍是一次请求、一次 UI 动作。
	if len(input.OrderIDs) > 0 {
		rows, err := service.CreateTicketRefundsForOrdersWithDB(db, actor, input, ticketRefundVerifier(db))
		if err != nil {
			writeTicketRefundError(c, err)
			return
		}
		items, err := withTicketRefundProofFlags(db, rows)
		if err != nil {
			writeTicketRefundError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"count": len(items),
			"items": items,
		})
		return
	}
	refund, err := service.CreateTicketRefundWithDB(db, actor, input, ticketRefundVerifier(db))
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	respondTicketRefund(c, db, http.StatusCreated, refund)
}

func TicketRefundDetail(c *gin.Context) {
	db, actor, ok := ticketRefundRequest(c, false)
	if !ok {
		return
	}
	id, ok := ticketRefundID(c)
	if !ok {
		return
	}
	refund, err := service.GetTicketRefundWithDB(db, actor, id)
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	respondTicketRefund(c, db, http.StatusOK, refund)
}

func ReviewTicketRefund(c *gin.Context) {
	db, actor, ok := ticketRefundRequest(c, true)
	if !ok {
		return
	}
	id, ok := ticketRefundID(c)
	if !ok {
		return
	}
	var input struct {
		Action        string `json:"action"`
		ApprovedCents int64  `json:"approvedCents"`
	}
	if c.ShouldBindJSON(&input) != nil || (input.Action != "approve" && input.Action != "reject") {
		writeTicketRefundError(c, service.ErrTicketRefundInvalid)
		return
	}
	refund, err := service.GetTicketRefundWithDB(db, actor, id)
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	if input.Action == "approve" && refund.Kind == service.TicketRefundKindManual && refund.Status == service.TicketRefundStatusPendingReview {
		if _, _, err := service.ReadTicketRefundProofWithDB(db, actor, id); err != nil {
			if errors.Is(err, service.ErrTicketRefundNotFound) {
				err = service.ErrTicketRefundProofRequired
			}
			writeTicketRefundError(c, err)
			return
		}
	}
	refund, err = service.ReviewTicketRefundWithDB(db, actor, id, input.Action == "approve", input.ApprovedCents, ticketRefundVerifier(db))
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	respondTicketRefund(c, db, http.StatusOK, refund)
}

func UploadTicketRefundProof(c *gin.Context) {
	db, actor, ok := ticketRefundRequest(c, false)
	if !ok {
		return
	}
	id, ok := ticketRefundID(c)
	if !ok {
		return
	}
	reader, err := c.Request.MultipartReader()
	if err != nil {
		writeTicketRefundError(c, service.ErrTicketRefundProofInvalid)
		return
	}
	part, err := reader.NextPart()
	if err != nil {
		writeTicketRefundError(c, service.ErrTicketRefundProofInvalid)
		return
	}
	defer part.Close()
	if part.FormName() != "file" || part.FileName() == "" {
		writeTicketRefundError(c, service.ErrTicketRefundProofInvalid)
		return
	}
	data, err := io.ReadAll(io.LimitReader(part, service.MaxTicketRefundProofBytes+1))
	if err != nil || len(data) > service.MaxTicketRefundProofBytes {
		writeTicketRefundError(c, service.ErrTicketRefundProofInvalid)
		return
	}
	if _, err := reader.NextPart(); err != io.EOF {
		writeTicketRefundError(c, service.ErrTicketRefundProofInvalid)
		return
	}
	if err := service.StoreTicketRefundProofWithDB(db, actor, id, data); err != nil {
		writeTicketRefundError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"hasProof": true})
}

func DownloadTicketRefundProof(c *gin.Context) {
	db, actor, ok := ticketRefundRequest(c, false)
	if !ok {
		return
	}
	id, ok := ticketRefundID(c)
	if !ok {
		return
	}
	mimeType, data, err := service.ReadTicketRefundProofWithDB(db, actor, id)
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	suffix := "png"
	if mimeType == "image/jpeg" {
		suffix = "jpg"
	}
	c.Header("Content-Disposition", "attachment; filename=refund-proof."+suffix)
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Data(http.StatusOK, mimeType, data)
}

func ProcessTicketRefund(c *gin.Context) {
	db, actor, ok := ticketRefundRequest(c, true)
	if !ok {
		return
	}
	id, ok := ticketRefundID(c)
	if !ok {
		return
	}
	var input struct {
		Action string `json:"action"`
	}
	if c.ShouldBindJSON(&input) != nil || (input.Action != "submit" && input.Action != "query" && input.Action != "cancel_unsent") {
		writeTicketRefundError(c, service.ErrTicketRefundInvalid)
		return
	}
	if input.Action == "cancel_unsent" {
		// Proven never sent; do not require a still-working gateway to
		// release this hold. The service serializes against Begin.
		refund, err := service.CancelUnsentTicketRefundWithDB(db, actor, id)
		if err != nil {
			writeTicketRefundError(c, err)
			return
		}
		respondTicketRefund(c, db, http.StatusOK, refund)
		return
	}
	cfg, err := service.LoadTicketRefundEPayConfigWithDB(db)
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	refund, err := service.GetTicketRefundWithDB(db, actor, id)
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	if refund.Kind != service.TicketRefundKindOriginal || refund.OrderID == nil {
		writeTicketRefundError(c, service.ErrTicketRefundState)
		return
	}
	if input.Action == "submit" {
		// Durable state first, then exactly one authorized dispatch. A
		// request crash after commit can be queried, never blindly resent.
		refund, err = service.BeginTicketRefundWithDB(db, actor, id, cfg.VerifyOriginalOrder)
		if err != nil {
			writeTicketRefundError(c, err)
			return
		}
	}
	var order entity.RechargeOrder
	err = db.Where("id = ? AND user_id = ?", *refund.OrderID, refund.UserID).First(&order).Error
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	if input.Action == "submit" {
		if err := service.SubmitTicketRefundEPay(c.Request.Context(), cfg, refund, order); err == nil {
			respondTicketRefund(c, db, http.StatusAccepted, refund)
			return
		}
		// Client disconnect is not a terminal payment result. Record unknown
		// if possible; processing is equally non-retriable and keeps the hold.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 4*time.Second)
		defer cancel()
		updated, stateErr := service.ReconcileTicketRefundWithDB(db.WithContext(ctx), actor, id, service.TicketRefundStatusUnknown)
		if stateErr == nil {
			refund = updated
		}
		respondTicketRefund(c, db.WithContext(ctx), http.StatusAccepted, refund)
		return
	}
	outcome, reference, err := service.QueryTicketRefundEPay(c.Request.Context(), cfg, refund, order)
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 4*time.Second)
	defer cancel()
	updated, err := service.ReconcileTicketRefundWithDB(db.WithContext(ctx), actor, id, outcome, reference)
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	respondTicketRefund(c, db.WithContext(ctx), http.StatusOK, updated)
}

func ReconcileManualTicketRefund(c *gin.Context) {
	db, actor, ok := ticketRefundRequest(c, true)
	if !ok {
		return
	}
	id, ok := ticketRefundID(c)
	if !ok {
		return
	}
	var input struct {
		Outcome   string `json:"outcome"`
		Reference string `json:"reference"`
	}
	if c.ShouldBindJSON(&input) != nil || !refundManualReferencePattern.MatchString(input.Reference) ||
		(input.Outcome != service.TicketRefundStatusSucceeded && input.Outcome != service.TicketRefundStatusFailed) {
		writeTicketRefundError(c, service.ErrTicketRefundInvalid)
		return
	}
	refund, err := service.GetTicketRefundWithDB(db, actor, id)
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	if refund.Kind != service.TicketRefundKindManual {
		writeTicketRefundError(c, service.ErrTicketRefundForbidden)
		return
	}
	updated, err := service.ReconcileTicketRefundWithDB(db, actor, id, input.Outcome, input.Reference)
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	respondTicketRefund(c, db, http.StatusOK, updated)
}

func writeTicketRefundError(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, "REFUND_ERROR", "退款工单处理失败"
	switch {
	case errors.Is(err, service.ErrTicketRefundInvalid), errors.Is(err, service.ErrTicketRefundAmount):
		status, code, message = http.StatusBadRequest, "REFUND_INVALID", "退款参数或金额无效"
	case errors.Is(err, service.ErrTicketRefundProofInvalid):
		status, code, message = http.StatusBadRequest, "REFUND_PROOF_INVALID", service.ErrTicketRefundProofInvalid.Error()
	case errors.Is(err, service.ErrTicketRefundForbidden):
		status, code, message = http.StatusForbidden, "REFUND_FORBIDDEN", "无权执行此退款操作"
	case errors.Is(err, service.ErrTicketRefundNotFound):
		status, code, message = http.StatusNotFound, "REFUND_NOT_FOUND", "未找到退款记录或凭证"
	case errors.Is(err, service.ErrTicketRefundOrderUnavailable):
		status, code, message = http.StatusConflict, "REFUND_ORDER_UNAVAILABLE", "该订单无法申请原路退款，请核对资格或提交人工工单"
	case errors.Is(err, service.ErrTicketRefundInsufficientBalance):
		status, code, message = http.StatusConflict, "REFUND_INSUFFICIENT_BALANCE", "可用余额不足，未冻结资金"
	case errors.Is(err, service.ErrTicketRefundProofRequired):
		status, code, message = http.StatusConflict, "REFUND_PROOF_REQUIRED", service.ErrTicketRefundProofRequired.Error()
	case errors.Is(err, service.ErrTicketRefundProofExists), errors.Is(err, service.ErrTicketRefundState):
		status, code, message = http.StatusConflict, "REFUND_STATE_CONFLICT", "当前状态不允许此操作，请刷新核对"
	case errors.Is(err, service.ErrTicketRefundChannelDisabled), errors.Is(err, service.ErrTicketRefundProofDisabled):
		status, code, message = http.StatusServiceUnavailable, "REFUND_CHANNEL_UNAVAILABLE", err.Error()
	case errors.Is(err, service.ErrTicketRefundChannelUnknown):
		status, code, message = http.StatusBadGateway, "REFUND_CHANNEL_UNKNOWN", service.ErrTicketRefundChannelUnknown.Error()
	}
	c.JSON(status, gin.H{"code": code, "message": message})
}

// RecoverTicketRefundPayoutQr 用当前密码取回该用户**上次退款用过的收款码**。
//
// 2026-11-19 用户要求：「提交的时候输入密码才能看到之前上传的收款码；
// 替换不需要。添加本地缓存，如果是本机上传的，直接加载，换了设备才需要密码
// 来恢复数据。」
//
// 所以这个接口只在「换设备 / 清过缓存」时被调用一次，拿到 ID 后前端写回
// 本地缓存，之后同机再打开就直接用缓存、不再要密码。
//
// 安全要点：
//
//	· 收款码是支付账号信息，跨设备取回必须验密 —— 只凭登录态不够，
//	  因为登录态可能来自一台共享电脑。
//	· 密码校验复用**登录失败计数**做限流（与注销申请同一口径）：同一个账号的
//	  凭据被反复试探，锁定策略应当一致，另起一套计数器只会让爆破面变大。
//	· 只返回附件 ID，不直接返回图片字节：真正取图仍走
//	  /api/ticket-attachments/:id（有既有的归属校验与审计）。
func RecoverTicketRefundPayoutQr(c *gin.Context) {
	db, actor, ok := ticketRefundRequest(c, false)
	if !ok {
		return
	}
	var payload struct {
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	password := strings.TrimSpace(payload.Password)
	if password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请输入当前密码"})
		return
	}

	// 限流：与注销申请完全一致（同一份凭据，锁定口径必须一致）。
	policy := service.AccountLoginPolicy(db)
	subjectKey := service.NormalizeLoginAccountKey(actor.Username)
	if lock, err := service.CheckLoginLockoutWithDB(db, policy, subjectKey, time.Now()); err == nil && lock.Locked {
		c.JSON(http.StatusTooManyRequests, gin.H{
			"message": fmt.Sprintf("密码错误次数过多，请 %d 分钟后再试", lock.RemainingMinutesCeil()),
		})
		return
	}
	if matches, _ := util.VerifyPassword(actor.Password, password); !matches {
		if _, recordErr := service.RecordLoginFailureWithDB(db, policy, subjectKey, time.Now()); recordErr != nil {
			logger.SysWarn("记录收款码恢复的密码失败次数失败: " + logger.RedactSecrets(recordErr.Error()))
		}
		c.JSON(http.StatusBadRequest, gin.H{"message": "密码错误"})
		return
	}
	if clearErr := service.ClearLoginAttemptsOnSuccessWithDB(db, policy, subjectKey, time.Now()); clearErr != nil {
		logger.SysWarn("清除登录失败计数失败: " + logger.RedactSecrets(clearErr.Error()))
	}

	attachmentID, err := service.LatestRefundPayoutQrAttachmentIDWithDB(db, actor.ID)
	if err != nil {
		writeTicketRefundError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	if attachmentID <= 0 {
		// 没有历史记录：给 200 + 0，前端提示「没有可恢复的收款码」。
		// 不回 404 —— 那不是「资源不存在」，而是「你还没用它」。
		c.JSON(http.StatusOK, gin.H{"attachmentId": 0})
		return
	}
	c.JSON(http.StatusOK, gin.H{"attachmentId": attachmentID})
}
