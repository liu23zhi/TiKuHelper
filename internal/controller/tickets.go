package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

var ticketControllerDB = func() *gorm.DB { return manager.GetManager().GetDB() }
var sendTicketControllerWebhook = service.SendTicketWebhook

func TicketList(c *gin.Context) {
	user, ok := currentOperator(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", strconv.Itoa(service.TicketPageSizeDefault)))
	createdFrom, createdFromErr := parseTicketTime(c.Query("createdFrom"), false)
	createdTo, createdToErr := parseTicketTime(c.Query("createdTo"), true)
	updatedFrom, updatedFromErr := parseTicketTime(c.Query("updatedFrom"), false)
	updatedTo, updatedToErr := parseTicketTime(c.Query("updatedTo"), true)
	if createdFromErr != nil || createdToErr != nil || updatedFromErr != nil || updatedToErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "时间筛选格式无效，请使用 RFC3339 或 YYYY-MM-DD"})
		return
	}
	if err := validateListFilterTimeRange(createdFrom, createdTo, "创建时间"); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if err := validateListFilterTimeRange(updatedFrom, updatedTo, "更新时间"); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	// origin / category 是多类型工单的筛选维度：空串表示不过滤。
	// category 对普通用户也生效（他们只看得到自己的、非系统单）；
	// origin 由 service 侧只对管理员应用。
	query := service.TicketListQuery{
		Status:      strings.TrimSpace(c.Query("status")),
		Keyword:     strings.TrimSpace(c.Query("keyword")),
		Origin:      strings.TrimSpace(c.Query("origin")),
		Category:    strings.TrimSpace(c.Query("category")),
		CreatedFrom: createdFrom, CreatedTo: createdTo, UpdatedFrom: updatedFrom, UpdatedTo: updatedTo,
	}
	result, err := service.ListTicketsWithDB(withDatabasePriority(ticketControllerDB(), databaseRequestContext(c), dbqueue.PriorityLow), &user, query, page, pageSize)
	if err != nil {
		writeTicketError(c, err, "获取工单列表失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func CreateTicket(c *gin.Context) {
	user, ok := currentOperator(c)
	if !ok {
		return
	}
	var input service.CreateTicketInput
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	detail, err := service.CreateTicketWithDB(ticketControllerDB(), &user, input)
	if err != nil {
		writeTicketError(c, err, "创建工单失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": detail})
}

func TicketDetail(c *gin.Context) {
	user, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseTicketID(c)
	if !ok {
		return
	}
	detail, err := service.GetTicketWithDB(withDatabasePriority(ticketControllerDB(), databaseRequestContext(c), dbqueue.PriorityLow), &user, id)
	if err != nil {
		writeTicketError(c, err, "获取工单详情失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": detail})
}

func AddTicketMessage(c *gin.Context) {
	user, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseTicketID(c)
	if !ok {
		return
	}
	var input service.ReplyTicketInput
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	detail, err := service.AddTicketMessageWithDB(ticketControllerDB(), &user, id, input)
	if err != nil {
		writeTicketError(c, err, "回复工单失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": detail})
}

func UpdateTicket(c *gin.Context) {
	user, ok := currentOperator(c)
	if !ok {
		return
	}
	if !requireAdmin(c) {
		return
	}
	id, ok := parseTicketID(c)
	if !ok {
		return
	}
	var input service.UpdateTicketInput
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	detail, err := service.UpdateTicketWithDB(ticketControllerDB(), &user, id, input)
	if err != nil {
		writeTicketError(c, err, "更新工单失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": detail})
}

func RetryTicketNotification(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	ticketID, ok := parseTicketID(c)
	if !ok {
		return
	}
	notificationID, err := strconv.ParseInt(c.Param("notificationId"), 10, 64)
	if err != nil || notificationID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	db := withDatabasePriority(ticketControllerDB(), databaseRequestContext(c), dbqueue.PriorityLow)
	var notification entity.TicketNotification
	if err := db.Where("id = ? AND ticket_id = ?", notificationID, ticketID).First(&notification).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "提醒记录不存在"})
		return
	}
	if err := service.RetryTicketNotificationWithDB(db, notificationID); err != nil {
		writeTicketError(c, err, "重试提醒失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已加入重试队列"})
}

func TestTicketWecomWebhook(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	config, err := service.GetTicketWecomConfigWithDB(withDatabasePriority(ticketControllerDB(), databaseRequestContext(c), dbqueue.PriorityLow))
	if err != nil {
		writeTicketError(c, err, "读取企业微信工单通知配置失败")
		return
	}
	if strings.TrimSpace(config.WebhookURL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请先配置企业微信机器人 Webhook"})
		return
	}
	if !config.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请先启用企业微信工单通知"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	if err := sendTicketControllerWebhook(ctx, config, "工单系统测试通知：Webhook 配置正常。", config.MentionedList); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"message": "企业微信测试发送失败：" + service.TicketWebhookErrorMessage(err, config.WebhookURL)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "测试通知已发送"})
}

func TicketWecomSettings(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	settings, err := service.GetTicketWecomSettingsWithDB(withDatabasePriority(ticketControllerDB(), databaseRequestContext(c), dbqueue.PriorityLow))
	if err != nil {
		writeTicketError(c, err, "获取企业微信工单通知配置失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": settings})
}

func UpdateTicketWecomSettings(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var input service.UpdateTicketWecomSettingsInput
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}
	settings, err := service.UpdateTicketWecomSettingsWithDB(ticketControllerDB(), input)
	if err != nil {
		if errors.Is(err, service.ErrInvalidTicketWecomWebhook) {
			c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
			return
		}
		writeTicketError(c, err, "保存企业微信工单通知配置失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": settings})
}

func parseTicketID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "工单编号无效"})
		return 0, false
	}
	return id, true
}

func parseTicketTime(value string, endOfDay bool) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		parsed, err = time.ParseInLocation("2006-01-02", value, time.Local)
		if err == nil && endOfDay {
			parsed = parsed.AddDate(0, 0, 1)
		}
	}
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func writeTicketError(c *gin.Context, err error, fallback string) {
	status := http.StatusInternalServerError
	message := fallback
	switch {
	case errors.Is(err, service.ErrTicketSubjectRequired), errors.Is(err, service.ErrTicketBodyRequired),
		errors.Is(err, service.ErrTicketInvalidStatus), errors.Is(err, service.ErrTicketInvalidPriority),
		errors.Is(err, service.ErrTicketInvalidOrigin), errors.Is(err, service.ErrTicketInvalidCategory),
		errors.Is(err, service.ErrTicketSystemUserRequired), errors.Is(err, service.ErrTicketContextTooLarge),
		errors.Is(err, service.ErrTicketWebhookInvalid), errors.Is(err, service.ErrTicketWebhookNotConfigured), errors.Is(err, service.ErrTicketTextTooLong):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, service.ErrTicketNotFound):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, service.ErrTicketForbidden):
		status, message = http.StatusForbidden, err.Error()
	case errors.Is(err, service.ErrTicketNotificationNotRetryable):
		status, message = http.StatusConflict, err.Error()
	default:
		message += "：" + err.Error()
	}
	c.JSON(status, gin.H{"message": message})
}
