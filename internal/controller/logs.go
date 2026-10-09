package controller

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
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
		Description: strings.TrimSpace(description),
		ClientIP:    strings.TrimSpace(clientIP),
		Qid:         qid,
		OldAnswer:   oldAnswer,
		NewAnswer:   newAnswer,
		CreateTime:  time.Now(),
	}
	if user != nil {
		entry.UserID = user.ID
		entry.Username = strings.TrimSpace(user.Username)
	}
	if entry.Username == "" {
		entry.Username = "anonymous"
	}
	_ = dao.Log.Create(&entry)
}

// LogList - 日志列表
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

	query := dao.Log.UnderlyingDB().Model(&entity.Log{})
	if !isAdminUser(currentUser) {
		query = query.Where("user_id = ?", currentUser.ID)
	}
	if module != "" {
		query = query.Where("module = ?", module)
	}
	if actionValue != "" {
		action, parseErr := strconv.Atoi(actionValue)
		if parseErr == nil {
			query = query.Where("action = ?", action)
		}
	}

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

	c.JSON(200, gin.H{
		"data":     find,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	})
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
	if err := dao.User.UnderlyingDB().Where("id IN ?", userIDs).Find(&users).Error; err != nil {
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
