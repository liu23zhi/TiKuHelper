package controller

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 敏感图片检测管理页的 HTTP 控制器（Q1，109-*.md §2.5 的页面规格）。
//
// 两条路由（管理员）：
//
//	GET  /adapter-service/sensitive-image/entries   检测记录（两 Tab = status 筛选 + 分页）
//	POST /adapter-service/sensitive-image/detect    手动触发检测（按 SHA256 或按工单）
//
// 三条不可协商的性质：
//
//  1. **手动触发完全不经过额度闸门**。管理员复核是运营动作，不该被某个用户
//     当天的限额挡住 —— 那个用户额度用满，恰恰是最需要人来看一见的情形。
//
//  2. **触发作用于 SHA256，不是工单**。检测结果按图片内容缓存（117 N1-b），
//     所以重判一张图会同时改变所有引用它的工单的结论。响应里必须写清这一次
//     涉及多少行，否则管理员会以为只改了一个工单。
//
//  3. **响应绝不回图片字节与上游原文**。缓存表本身就不存字节（字节在
//     ticket_attachments 的密文里），Reason 也已经在上游边界截断过。

// sensitiveImageAdminDB 取数据库句柄；未初始化时返回 nil（调用方降级成 500）。
//
// 与 blacklistAdminDB / aiOCRCacheDatabase 同构：单测里没有 manager 时不能 panic。
var sensitiveImageAdminDB = func(c *gin.Context) *gorm.DB {
	mg := manager.GetManager()
	if mg == nil {
		return nil
	}
	return withDatabasePriority(mg.GetDB(), databaseRequestContext(c), dbqueue.PriorityLow)
}

// SensitiveImageEntryList 返回检测记录列表（管理员）。
//
// 「两个 Tab」（超额未检测 / 全部检测记录）在服务端体现为一个 status 筛选：
// 前端 Tab 只需换 status 参数，不必让后端维护两套查询。
// 超额未检测的图**不在缓存表里**（额度耗尽时根本没写缓存），所以那一 Tab
// 的数据源是 ticket_attachments 的 scan_state，见下方 quotaExceeded 分支。
func SensitiveImageEntryList(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db := sensitiveImageAdminDB(c)
	if db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "数据库未初始化"})
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
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	imageSHA := strings.ToLower(strings.TrimSpace(c.Query("imageSha256")))
	if imageSHA != "" && !isSensitiveImageSHA256(imageSHA) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "图片标识无效"})
		return
	}

	query := db.Model(&entity.SensitiveImageCacheEntry{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if imageSHA != "" {
		query = query.Where("image_sha256 = ?", imageSHA)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取敏感图片检测记录失败"})
		return
	}
	rows := make([]entity.SensitiveImageCacheEntry, 0, pageSize)
	if err := query.Order("updated_at DESC, image_sha256 ASC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "获取敏感图片检测记录失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data": rows, "total": total, "page": page, "pageSize": pageSize,
	})
}

// SensitiveImageQuotaStatus 返回额度配置与当前用户的当日用量（管理员）。
//
// 单独一条只读接口的理由：管理页要能回答「今天这个人还剩几次」，
// 而这个读数不能靠列表推断（额度表与缓存表是两张独立表）。
func SensitiveImageQuotaStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db := sensitiveImageAdminDB(c)
	if db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "数据库未初始化"})
		return
	}
	policy := service.LoadSensitiveImageQuotaPolicyWithDB(db)
	response := gin.H{
		"enabled":        policy.Enabled,
		"perTicketLimit": policy.PerTicketLimit,
		"dailyUserLimit": policy.DailyUserLimit,
	}
	if userID, err := strconv.ParseInt(strings.TrimSpace(c.Query("userId")), 10, 32); err == nil && userID > 0 {
		date := strings.TrimSpace(c.Query("date"))
		if date == "" {
			date = service.SensitiveImageQuotaStatusDefaultDate()
		}
		used, err := service.SensitiveImageQuotaUsageWithDB(db, int32(userID), date)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "读取当日额度失败"})
			return
		}
		response["userId"] = userID
		response["date"] = date
		response["usedCount"] = used
		if policy.DailyUserLimit > 0 {
			remaining := policy.DailyUserLimit - int(used)
			if remaining < 0 {
				remaining = 0
			}
			response["remaining"] = remaining
		}
	}
	c.JSON(http.StatusOK, response)
}

// SensitiveImageQuotaUpdate 修改三个配置键（管理员）。
//
// 指针语义：只传某个字段就只改那个键（与仓内其它系统设置接口一致）。
type SensitiveImageQuotaUpdate struct {
	Enabled        *bool `json:"enabled"`
	PerTicketLimit *int  `json:"perTicketLimit"`
	DailyUserLimit *int  `json:"dailyUserLimit"`
}

func SensitiveImageQuotaUpdateHandler(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var request SensitiveImageQuotaUpdate
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数无效"})
		return
	}
	db := sensitiveImageAdminDB(c)
	if db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "数据库未初始化"})
		return
	}
	if err := service.SetSensitiveImageQuotaPolicyWithDB(db, request.Enabled, request.PerTicketLimit, request.DailyUserLimit); err != nil {
		if errors.Is(err, service.ErrSensitiveImageQuotaInvalidLimit) {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"message": "保存敏感图片额度配置失败"})
		return
	}
	policy := service.LoadSensitiveImageQuotaPolicyWithDB(db)
	c.JSON(http.StatusOK, gin.H{
		"enabled":        policy.Enabled,
		"perTicketLimit": policy.PerTicketLimit,
		"dailyUserLimit": policy.DailyUserLimit,
	})
}

// SensitiveImageDetectRequest 是手动触发检测的请求体。
//
// 两种寻址方式（二选一）：
//   - ImageSHA256：管理员在列表里点某一行
//   - TicketID：107 Q1 ③ 拍板「按工单批量触发」
//
// 两者都给时**只处理 ImageSHA256**，并在响应里说明忽略了 ticketId ——
// 静默二选一会让人以为批量跑了。
type SensitiveImageDetectRequest struct {
	ImageSHA256 string `json:"imageSha256"`
	TicketID    int64  `json:"ticketId"`
}

// SensitiveImageManualDetect 手动触发敏感图检测（管理员）。
//
// 说明为什么这里**没有**额度检查：手动触发是运营复核通道，额度是防滥用的
// 用户侧闸门。管理员看到一张被 sensitive_by_quota 拦下的图，本来就该能查它。
func SensitiveImageManualDetect(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var request SensitiveImageDetectRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数无效"})
		return
	}
	request.ImageSHA256 = strings.ToLower(strings.TrimSpace(request.ImageSHA256))
	if request.ImageSHA256 == "" && request.TicketID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "需要提供 imageSha256 或 ticketId"})
		return
	}
	if request.ImageSHA256 != "" && !isSensitiveImageSHA256(request.ImageSHA256) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "图片标识无效"})
		return
	}
	db := sensitiveImageAdminDB(c)
	if db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "数据库未初始化"})
		return
	}

	// 按工单批量：取该工单的附件（解密后交给检测）。额度在这里**不检查**。
	if request.ImageSHA256 == "" {
		result, err := service.ManualDetectSensitiveImageTicketWithDB(c.Request.Context(), db, request.TicketID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
		return
	}

	// 按 SHA256：缓存表不存字节，字节必须从 ticket_attachments 里按 sha256 找一行解密。
	plain, mimeType, err := service.LoadSensitiveImageBytesWithDB(db, request.ImageSHA256)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrSensitiveImageBytesUnavailable):
			c.JSON(http.StatusNotFound, gin.H{"message": "找不到该图片的原始字节，无法重新检测"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"message": "读取图片失败"})
		}
		return
	}
	now := service.SensitiveImageQuotaNow()
	result, err := service.ManualDetectSensitiveImageWithDB(c.Request.Context(), db, plain, mimeType, now)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": err.Error()})
		return
	}
	// 结论跟着 SHA256 走：写回缓存后，所有引用这张图的附件都要同步 scan_state，
	// 否则「管理页显示已重判为 clean，但用户看到的还是隐藏」。
	updated, syncErr := service.SyncSensitiveImageScanStateWithDB(db, request.ImageSHA256, string(result.State))
	response := gin.H{
		"result":              result,
		"affectedAttachments": updated,
	}
	if syncErr != nil {
		// 同步失败必须如实告知：缓存已经改了，但附件行没跟上，展示会不一致。
		response["attachSyncError"] = syncErr.Error()
	}
	c.JSON(http.StatusOK, response)
}

// isSensitiveImageSHA256 校验 64 位十六进制（缓存主键的唯一合法形态）。
func isSensitiveImageSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
