package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const (
	logActionCreate     = 0
	logActionUpdate     = 1
	logActionDelete     = 2
	logActionSearch     = 3
	logActionLogin      = 10
	logActionAIFallback = 11
)

func writeAuditLog(user *entity.User, module string, action int32, description string, clientIP string, qid int32, oldAnswer string, newAnswer string) {
	if dao.Log == nil {
		return
	}

	entry := entity.Log{
		UserID:      0,
		Username:    "anonymous",
		Module:      strings.TrimSpace(module),
		Action:      action,
		Description: truncateAuditDescription(strings.TrimSpace(description)),
		ClientIP:    strings.TrimSpace(clientIP),
		Qid:         qid,
		OldAnswer:   oldAnswer,
		NewAnswer:   newAnswer,
		CreateTime:  time.Now().In(logger.ShanghaiLocation()),
	}
	if user != nil {
		entry.UserID = user.ID
		entry.Username = strings.TrimSpace(user.Username)
	}
	if entry.Username == "" {
		entry.Username = "anonymous"
	}
	if err := dao.Log.Create(&entry); err != nil {
		logger.SysError(fmt.Sprintf("审计日志写入失败 module=%s action=%d userId=%d error=%s", entry.Module, entry.Action, entry.UserID, logger.RedactSecrets(err.Error())))
	}
}

func truncateAuditDescription(value string) string {
	const limit = 240
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit-3]) + "..."
	}
	return value
}

// LogList - 日志列表
//
// 支持按关键词搜索（说明 / 用户名 / 客户端 IP / 写入者）以及模块、操作类型筛选。
// 非管理员只能看到自己的日志。
func LogList(c *gin.Context) {
	user, _ := c.Get("user")
	currentUser := user.(*entity.User)

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}
	offset := (page - 1) * pageSize
	module := strings.TrimSpace(c.Query("module"))
	actionValue := strings.TrimSpace(c.Query("action"))
	keyword := util.NormalizeSearchKeyword(c.Query("keyword"))
	createdFrom, createdFromErr := parseListFilterTime(c.Query("createdFrom"), false)
	createdTo, createdToErr := parseListFilterTime(c.Query("createdTo"), true)
	userID, userIDErr := parseOptionalPositiveInt32(c.Query("userId"))
	qid, qidErr := parseOptionalPositiveInt32(c.Query("qid"))
	if createdFromErr != nil || createdToErr != nil || userIDErr != nil || qidErr != nil {
		c.JSON(400, gin.H{"message": "筛选参数格式无效"})
		return
	}
	if err := validateListFilterTimeRange(createdFrom, createdTo, "创建时间"); err != nil {
		c.JSON(400, gin.H{"message": err.Error()})
		return
	}
	clientIP := strings.TrimSpace(c.Query("clientIp"))

	readDB := withDatabasePriority(dao.Log.UnderlyingDB(), databaseRequestContext(c), dbqueue.PriorityLow)
	query := buildLogListQueryWithFilter(readDB.Model(&entity.Log{}), currentUser, logListFilter{
		Module: module, Action: actionValue, Keyword: keyword, ClientIP: clientIP,
		UserID: userID, QID: qid, CreatedFrom: createdFrom, CreatedTo: createdTo,
	})

	var (
		total int64
		find  []*entity.Log
		err   error
	)

	err = query.Count(&total).Error
	if err == nil {
		err = query.Order("id DESC").Limit(pageSize).Offset(offset).Find(&find).Error
	}

	if err != nil {
		c.JSON(400, gin.H{
			"message": "查询失败",
		})
		return
	}
	resolveLogUsernames(find)
	items, billingMeta, err := logListItemsWithBillingIDs(readDB, currentUser, find)
	if err != nil {
		c.JSON(400, gin.H{"message": "查询失败"})
		return
	}
	fillLogProjects(items, billingMeta)

	c.JSON(200, gin.H{
		"data":     items,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
}

type logListItem struct {
	*entity.Log
	BillingIDs []int32 `json:"billingIds"`
	// Project 是读取时派生的「项目」列（方案 B：不落库、不加列）。
	// 永不为空字符串，兜底 "-"，口径见 buildLogProject。
	Project string `json:"project"`
}

type logBillingKey struct {
	userID    int32
	requestID string
}

// logListItemsWithBillingIDs 一次性把整页日志与账单关联起来。
//
// 性能契约（AGENTS.md 第 18b 条）：一页只查一次 billing_records，绝不逐行查。
// 返回的 billingMeta 供「项目」列派生使用，来自**同一次**查询（只多读四列），
// 因此新增「项目」列不会增加任何查询次数。
func logListItemsWithBillingIDs(db *gorm.DB, currentUser *entity.User, logs []*entity.Log) ([]logListItem, map[int32]logBillingProjectMeta, error) {
	items := make([]logListItem, len(logs))
	billingMeta := make(map[int32]logBillingProjectMeta)
	keys := make([]logBillingKey, len(logs))
	userIDs := make([]int32, 0, len(logs))
	requestIDs := make([]string, 0, len(logs))
	seenUsers := make(map[int32]struct{}, len(logs))
	seenRequests := make(map[string]struct{}, len(logs))
	for i, entry := range logs {
		items[i] = logListItem{Log: entry, BillingIDs: []int32{}}
		if entry == nil || entry.UserID <= 0 {
			continue
		}
		requestID := logBillingRequestID(entry)
		if requestID == "" {
			continue
		}
		keys[i] = logBillingKey{userID: entry.UserID, requestID: requestID}
		if _, exists := seenUsers[entry.UserID]; !exists {
			seenUsers[entry.UserID] = struct{}{}
			userIDs = append(userIDs, entry.UserID)
		}
		if _, exists := seenRequests[requestID]; !exists {
			seenRequests[requestID] = struct{}{}
			requestIDs = append(requestIDs, requestID)
		}
	}
	if len(requestIDs) == 0 {
		return items, billingMeta, nil
	}

	var bills []struct {
		ID           int32
		UserID       int32
		RequestID    string
		Type         string
		QuestionText string
		Remark       string
	}
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.BillingRecord{}).
		Select("id, user_id, request_id, type, question_text, remark").
		Where("user_id IN ? AND request_id IN ?", userIDs, requestIDs)
	if !isAdminUser(currentUser) {
		query = query.Where("user_id = ? AND hidden_from_statement = ?", currentUser.ID, false)
	}
	if err := query.Order("id ASC").Find(&bills).Error; err != nil {
		return nil, nil, err
	}

	billingIDs := make(map[logBillingKey][]int32, len(bills))
	for _, bill := range bills {
		key := logBillingKey{userID: bill.UserID, requestID: bill.RequestID}
		billingIDs[key] = append(billingIDs[key], bill.ID)
		billingMeta[bill.ID] = logBillingProjectMeta{
			Type:         bill.Type,
			QuestionText: bill.QuestionText,
			Remark:       bill.Remark,
		}
	}
	for i, key := range keys {
		if ids := billingIDs[key]; len(ids) > 0 {
			items[i].BillingIDs = ids
		}
	}
	return items, billingMeta, nil
}

func logRequestID(description string) string {
	for _, field := range strings.Fields(description) {
		if requestID, ok := strings.CutPrefix(field, "requestId="); ok {
			return requestID
		}
	}
	return ""
}

func logBillingRequestID(entry *entity.Log) string {
	if requestID := logRequestID(entry.Description); requestID != "" {
		return requestID
	}
	for _, raw := range []string{entry.NewAnswer, entry.OldAnswer} {
		var snapshot struct {
			RequestID string `json:"requestId"`
			OrderNo   string `json:"orderNo"`
		}
		if json.Unmarshal([]byte(raw), &snapshot) == nil {
			if requestID := strings.TrimSpace(snapshot.RequestID); requestID != "" {
				return requestID
			}
			if entry.Module == "recharge" && strings.TrimSpace(snapshot.OrderNo) != "" {
				return strings.TrimSpace(snapshot.OrderNo)
			}
		}
	}
	if entry.Module == "recharge" {
		return strings.TrimSpace(entry.CorrelationID)
	}
	return ""
}

// buildLogListQuery 组装日志查询条件：先按权限收窄，再叠加筛选。
// 抽成独立函数便于单测覆盖关键词转义与筛选组合。
func buildLogListQuery(query *gorm.DB, currentUser *entity.User, module string, actionValue string, keyword string) *gorm.DB {
	return buildLogListQueryWithFilter(query, currentUser, logListFilter{Module: module, Action: actionValue, Keyword: keyword})
}

type logListFilter struct {
	Module      string
	Action      string
	Keyword     string
	ClientIP    string
	UserID      int32
	QID         int32
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

func buildLogListQueryWithFilter(query *gorm.DB, currentUser *entity.User, filter logListFilter) *gorm.DB {
	if !isAdminUser(currentUser) {
		query = query.Where("user_id = ?", currentUser.ID)
	}
	if strings.TrimSpace(filter.Module) != "" {
		query = query.Where("module = ?", filter.Module)
	}
	if filter.Action != "" {
		if action, parseErr := strconv.Atoi(filter.Action); parseErr == nil {
			query = query.Where("action = ?", action)
		}
	}
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.QID > 0 {
		query = query.Where("qid = ?", filter.QID)
	}
	if filter.ClientIP != "" {
		query = query.Where("client_ip = ?", filter.ClientIP)
	}
	if filter.CreatedFrom != nil {
		query = query.Where("create_time >= ?", filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		query = query.Where("create_time < ?", filter.CreatedTo)
	}

	// 关键词按「纯子串」匹配（不使用 LIKE 通配符），见 util.BuildInstrCondition。
	condition, args := util.BuildInstrCondition(filter.Keyword,
		"COALESCE(description, '')",
		"COALESCE(username, '')",
		"COALESCE(client_ip, '')",
		"COALESCE(module, '')",
	)
	if condition == "" {
		return query
	}
	return query.Where(condition, args...)
}

func resolveLogUsernames(logs []*entity.Log) {
	userIDs := make([]int32, 0, len(logs))
	seen := make(map[int32]struct{}, len(logs))
	for _, entry := range logs {
		if entry == nil || entry.UserID <= 0 || strings.TrimSpace(entry.Username) != "" {
			continue
		}
		if _, exists := seen[entry.UserID]; exists {
			continue
		}
		seen[entry.UserID] = struct{}{}
		userIDs = append(userIDs, entry.UserID)
	}
	if len(userIDs) == 0 {
		return
	}

	var users []entity.User
	if err := withDatabasePriority(dao.User.UnderlyingDB(), context.Background(), dbqueue.PriorityLow).Where("id IN ?", userIDs).Find(&users).Error; err != nil {
		return
	}
	usernameByID := make(map[int32]string, len(users))
	for _, user := range users {
		usernameByID[user.ID] = strings.TrimSpace(user.Username)
	}
	for _, entry := range logs {
		if entry == nil || strings.TrimSpace(entry.Username) != "" {
			continue
		}
		if username := usernameByID[entry.UserID]; username != "" {
			entry.Username = username
		}
	}
}
