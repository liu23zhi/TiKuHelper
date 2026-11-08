package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
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
