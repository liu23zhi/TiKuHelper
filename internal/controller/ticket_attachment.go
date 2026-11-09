package controller

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
)

// UploadTicketAttachment 接收一张工单图片。
//
// 时序刻意是「先上传、后检测」：本接口只做本地校验 + 加密落库 + 标记 pending，
// **不同步调用外部检测**。因此上传不受上游延迟或故障影响，前端立刻拿到附件 ID；
// 检测由 service.StartTicketAttachmentScanWorker 异步完成，完成后 scan_state 才变化。
//
// 工单归属：query `ticketId` 可选。
//   - 带 ticketId：图片直接挂到该工单（必须是本人工单，或调用者是管理员）；
//   - 不带：草稿态（ticket_id=0），用于「提交工单时带图」，建单事务里认领。
//
// 鉴权：controller.Auth（路由层）+ 服务层按工单归属再判一次。
func UploadTicketAttachment(c *gin.Context) {
	user, ok := currentOperator(c)
	if !ok {
		return
	}
	var ticketID int64
	if raw := strings.TrimSpace(c.Query("ticketId")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"message": "工单编号无效"})
			return
		}
		ticketID = parsed
	}
	db := withDatabasePriority(ticketControllerDB(), databaseRequestContext(c), dbqueue.PriorityLow)
	if db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "工单附件暂不可用"})
		return
	}
	data, err := readSingleTicketAttachmentPart(c, service.MaxTicketAttachmentBytes)
	if err != nil {
		writeTicketAttachmentError(c, err)
		return
	}
	info, err := service.StoreTicketAttachmentWithDB(db, &user, ticketID, data)
	if err != nil {
		writeTicketAttachmentError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"data": info})
}

// readSingleTicketAttachmentPart 是严格的单文件 multipart 解析。
//
// 与退款凭证上传同构，逐条：
//   - 要求字段名恰好是 file 且带文件名 → 拒绝把一个普通表单字段当图片；
//   - io.LimitReader(limit+1) 读取，超出上限即判无效 → 不信任 Content-Length；
//   - 读完第一个 part 后必须正好 EOF → 拒绝「多文件 / 夹带额外 part」的请求，
//     避免把请求体当成任意容器。
func readSingleTicketAttachmentPart(c *gin.Context, limit int) ([]byte, error) {
	reader, err := c.Request.MultipartReader()
	if err != nil {
		return nil, service.ErrTicketAttachmentInvalid
	}
	part, err := reader.NextPart()
	if err != nil {
		return nil, service.ErrTicketAttachmentInvalid
	}
	defer part.Close()
	if part.FormName() != "file" || part.FileName() == "" {
		return nil, service.ErrTicketAttachmentInvalid
	}
	data, err := io.ReadAll(io.LimitReader(part, int64(limit)+1))
	if err != nil || len(data) > limit {
		return nil, service.ErrTicketAttachmentInvalid
	}
	if _, err := reader.NextPart(); err != io.EOF {
		return nil, service.ErrTicketAttachmentInvalid
	}
	return data, nil
}

// DownloadTicketAttachment 是**用户端**读图接口。
//
// 关键点：敏感图不返回任何字节，只返回占位所需的状态（JSON），
// 前端据此渲染「此图片包含敏感内容，已隐藏」，且不提供任何查看入口。
func DownloadTicketAttachment(c *gin.Context) {
	user, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseTicketAttachmentID(c)
	if !ok {
		return
	}
	db := withDatabasePriority(ticketControllerDB(), databaseRequestContext(c), dbqueue.PriorityLow)
	mimeType, data, _, err := service.ReadTicketAttachmentWithDB(db, &user, id)
	if err != nil {
		writeTicketAttachmentError(c, err)
		return
	}
	writeTicketAttachmentBytes(c, mimeType, data, "attachment")
}

// AdminDownloadTicketAttachment 是**管理端**读图接口。
//
// 与用户端的唯一区别：管理员可以拿到敏感图的字节。这不是「放宽」，而是把
// 「谁能看敏感内容」收敛成一个可审计的动作：
//   - 路由必须挂 middleware.RequireAdmin（第二道），handler 内 requireAdmin 是第三道；
//   - 每次真正下发**非 approved** 的图片都写一条账户审计（谁、何时、哪张图、什么判定）。
//
// approved 的正常图片也会写审计，理由：审计只关心「管理员看了用户的图片」这一事实，
// 用 scan 状态分叉会让审计记录出现缺口。
func AdminDownloadTicketAttachment(c *gin.Context) {
	user, ok := currentOperator(c)
	if !ok {
		return
	}
	if !requireAdmin(c) {
		return
	}
	id, ok := parseTicketAttachmentID(c)
	if !ok {
		return
	}
	db := withDatabasePriority(ticketControllerDB(), databaseRequestContext(c), dbqueue.PriorityLow)
	mimeType, data, allowSensitive, err := service.ReadTicketAttachmentWithDB(db, &user, id)
	if err != nil {
		writeTicketAttachmentError(c, err)
		return
	}
	// 审计先写、再回字节：即使后续断连，也已经留下「谁请求过」的记录。
	recordAccountAudit(c, &user, "ticket", logActionSearch, "管理员查看工单图片", map[string]any{
		"status":  "success",
		"message": "管理员查看工单图片",
		"scene":   "ticket_attachment_admin_view",
		"name":    "attachmentId=" + strconv.FormatInt(id, 10) + " sensitive=" + strconv.FormatBool(allowSensitive),
	})
	writeTicketAttachmentBytes(c, mimeType, data, "attachment")
}

// ListTicketAttachments 返回工单的附件列表（含占位条目）。
func ListTicketAttachments(c *gin.Context) {
	user, ok := currentOperator(c)
	if !ok {
		return
	}
	ticketID, ok := parseTicketID(c)
	if !ok {
		return
	}
	db := withDatabasePriority(ticketControllerDB(), databaseRequestContext(c), dbqueue.PriorityLow)
	// 复用工单详情做鉴权：不属于自己的工单直接 404，不泄露「有没有附件」。
	if _, err := service.GetTicketWithDB(db, &user, ticketID); err != nil {
		writeTicketError(c, err, "获取工单附件失败")
		return
	}
	rows, err := service.ListTicketAttachmentsWithDB(db, ticketID)
	if err != nil {
		writeTicketAttachmentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows})
}

func parseTicketAttachmentID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("attachmentId"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "图片编号无效"})
		return 0, false
	}
	return id, true
}

// writeTicketAttachmentBytes 统一下发图片字节的响应头。
//
// Content-Security-Policy sandbox + nosniff 与退款凭证一致：这类响应一旦被当作
// 文档/脚本解释就是 XSS 面，必须显式降级为「不可执行」。
func writeTicketAttachmentBytes(c *gin.Context, mimeType string, data []byte, _ string) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Data(http.StatusOK, mimeType, data)
}

func writeTicketAttachmentError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "工单图片处理失败"
	switch {
	case errors.Is(err, service.ErrTicketAttachmentInvalid):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, service.ErrTicketAttachmentLimit):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, service.ErrTicketAttachmentNotFound):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, service.ErrTicketAttachmentHidden):
		// 403 + 明确的 code：前端凭 code 渲染占位，不需要（也不允许）再试一次。
		c.JSON(http.StatusForbidden, gin.H{"code": "TICKET_ATTACHMENT_HIDDEN", "message": err.Error()})
		return
	case errors.Is(err, service.ErrTicketAttachmentForbidden), errors.Is(err, service.ErrTicketForbidden):
		status, message = http.StatusForbidden, err.Error()
	case errors.Is(err, service.ErrTicketNotFound):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, service.ErrTicketAttachmentDisabled):
		status, message = http.StatusServiceUnavailable, "工单图片功能尚未启用"
	default:
		message = "工单图片处理失败：" + err.Error()
	}
	c.JSON(status, gin.H{"message": message})
}
