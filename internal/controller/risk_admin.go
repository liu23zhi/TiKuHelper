package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 风控管理页的 HTTP 控制器（96-风控管理仅告警与用户画像-设计.md §4.2）。
//
// 四条路由（全部 Auth + middleware.RequireAdmin，且**每个 handler 内部再校验一次**，
// 与 98-数据源修正与画像权限约束.md 的五层落地要求一致）：
//
//	GET  /adapter-service/risk/alerts              告警列表（只读）
//	GET  /adapter-service/risk/alerts/:id          告警详情（只读）
//	GET  /adapter-service/risk/webhook             风控 webhook 配置（URL 恒为空 + 脱敏预览）
//	PUT  /adapter-service/risk/webhook             风控 webhook 配置（空白/掩码 = 保持原值）
//
// ## 两条不可协商的性质
//
//  1. **只读告警**：告警列表/详情没有任何写路径。96 C1「风控只告警，不拦截」在
//     控制器层体现为「管理台**不能**从告警记录触发任何处置动作」—— 处置必须走
//     封禁/黑名单各自的接口，人工再确认一次。
//
//  2. **风控 webhook 与工单 webhook 完全独立**（96 C2/C3）。本文件只碰
//     `wecom_risk_*` 三个键，**从不读也不写** `wecom_ticket_*`。
//     两个 URL 填成同一个值也是两条独立记录，清掉其中一个不影响另一个。
//
// ⚠️ 响应里 `webhookUrl` **恒为空串**：它是 credential-bearing 凭据，
// 只回 `preview`（脱敏后的 `?key=已配置`）。这与 ticket_settings.go 一致。

// riskAdminDB 返回当前数据库句柄；未初始化时返回 nil（调用方降级）。
//
// 与 ticketControllerDB / blacklistAdminDB 同构：单测里没有 manager 时不能 panic。
var riskAdminDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

// riskAdminWebhookSend 是「发送测试消息」的可替换钩子。
//
// 抽成变量而不是直接调 service：
//   - 单测要能断言「点测试按钮确实用**风控**配置里的 URL 发」；
//   - 没有它就只能靠真实打企业微信，测试会联网且不可控。
var riskAdminWebhookSend = func(ctx context.Context, config service.RiskWecomConfig, content string, mentioned []string) error {
	return service.SendRiskTestWebhook(ctx, config, content, mentioned)
}

var errRiskDBUnavailable = errors.New("数据库未初始化")

// requireRiskAdminDB 取一个带优先级的会话，并把「没有数据库」统一成一个错误。
//
// PriorityLow：风控管理页是纯运营操作，不该抢答题链路的数据库连接。
func requireRiskAdminDB(c *gin.Context) (*gorm.DB, error) {
	db := riskAdminDB()
	if db == nil {
		return nil, errRiskDBUnavailable
	}
	return withDatabasePriority(db, databaseRequestContext(c), dbqueue.PriorityLow), nil
}

// writeRiskAdminError 把内部错误翻译成响应。
//
// 与 writeTicketError 同构：配置类错误回 400 且**原文可见**（管理员要知道哪一项不合法），
// 其余回 500 且只回通用文案（不把 SQL 错误暴露给前端，但 SysLog 里保留）。
func writeRiskAdminError(c *gin.Context, err error, fallback string) {
	switch {
	case errors.Is(err, errRiskDBUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": errRiskDBUnavailable.Error()})
	case errors.Is(err, service.ErrInvalidRiskWecomWebhook):
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"message": "风控告警不存在"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"message": fallback})
	}
}

// riskPagination 解析分页参数。非法值回落到默认（与管理端其它列表一致）。
func riskPagination(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	return page, pageSize
}

// parseRiskTimeQuery 解析 `YYYY-MM-DD` 或 RFC3339 的时间筛选。
//
// endOfDay 为 true 时把纯日期推到次日零点（不含），这样 `createdTo=2026-11-13`
// 能覆盖 13 日整天，而不是只到 13 日 00:00:00。
func parseRiskTimeQuery(value string, endOfDay bool) (*time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02", trimmed, time.Local); err == nil {
		if endOfDay {
			parsed = parsed.AddDate(0, 0, 1).Add(-time.Nanosecond)
		}
		return &parsed, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return nil, fmt.Errorf("时间格式无效：%s", trimmed)
	}
	return &parsed, nil
}

// RiskWarningList 返回风控告警列表（管理员，只读）。
//
// 响应形态：{data: [...], total, page, pageSize}（与黑名单/返佣列表一致）。
func RiskWarningList(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireRiskAdminDB(c)
	if err != nil {
		writeRiskAdminError(c, err, "获取风控告警列表失败")
		return
	}
	notified, notifiedErr := parseOptionalBoolQuery(c.Query("notified"))
	if notifiedErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数 notified 只能是 true 或 false"})
		return
	}
	createdFrom, fromErr := parseRiskTimeQuery(c.Query("createdFrom"), false)
	if fromErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "创建时间起（createdFrom）格式无效"})
		return
	}
	createdTo, toErr := parseRiskTimeQuery(c.Query("createdTo"), true)
	if toErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "创建时间止（createdTo）格式无效"})
		return
	}
	if createdFrom != nil && createdTo != nil && createdFrom.After(*createdTo) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "创建时间范围无效：起始晚于结束"})
		return
	}
	page, pageSize := riskPagination(c)
	var userID int32
	if raw := strings.TrimSpace(c.Query("userId")); raw != "" {
		parsed, parseErr := strconv.ParseInt(raw, 10, 32)
		if parseErr != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数 userId 无效"})
			return
		}
		userID = int32(parsed)
	}
	result, err := service.RiskWarningList(db, service.RiskWarningFilter{
		RuleID:      strings.TrimSpace(c.Query("ruleId")),
		UserID:      userID,
		Notified:    notified,
		CreatedFrom: createdFrom,
		CreatedTo:   createdTo,
		Page:        page,
		PageSize:    pageSize,
	})
	if err != nil {
		writeRiskAdminError(c, err, "获取风控告警列表失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result.Data, "total": result.Total, "page": result.Page, "pageSize": result.PageSize})
}

// RiskWarningDetail 返回单条风控告警（管理员，只读）。
//
// 它是 96 §4.3 消息里那条跳转链接的目标。
func RiskWarningDetail(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireRiskAdminDB(c)
	if err != nil {
		writeRiskAdminError(c, err, "获取风控告警详情失败")
		return
	}
	id, parseErr := strconv.ParseInt(c.Param("id"), 10, 64)
	if parseErr != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数 id 无效"})
		return
	}
	detail, err := service.RiskWarningDetail(db, id)
	if err != nil {
		writeRiskAdminError(c, err, "获取风控告警详情失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": detail})
}

// RiskWecomSettings 返回风控 webhook 配置（管理员）。
//
// `webhookUrl` 恒为空、`preview` 是脱敏预览 —— 绝不回显凭据。
func RiskWecomSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireRiskAdminDB(c)
	if err != nil {
		writeRiskAdminError(c, err, "获取企业微信风控告警配置失败")
		return
	}
	settings, err := service.GetRiskWecomSettings(db)
	if err != nil {
		writeRiskAdminError(c, err, "获取企业微信风控告警配置失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": settings})
}

// UpdateRiskWecomSettings 写入风控 webhook 配置（管理员）。
//
// ⚠️ 只写 `wecom_risk_*` 三键。工单那三键一个字节都不动（96 C3 判据 3）。
func UpdateRiskWecomSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	var input service.UpdateRiskWecomSettingsInput
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	db, err := requireRiskAdminDB(c)
	if err != nil {
		writeRiskAdminError(c, err, "保存企业微信风控告警配置失败")
		return
	}
	settings, err := service.UpdateRiskWecomSettings(db, input)
	if err != nil {
		writeRiskAdminError(c, err, "保存企业微信风控告警配置失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": settings})
}

// TestRiskWecomWebhook 用**风控配置**发一条测试消息（管理员）。
//
// 三个前置条件各自 400 且文案指明是哪一项（96 §4.2：两个输入框并排，
// 报错不指明框位会让管理员把风控的错怪到工单配置上）。
//
// ⚠️ 这里**绝不**在风控未配置时改用工单配置发送（96 C2）。
func TestRiskWecomWebhook(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireRiskAdminDB(c)
	if err != nil {
		writeRiskAdminError(c, err, "企业微信风控测试发送失败")
		return
	}
	config, err := service.RiskAlertConfig(db)
	if err != nil {
		writeRiskAdminError(c, err, "企业微信风控测试发送失败")
		return
	}
	if strings.TrimSpace(config.WebhookURL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请先配置企业微信**风控**机器人 Webhook（与工单 Webhook 是两套独立配置）"})
		return
	}
	if !config.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请先启用企业微信风控通知"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	if err := riskAdminWebhookSend(ctx, config, "风控系统测试通知：Webhook 配置正常（这是风控告警通道，不是工单通道）。", config.MentionedList); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "企业微信测试发送失败：" + service.TicketWebhookErrorMessage(err, config.WebhookURL)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "测试通知已发送"})
}
