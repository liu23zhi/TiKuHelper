package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	TicketSubjectMaxLength             = 120
	TicketBodyMaxLength                = 10000
	TicketPageSizeDefault              = 20
	TicketPageSizeMax                  = 100
	TicketWecomEnabledSettingKey       = "wecom_ticket_notifications_enabled"
	TicketWecomWebhookURLSettingKey    = "wecom_ticket_webhook_url"
	TicketWecomMentionedListSettingKey = "wecom_ticket_mentioned_list"

	ticketNotificationPending     = "pending"
	ticketNotificationSending     = "sending"
	ticketNotificationSent        = "sent"
	ticketNotificationFailed      = "failed"
	ticketNotificationSkipped     = "skipped"
	TicketNotificationMaxAttempts = 3
)

var (
	ErrTicketSubjectRequired          = errors.New("工单标题不能为空")
	ErrTicketBodyRequired             = errors.New("工单内容不能为空")
	ErrTicketTextTooLong              = errors.New("工单内容长度超出限制")
	ErrTicketNotFound                 = errors.New("工单不存在")
	ErrTicketForbidden                = errors.New("无权访问该工单")
	ErrTicketInvalidStatus            = errors.New("工单状态无效")
	ErrTicketInvalidPriority          = errors.New("工单优先级无效")
	ErrTicketWebhookInvalid           = errors.New("企业微信机器人 Webhook 必须是 HTTPS 地址")
	ErrTicketWebhookNotConfigured     = errors.New("企业微信工单通知未启用或未配置有效 Webhook")
	ErrTicketNotificationNotRetryable = errors.New("该提醒当前不可重试")
)

type TicketListQuery struct {
	Status      string
	Keyword     string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	UpdatedFrom *time.Time
	UpdatedTo   *time.Time
}

type TicketListResult struct {
	Items    []TicketListItem `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

type TicketListItem struct {
	ID            int64      `json:"id"`
	UserID        int32      `json:"userId"`
	UserName      string     `json:"userName"`
	Subject       string     `json:"subject"`
	Status        string     `json:"status"`
	Priority      string     `json:"priority"`
	LastMessageAt time.Time  `json:"lastMessageAt"`
	ClosedAt      *time.Time `json:"closedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	MessageCount  int64      `json:"messageCount"`
}

type TicketDetail struct {
	Ticket       entity.Ticket           `json:"ticket"`
	Messages     []entity.TicketMessage  `json:"messages"`
	Notification *TicketNotificationInfo `json:"notification,omitempty"`
}

type TicketNotificationInfo struct {
	ID           int64      `json:"id"`
	Status       string     `json:"status"`
	AttemptCount int        `json:"attemptCount"`
	LastError    string     `json:"lastError,omitempty"`
	SentAt       *time.Time `json:"sentAt,omitempty"`
}

type CreateTicketInput struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

type ReplyTicketInput struct {
	Body   string  `json:"body"`
	Status *string `json:"status,omitempty"`
}

type UpdateTicketInput struct {
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

func normalizeTicketText(value string, max int, field error) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", field
	}
	if utf8.RuneCountInString(value) > max {
		return "", fmt.Errorf("%w：最多%d个字符", ErrTicketTextTooLong, max)
	}
	return value, nil
}

func normalizeTicketStatus(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case entity.TicketStatusOpen, entity.TicketStatusPending, entity.TicketStatusResolved, entity.TicketStatusClosed:
		return value, nil
	default:
		return "", ErrTicketInvalidStatus
	}
}

func normalizeTicketPriority(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return entity.TicketPriorityNormal, nil
	}
	if value != entity.TicketPriorityNormal {
		return "", ErrTicketInvalidPriority
	}
	return value, nil
}

func isTicketAdmin(user *entity.User) bool {
	return user != nil && strings.EqualFold(strings.TrimSpace(user.Role), "admin")
}

func CreateTicketWithDB(db *gorm.DB, user *entity.User, input CreateTicketInput) (TicketDetail, error) {
	if db == nil || user == nil {
		return TicketDetail{}, ErrTicketForbidden
	}
	subject, err := normalizeTicketText(input.Subject, TicketSubjectMaxLength, ErrTicketSubjectRequired)
	if err != nil {
		return TicketDetail{}, err
	}
	body, err := normalizeTicketText(input.Body, TicketBodyMaxLength, ErrTicketBodyRequired)
	if err != nil {
		return TicketDetail{}, err
	}

	now := time.Now()
	var ticket entity.Ticket
	err = db.Transaction(func(tx *gorm.DB) error {
		ticket = entity.Ticket{
			UserID: user.ID, UserName: ticketUserName(user), Subject: subject,
			Status: entity.TicketStatusOpen, Priority: entity.TicketPriorityNormal,
			LastMessageAt: now,
		}
		if err := tx.Create(&ticket).Error; err != nil {
			return err
		}
		message := entity.TicketMessage{
			TicketID: ticket.ID, UserID: user.ID, AuthorName: ticketUserName(user),
			Body: body, AuthorRole: "user", CreatedAt: now,
		}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		if err := tx.Model(&entity.Ticket{}).Where("id = ?", ticket.ID).Updates(map[string]interface{}{
			"last_message_at": now,
			"updated_at":      now,
		}).Error; err != nil {
			return err
		}
		return enqueueTicketNotificationInTx(tx, ticket, body, "new_ticket")
	})
	if err != nil {
		return TicketDetail{}, err
	}
	return GetTicketWithDB(db, user, ticket.ID)
}

func ListTicketsWithDB(db *gorm.DB, user *entity.User, query TicketListQuery, page, pageSize int) (TicketListResult, error) {
	if db == nil || user == nil {
		return TicketListResult{}, ErrTicketForbidden
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > TicketPageSizeMax {
		if pageSize > TicketPageSizeMax {
			pageSize = TicketPageSizeMax
		} else {
			pageSize = TicketPageSizeDefault
		}
	}
	base := db.Model(&entity.Ticket{})
	if !isTicketAdmin(user) {
		base = base.Where("tickets.user_id = ?", user.ID)
	}
	status := strings.TrimSpace(query.Status)
	if status != "" {
		var err error
		status, err = normalizeTicketStatus(status)
		if err != nil {
			return TicketListResult{}, err
		}
		base = base.Where("tickets.status = ?", status)
	}
	if condition, args := ticketKeywordCondition(query.Keyword); condition != "" {
		base = base.Where(condition, args...)
	}
	if query.CreatedFrom != nil {
		base = base.Where("tickets.created_at >= ?", query.CreatedFrom)
	}
	if query.CreatedTo != nil {
		base = base.Where("tickets.created_at < ?", query.CreatedTo)
	}
	if query.UpdatedFrom != nil {
		base = base.Where("tickets.updated_at >= ?", query.UpdatedFrom)
	}
	if query.UpdatedTo != nil {
		base = base.Where("tickets.updated_at < ?", query.UpdatedTo)
	}
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return TicketListResult{}, err
	}
	var tickets []entity.Ticket
	if err := base.Order("tickets.last_message_at DESC, tickets.id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&tickets).Error; err != nil {
		return TicketListResult{}, err
	}
	counts := make(map[int64]int64, len(tickets))
	if len(tickets) > 0 {
		type row struct {
			TicketID int64
			Count    int64
		}
		var rows []row
		if err := db.Model(&entity.TicketMessage{}).Select("ticket_id, COUNT(*) AS count").Where("ticket_id IN ?", ticketIDs(tickets)).Group("ticket_id").Scan(&rows).Error; err != nil {
			return TicketListResult{}, err
		}
		for _, item := range rows {
			counts[item.TicketID] = item.Count
		}
	}
	items := make([]TicketListItem, 0, len(tickets))
	for _, ticket := range tickets {
		items = append(items, TicketListItem{ID: ticket.ID, UserID: ticket.UserID, UserName: ticket.UserName,
			Subject: ticket.Subject, Status: ticket.Status, Priority: ticket.Priority, LastMessageAt: ticket.LastMessageAt,
			ClosedAt: ticket.ClosedAt, CreatedAt: ticket.CreatedAt, UpdatedAt: ticket.UpdatedAt, MessageCount: counts[ticket.ID]})
	}
	return TicketListResult{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func ticketIDs(tickets []entity.Ticket) []int64 {
	ids := make([]int64, 0, len(tickets))
	for _, ticket := range tickets {
		ids = append(ids, ticket.ID)
	}
	return ids
}

func ticketKeywordCondition(keyword string) (string, []interface{}) {
	normalized := util.NormalizeSearchKeyword(keyword)
	if normalized == "" {
		return "", nil
	}
	mainCondition, args := util.BuildInstrCondition(normalized,
		"COALESCE(tickets.subject, '')", "COALESCE(tickets.user_name, '')")
	messageCondition, messageArgs := util.BuildInstrCondition(normalized, "COALESCE(ticket_messages.body, '')", "COALESCE(ticket_messages.author_name, '')")
	args = append(args, messageArgs...)
	parts := []string{mainCondition}
	if messageCondition != "" {
		parts = append(parts, "EXISTS (SELECT 1 FROM ticket_messages WHERE ticket_messages.ticket_id = tickets.id AND "+messageCondition+")")
	}
	userCondition, userArgs := util.BuildInstrCondition(normalized, "COALESCE(users.username, '')")
	if userCondition != "" {
		parts = append(parts, "EXISTS (SELECT 1 FROM users WHERE users.id = tickets.user_id AND "+userCondition+")")
		args = append(args, userArgs...)
	}
	if id, err := strconv.ParseInt(normalized, 10, 64); err == nil && id > 0 {
		parts = append(parts, "tickets.id = ?")
		args = append(args, id)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func GetTicketWithDB(db *gorm.DB, user *entity.User, id int64) (TicketDetail, error) {
	if db == nil || user == nil {
		return TicketDetail{}, ErrTicketForbidden
	}
	if id <= 0 {
		return TicketDetail{}, ErrTicketNotFound
	}
	query := db.Where("id = ?", id)
	if !isTicketAdmin(user) {
		query = query.Where("user_id = ?", user.ID)
	}
	var ticket entity.Ticket
	if err := query.First(&ticket).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TicketDetail{}, ErrTicketNotFound
		}
		return TicketDetail{}, err
	}
	var messages []entity.TicketMessage
	if err := db.Where("ticket_id = ?", ticket.ID).Order("created_at ASC, id ASC").Find(&messages).Error; err != nil {
		return TicketDetail{}, err
	}
	detail := TicketDetail{Ticket: ticket, Messages: messages}
	if !isTicketAdmin(user) {
		return detail, nil
	}
	var notification entity.TicketNotification
	if err := db.Where("ticket_id = ?", ticket.ID).Order("id DESC").First(&notification).Error; err == nil {
		detail.Notification = &TicketNotificationInfo{ID: notification.ID, Status: notification.Status, AttemptCount: notification.AttemptCount, LastError: notification.LastError, SentAt: notification.SentAt}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return TicketDetail{}, err
	}
	return detail, nil
}

func AddTicketMessageWithDB(db *gorm.DB, user *entity.User, id int64, input ReplyTicketInput) (TicketDetail, error) {
	if db == nil || user == nil {
		return TicketDetail{}, ErrTicketForbidden
	}
	body, err := normalizeTicketText(input.Body, TicketBodyMaxLength, ErrTicketBodyRequired)
	if err != nil {
		return TicketDetail{}, err
	}
	var ticket entity.Ticket
	now := time.Now()
	err = db.Transaction(func(tx *gorm.DB) error {
		query := tx.Where("id = ?", id)
		if !isTicketAdmin(user) {
			query = query.Where("user_id = ?", user.ID)
		}
		if err := query.First(&ticket).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTicketNotFound
			}
			return err
		}
		status := entity.TicketStatusPending
		if !isTicketAdmin(user) {
			status = entity.TicketStatusOpen
		}
		if input.Status != nil && isTicketAdmin(user) {
			status, err = normalizeTicketStatus(*input.Status)
			if err != nil {
				return err
			}
		}
		message := entity.TicketMessage{TicketID: ticket.ID, UserID: user.ID, AuthorName: ticketUserName(user), Body: body, AuthorRole: ticketAuthorRole(user), CreatedAt: now}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		updates := map[string]interface{}{"status": status, "last_message_at": now, "updated_at": now}
		if status == entity.TicketStatusClosed {
			updates["closed_at"] = now
		} else {
			updates["closed_at"] = nil
		}
		if err := tx.Model(&entity.Ticket{}).Where("id = ?", ticket.ID).Updates(updates).Error; err != nil {
			return err
		}
		ticket.Status, ticket.LastMessageAt = status, now
		if !isTicketAdmin(user) {
			return enqueueTicketNotificationInTx(tx, ticket, body, "user_reply")
		}
		return nil
	})
	if err != nil {
		return TicketDetail{}, err
	}
	return GetTicketWithDB(db, user, id)
}

func UpdateTicketWithDB(db *gorm.DB, user *entity.User, id int64, input UpdateTicketInput) (TicketDetail, error) {
	if db == nil || user == nil || !isTicketAdmin(user) {
		return TicketDetail{}, ErrTicketForbidden
	}
	var ticket entity.Ticket
	query := db.Where("id = ?", id)
	if err := query.First(&ticket).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TicketDetail{}, ErrTicketNotFound
		}
		return TicketDetail{}, err
	}
	updates := make(map[string]interface{})
	if strings.TrimSpace(input.Status) != "" {
		status, err := normalizeTicketStatus(input.Status)
		if err != nil {
			return TicketDetail{}, err
		}
		updates["status"] = status
		if status == entity.TicketStatusClosed {
			now := time.Now()
			updates["closed_at"] = now
		} else {
			updates["closed_at"] = nil
		}
	}
	if strings.TrimSpace(input.Priority) != "" {
		priority, err := normalizeTicketPriority(input.Priority)
		if err != nil {
			return TicketDetail{}, err
		}
		updates["priority"] = priority
	}
	if len(updates) == 0 {
		return GetTicketWithDB(db, user, id)
	}
	updates["updated_at"] = time.Now()
	if err := db.Model(&entity.Ticket{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return TicketDetail{}, err
	}
	return GetTicketWithDB(db, user, id)
}

func ticketUserName(user *entity.User) string {
	if user == nil {
		return ""
	}
	if name := strings.TrimSpace(user.Nickname); name != "" {
		return name
	}
	return strings.TrimSpace(user.Username)
}

func ticketAuthorRole(user *entity.User) string {
	if isTicketAdmin(user) {
		return "admin"
	}
	return "user"
}

// TicketWecomConfig is read from system_settings by the notification worker.
// The webhook URL is never included in ordinary ticket responses or logs.
type TicketWecomConfig struct {
	Enabled       bool
	WebhookURL    string
	MentionedList []string
}

func GetTicketWecomConfigWithDB(db *gorm.DB) (TicketWecomConfig, error) {
	if db == nil {
		return TicketWecomConfig{}, errors.New("数据库未初始化")
	}
	var settings []entity.SystemSetting
	if err := db.Where(clause.IN{Column: clause.Column{Name: "key"}, Values: []interface{}{TicketWecomEnabledSettingKey, TicketWecomWebhookURLSettingKey, TicketWecomMentionedListSettingKey}}).Find(&settings).Error; err != nil {
		return TicketWecomConfig{}, err
	}
	config := TicketWecomConfig{}
	for _, setting := range settings {
		switch setting.Key {
		case TicketWecomEnabledSettingKey:
			config.Enabled = strings.EqualFold(strings.TrimSpace(setting.Value), "true") || strings.TrimSpace(setting.Value) == "1"
		case TicketWecomWebhookURLSettingKey:
			config.WebhookURL = strings.TrimSpace(setting.Value)
		case TicketWecomMentionedListSettingKey:
			config.MentionedList = normalizeMentionedList(setting.Value)
		}
	}
	return config, nil
}

func normalizeMentionedList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || value == "null" {
		return nil
	}
	var list []string
	if strings.HasPrefix(value, "[") {
		_ = json.Unmarshal([]byte(value), &list)
	} else {
		for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' || r == '，' || r == ';' || r == '；' }) {
			list = append(list, strings.TrimSpace(item))
		}
	}
	seen := make(map[string]struct{}, len(list))
	result := make([]string, 0, len(list))
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" || utf8.RuneCountInString(item) > 128 {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

// Persist the outbox event in the same transaction as its source message. The
// configuration decision is made in the same transaction so a disabled or
// malformed integration never creates an automatically retryable task.
func enqueueTicketNotificationInTx(tx *gorm.DB, ticket entity.Ticket, body, eventType string) error {
	content := buildTicketNotificationContent(ticket, body, eventType)
	now := time.Now()
	config, err := GetTicketWecomConfigWithDB(tx)
	if err != nil {
		return err
	}
	status, reason := ticketNotificationStatusForConfig(config)
	notification := entity.TicketNotification{TicketID: ticket.ID, EventType: eventType, Payload: content, Status: status}
	if status == ticketNotificationPending {
		notification.NextAttemptAt = &now
	} else {
		notification.LastError = reason
	}
	return tx.Create(&notification).Error
}

func QueueTicketNotificationWithDB(db *gorm.DB, ticketID int64, eventType, content string, mentionedList []string) error {
	if db == nil || ticketID <= 0 {
		return ErrTicketNotFound
	}
	mentioned, err := json.Marshal(normalizeMentionedList(strings.Join(mentionedList, ",")))
	if err != nil {
		return err
	}
	now := time.Now()
	config, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		return err
	}
	status, reason := ticketNotificationStatusForConfig(config)
	notification := entity.TicketNotification{TicketID: ticketID, EventType: strings.TrimSpace(eventType), Payload: content, MentionedList: string(mentioned), Status: status}
	if status == ticketNotificationPending {
		notification.NextAttemptAt = &now
	} else {
		notification.LastError = reason
	}
	return db.Create(&notification).Error
}

func RetryTicketNotificationWithDB(db *gorm.DB, notificationID int64) error {
	if db == nil || notificationID <= 0 {
		return gorm.ErrRecordNotFound
	}
	var existing entity.TicketNotification
	if err := db.Select("status").First(&existing, notificationID).Error; err != nil {
		return err
	}
	if existing.Status != ticketNotificationFailed {
		return ErrTicketNotificationNotRetryable
	}
	config, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		return err
	}
	if configErr := validateTicketWecomConfig(config); configErr != nil {
		return configErr
	}
	now := time.Now()
	result := db.Model(&entity.TicketNotification{}).Where("id = ? AND status = ?", notificationID, ticketNotificationFailed).Updates(map[string]interface{}{"status": ticketNotificationPending, "attempt_count": 0, "last_error": "", "next_attempt_at": now, "sent_at": nil})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrTicketNotificationNotRetryable
	}
	return nil
}

// ticketNotificationStatusForConfig is the enqueue policy. It deliberately
// returns skipped for every invalid configuration, including a missing system
// setting, so a later configuration change cannot silently replay old events.
func ticketNotificationStatusForConfig(config TicketWecomConfig) (string, string) {
	if err := validateTicketWecomConfig(config); err != nil {
		return ticketNotificationSkipped, err.Error()
	}
	return ticketNotificationPending, ""
}

func validateTicketWecomConfig(config TicketWecomConfig) error {
	if !config.Enabled || strings.TrimSpace(config.WebhookURL) == "" {
		return ErrTicketWebhookNotConfigured
	}
	if err := validateTicketWecomWebhookURL(config.WebhookURL); err != nil {
		return ErrTicketWebhookInvalid
	}
	return nil
}

func buildTicketNotificationContent(ticket entity.Ticket, body, eventType string) string {
	label := "新工单"
	if eventType == "user_reply" {
		label = "用户追问"
	}
	status := map[string]string{entity.TicketStatusOpen: "待处理", entity.TicketStatusPending: "处理中", entity.TicketStatusResolved: "已解决", entity.TicketStatusClosed: "已关闭"}[ticket.Status]
	if status == "" {
		status = "待处理"
	}
	header := fmt.Sprintf("## %s\n**工单 #%d**\n> **标题：** %s\n> **用户：** %s\n> **状态：** %s", label, ticket.ID, escapeTicketMarkdown(strings.Join(strings.Fields(ticket.Subject), " ")), escapeTicketMarkdown(strings.Join(strings.Fields(ticket.UserName), " ")), status)
	if !ticket.LastMessageAt.IsZero() {
		header += "\n> **时间：** " + ticket.LastMessageAt.Format("2006-01-02 15:04:05")
	}
	return header + "\n\n**内容：**\n" + escapeTicketMarkdown(body)
}

func escapeTicketMarkdown(value string) string {
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "`", "\\`", "[", "\\[", "]", "\\]", "#", "\\#").Replace(html.EscapeString(value))
}

const ticketMarkdownMaxBytes = 4096

func truncateTicketMarkdown(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	const suffix = "\n\n> 内容过长，已截断。"
	return truncateUTF8(value, maxBytes-len(suffix)) + suffix
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

type webhookError struct {
	err       error
	retryable bool
}

func (e *webhookError) Error() string { return e.err.Error() }
func (e *webhookError) Unwrap() error { return e.err }

var ticketWebhookHTTPClient = &http.Client{Timeout: 10 * time.Second}

func SendTicketWebhook(ctx context.Context, config TicketWecomConfig, content string, mentionedList []string) error {
	if strings.TrimSpace(config.WebhookURL) == "" || validateTicketWecomWebhookURL(config.WebhookURL) != nil {
		return &webhookError{err: ErrTicketWebhookInvalid, retryable: false}
	}
	parsed, _ := url.Parse(strings.TrimSpace(config.WebhookURL))
	mentioned := normalizeMentionedList(strings.Join(mentionedList, ","))
	tags := make([]string, 0, len(mentioned))
	for _, member := range mentioned {
		if strings.ContainsAny(member, "<>&\"' \t\r\n") {
			continue
		}
		if member == "@all" {
			member = "all"
		}
		tags = append(tags, "<@"+member+">")
	}
	suffix := ""
	if len(tags) > 0 {
		suffix = "\n\n" + strings.Join(tags, " ")
	}
	// Markdown mentions are part of content. Reserve their bytes before
	// truncating user details so neither UTF-8 characters nor tags are cut.
	if ticketMarkdownMaxBytes-len(suffix) < 128 {
		return &webhookError{err: errors.New("企业微信提醒成员列表过长"), retryable: false}
	}
	markdown := truncateTicketMarkdown(content, ticketMarkdownMaxBytes-len(suffix)) + suffix
	body, err := json.Marshal(map[string]interface{}{"msgtype": "markdown", "markdown": map[string]interface{}{"content": markdown}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), strings.NewReader(string(body)))
	if err != nil {
		return &webhookError{err: err, retryable: false}
	}
	req.Header.Set("Content-Type", "application/json")
	client := *ticketWebhookHTTPClient
	// A Webhook is a credential-bearing endpoint; never forward its request to
	// a redirect target, even when the initial URL passed the host allowlist.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return &webhookError{err: err, retryable: true}
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if readErr != nil {
		return &webhookError{err: fmt.Errorf("读取企业微信响应失败: %w", readErr), retryable: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &webhookError{err: fmt.Errorf("企业微信 Webhook HTTP %d: %s", resp.StatusCode, truncateUTF8(string(responseBody), 500)), retryable: resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500}
	}
	var result struct {
		ErrCode *int   `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return &webhookError{err: fmt.Errorf("企业微信响应格式错误: %w", err), retryable: false}
	}
	if result.ErrCode == nil {
		return &webhookError{err: errors.New("企业微信响应缺少 errcode"), retryable: false}
	}
	if *result.ErrCode != 0 {
		retryable := *result.ErrCode == -1 || *result.ErrCode == 45009
		return &webhookError{err: fmt.Errorf("企业微信返回错误 %d: %s", *result.ErrCode, truncateUTF8(result.ErrMsg, 500)), retryable: retryable}
	}
	return nil
}

func ProcessPendingTicketNotifications(ctx context.Context, db *gorm.DB, now time.Time) (int, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	if ctx.Err() != nil {
		return 0, nil
	}
	config, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		return 0, err
	}
	if err := validateTicketWecomConfig(config); err != nil {
		return 0, nil
	}
	var rows []entity.TicketNotification
	if err := db.Where("status = ? AND attempt_count < ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", ticketNotificationPending, TicketNotificationMaxAttempts, now).Order("id ASC").Limit(20).Find(&rows).Error; err != nil {
		return 0, err
	}
	processed := 0
	var firstError error
	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		processed++
		if err := processTicketNotification(ctx, db, row, now); err != nil {
			logger.SysWarn(fmt.Sprintf("ticket_notification_process_failed notificationId=%d ticketId=%d error=%v", row.ID, row.TicketID, err))
			if firstError == nil {
				firstError = err
			}
		}
	}
	return processed, firstError
}

// RecoverStaleTicketNotifications is intentionally explicit. It is called at
// worker startup and on a fixed recovery interval, rather than on every poll.
func RecoverStaleTicketNotifications(db *gorm.DB, now time.Time) (int64, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	recoveryBefore := now.Add(-2 * time.Minute)
	var affected int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var staleCount int64
		if err := tx.Model(&entity.TicketNotification{}).
			Where("status = ? AND updated_at < ?", ticketNotificationSending, recoveryBefore).
			Count(&staleCount).Error; err != nil {
			return err
		}
		if staleCount == 0 {
			return nil
		}
		result := tx.Model(&entity.TicketNotification{}).
			Where("status = ? AND updated_at < ?", ticketNotificationSending, recoveryBefore).
			Updates(map[string]interface{}{"status": ticketNotificationPending, "next_attempt_at": now, "updated_at": now})
		affected = result.RowsAffected
		return result.Error
	})
	return affected, err
}

// SkipPendingTicketNotifications marks legacy pending rows as skipped after a
// configuration is found invalid. New rows already use this policy at enqueue.
func SkipPendingTicketNotifications(db *gorm.DB, reason string, now time.Time) (int64, error) {
	if db == nil {
		return 0, errors.New("数据库未初始化")
	}
	var affected int64
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		affected, err = skipPendingTicketNotificationsInTx(tx, reason, now)
		return err
	})
	return affected, err
}

func skipPendingTicketNotificationsInTx(tx *gorm.DB, reason string, now time.Time) (int64, error) {
	if tx == nil {
		return 0, errors.New("数据库未初始化")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = ErrTicketWebhookNotConfigured.Error()
	}
	var pendingCount int64
	if err := tx.Model(&entity.TicketNotification{}).Where("status = ?", ticketNotificationPending).Count(&pendingCount).Error; err != nil {
		return 0, err
	}
	if pendingCount == 0 {
		return 0, nil
	}
	result := tx.Model(&entity.TicketNotification{}).
		Where("status = ?", ticketNotificationPending).
		Updates(map[string]interface{}{"status": ticketNotificationSkipped, "last_error": reason, "next_attempt_at": nil, "updated_at": now})
	return result.RowsAffected, result.Error
}

func processTicketNotification(ctx context.Context, db *gorm.DB, row entity.TicketNotification, now time.Time) error {
	if ctx.Err() != nil {
		return nil
	}
	// Revalidate the selected snapshot. Another worker may already have tried
	// and requeued it; status alone would allow a stale worker to bypass backoff.
	now = time.Now()
	claimed := false
	if err := db.Transaction(func(tx *gorm.DB) error {
		claim := tx.Model(&entity.TicketNotification{}).
			Where("id = ? AND status = ? AND attempt_count = ? AND updated_at = ?", row.ID, ticketNotificationPending, row.AttemptCount, row.UpdatedAt).
			Where("next_attempt_at IS NULL OR next_attempt_at <= ?", now).
			Updates(map[string]interface{}{"status": ticketNotificationSending, "updated_at": now})
		if claim.Error != nil {
			return claim.Error
		}
		claimed = claim.RowsAffected > 0
		return nil
	}); err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	if ctx.Err() != nil {
		return restoreCanceledTicketNotification(db, row.ID, now)
	}
	config, err := GetTicketWecomConfigWithDB(db)
	if err != nil {
		if requeueErr := db.Model(&entity.TicketNotification{}).Where("id = ? AND status = ?", row.ID, ticketNotificationSending).
			Updates(map[string]interface{}{"status": ticketNotificationPending, "last_error": "读取企业微信配置失败", "next_attempt_at": now.Add(time.Second), "updated_at": now}).Error; requeueErr != nil {
			return requeueErr
		}
		return err
	}
	if configErr := validateTicketWecomConfig(config); configErr != nil {
		return db.Model(&entity.TicketNotification{}).Where("id = ? AND status = ?", row.ID, ticketNotificationSending).
			Updates(map[string]interface{}{"status": ticketNotificationSkipped, "last_error": configErr.Error(), "next_attempt_at": nil, "updated_at": now}).Error
	}
	mentioned := normalizeMentionedList(row.MentionedList)
	if strings.TrimSpace(row.MentionedList) == "" {
		mentioned = config.MentionedList
	}
	err = SendTicketWebhook(ctx, config, row.Payload, mentioned)
	now = time.Now()
	if err != nil && ctx.Err() != nil {
		return restoreCanceledTicketNotification(db, row.ID, now)
	}
	attempts := row.AttemptCount + 1
	if err == nil {
		return db.Model(&entity.TicketNotification{}).Where("id = ?", row.ID).Updates(map[string]interface{}{"status": ticketNotificationSent, "attempt_count": attempts, "last_error": "", "sent_at": now, "next_attempt_at": nil, "updated_at": now}).Error
	}
	var werr *webhookError
	retryable := errors.As(err, &werr) && werr.retryable
	redactedError := truncateUTF8(redactTicketWebhookError(err.Error(), config.WebhookURL), 1000)
	logger.SysWarn(fmt.Sprintf("ticket_notification_delivery_failed notificationId=%d ticketId=%d attempt=%d retryable=%t error=%s", row.ID, row.TicketID, attempts, retryable, redactedError))
	updates := map[string]interface{}{"attempt_count": attempts, "last_error": redactedError, "updated_at": now}
	if retryable && attempts < TicketNotificationMaxAttempts {
		next := now.Add(time.Duration(attempts) * time.Minute)
		updates["status"] = ticketNotificationPending
		updates["next_attempt_at"] = next
	} else {
		updates["status"] = ticketNotificationFailed
		updates["next_attempt_at"] = nil
	}
	return db.Model(&entity.TicketNotification{}).Where("id = ?", row.ID).Updates(updates).Error
}

func restoreCanceledTicketNotification(db *gorm.DB, id int64, now time.Time) error {
	return db.Model(&entity.TicketNotification{}).Where("id = ? AND status = ?", id, ticketNotificationSending).
		Updates(map[string]interface{}{"status": ticketNotificationPending, "next_attempt_at": now, "updated_at": now}).Error
}

func redactTicketWebhookError(message, webhookURL string) string {
	message = strings.TrimSpace(message)
	if webhookURL != "" {
		message = strings.ReplaceAll(message, webhookURL, "[Webhook已隐藏]")
		if parsed, err := url.Parse(webhookURL); err == nil {
			if parsed.Host != "" {
				message = strings.ReplaceAll(message, parsed.Host, "[Webhook主机已隐藏]")
			}
			for _, key := range parsed.Query()["key"] {
				if key == "" {
					continue
				}
				message = strings.ReplaceAll(message, key, "[密钥已隐藏]")
				message = strings.ReplaceAll(message, url.QueryEscape(key), "[密钥已隐藏]")
			}
		}
	}
	return message
}

func StartTicketNotificationWorker(ctx context.Context, db *gorm.DB) <-chan struct{} {
	done := make(chan struct{})
	if db == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		if err := RunTicketNotificationWorker(ctx, db); err != nil && ctx.Err() == nil {
			logger.SysWarn(fmt.Sprintf("ticket_notification_worker_failed error=%v", err))
		}
	}()
	return done
}

const (
	ticketNotificationPollInterval     = 5 * time.Second
	ticketNotificationInvalidInterval  = 30 * time.Second
	ticketNotificationRecoveryInterval = 5 * time.Minute
)

func waitTicketNotificationWorker(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// RunTicketNotificationWorker runs the outbox loop for the background worker
// goroutine. Database operations are short and the webhook call is always made
// after the claim transaction has committed.
func RunTicketNotificationWorker(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return errors.New("数据库未初始化")
	}
	lastInvalidConfig := ""
	backoff := time.Second
	nextRecovery := time.Time{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		now := time.Now()
		config, err := GetTicketWecomConfigWithDB(db)
		if err != nil {
			logger.SysWarn(fmt.Sprintf("ticket_notification_config_read_failed error=%v", err))
			if !waitTicketNotificationWorker(ctx, backoff) {
				return nil
			}
			if backoff < time.Minute {
				backoff *= 2
				if backoff > time.Minute {
					backoff = time.Minute
				}
			}
			continue
		}
		configErr := validateTicketWecomConfig(config)
		if configErr != nil {
			state := fmt.Sprintf("%t|%s|%s", config.Enabled, config.WebhookURL, configErr.Error())
			if state != lastInvalidConfig {
				if _, skipErr := SkipPendingTicketNotifications(db, configErr.Error(), now); skipErr != nil {
					logger.SysWarn(fmt.Sprintf("ticket_notification_skip_invalid_config_failed error=%v", skipErr))
					if !waitTicketNotificationWorker(ctx, backoff) {
						return nil
					}
					if backoff < time.Minute {
						backoff *= 2
						if backoff > time.Minute {
							backoff = time.Minute
						}
					}
					continue
				}
				lastInvalidConfig = state
			}
			backoff = time.Second
			if !waitTicketNotificationWorker(ctx, ticketNotificationInvalidInterval) {
				return nil
			}
			continue
		}
		if nextRecovery.IsZero() || !now.Before(nextRecovery) {
			if _, err := RecoverStaleTicketNotifications(db, now); err != nil {
				logger.SysWarn(fmt.Sprintf("ticket_notification_recovery_failed error=%v", err))
				if !waitTicketNotificationWorker(ctx, backoff) {
					return nil
				}
				if backoff < time.Minute {
					backoff *= 2
					if backoff > time.Minute {
						backoff = time.Minute
					}
				}
				continue
			}
			nextRecovery = now.Add(ticketNotificationRecoveryInterval)
		}
		lastInvalidConfig = ""
		backoff = time.Second
		if _, err := ProcessPendingTicketNotifications(ctx, db, now); err != nil {
			logger.SysWarn(fmt.Sprintf("ticket_notification_poll_failed error=%v", err))
			if !waitTicketNotificationWorker(ctx, backoff) {
				return nil
			}
			if backoff < time.Minute {
				backoff *= 2
				if backoff > time.Minute {
					backoff = time.Minute
				}
			}
			continue
		}
		if !waitTicketNotificationWorker(ctx, ticketNotificationPollInterval) {
			return nil
		}
	}
}
