package controller

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 敏感原文的**单独读取接口**。
//
// 需求原文：「所有页面都默认打标，只是管理员有按钮可以显示原文」。
// 默认形态（列表）对非管理员只下发遮挡文本；原文必须走这条路，
// 路由上挂 controller.Auth + middleware.RequireAdmin，双保险：
//   - Auth：没有会话直接 401；
//   - RequireAdmin：普通用户 403，不会因为「知道 logId」就拿到别人的原文。
//
// 为什么不是「列表里给个 query 参数切原文」：
//   - 参数型开关极易被前端状态残留带进默认路径（一次 ?original=1 之后
//     所有请求都可能带上它），接口级权限才是硬边界；
//   - 原文按需取，列表响应不必为「管理员可能想看」多付一份大字段带宽。

// sensitiveOriginalTimeLayout 是原文接口的检测时间格式（与前端 Intl 展示一致）。
const sensitiveOriginalTimeLayout = "2006-01-02T15:04:05Z07:00"

// logSensitiveOriginalResponse 是原文接口的响应体。
//
// 同时返回遮挡形态：让调用方（含 PLUS 的 chaoxing 面板）在同一响应里拿到
// 「默认该显示什么」与「展开后显示什么」，不必自己再遮挡一遍，
// 避免两端实现漂移（AGENTS.md 第 13 条）。
type logSensitiveOriginalResponse struct {
	ID int32 `json:"id"`
	// SensitiveFlag 该行是否命中敏感词；false 时其余字段与普通日志读取一致。
	SensitiveFlag bool `json:"sensitiveFlag"`
	// SensitiveWords 命中明细（分组 JSON 文本），用于「命中词」提示。
	SensitiveWords string `json:"sensitiveWords,omitempty"`
	// SensitiveCheckedAt 最近一次检测时间；为空表示从未检测。
	SensitiveCheckedAt *string `json:"sensitiveCheckedAt,omitempty"`
	// OriginalDescription / OriginalOldAnswer / OriginalNewAnswer 是**原文**。
	OriginalDescription string `json:"originalDescription"`
	OriginalOldAnswer   string `json:"originalOldAnswer"`
	OriginalNewAnswer   string `json:"originalNewAnswer"`
	// Masked 是同一份内容在「默认遮挡」下的形态（不含原文任何字符）。
	MaskedDescription string `json:"maskedDescription"`
	MaskedOldAnswer   string `json:"maskedOldAnswer"`
	MaskedNewAnswer   string `json:"maskedNewAnswer"`
}

// LogSensitiveOriginal 返回单条日志的敏感原文（仅管理员）。
//
// 权限：路由层 RequireAdmin 已拦一次，这里再判一次是纵深防御 ——
// 任何把本 handler 挂到别的路由组上的改动都不会意外放开原文。
func LogSensitiveOriginal(c *gin.Context) {
	value, exists := c.Get("user")
	currentUser, ok := value.(*entity.User)
	if !exists || !ok || currentUser == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"message": "请先登录"})
		return
	}
	if !isAdminUser(currentUser) {
		c.JSON(http.StatusForbidden, gin.H{"message": "无权限执行该操作"})
		return
	}

	logID, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 32)
	if err != nil || logID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "日志编号无效"})
		return
	}

	readDB := withDatabasePriority(dao.Log.UnderlyingDB(), databaseRequestContext(c), dbqueue.PriorityLow)
	if readDB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"message": "数据库不可用"})
		return
	}

	var entry entity.Log
	// Session(NewDB)：不复用调用方可能残留的条件，避免一次误带 Where 读到别的行。
	if err := readDB.Session(&gorm.Session{NewDB: true}).Where("id = ?", logID).Take(&entry).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "日志不存在"})
		return
	}

	response := logSensitiveOriginalResponse{
		ID:                  entry.ID,
		OriginalDescription: entry.Description,
		OriginalOldAnswer:   entry.OldAnswer,
		OriginalNewAnswer:   entry.NewAnswer,
		// 默认遮挡：没有命中明细时整段遮挡（宁可多遮，不可漏遮）。
		MaskedDescription: maskOrBlank(entry.Description, nil),
		MaskedOldAnswer:   maskOrBlank(entry.OldAnswer, nil),
		MaskedNewAnswer:   maskOrBlank(entry.NewAnswer, nil),
	}

	sensitive, found := loadSensitiveWordFieldsForLog(readDB, entry.ID)
	if found {
		response.SensitiveFlag = sensitive.Flag
		response.SensitiveWords = sensitive.Words
		if sensitive.CheckedAt != nil {
			formatted := sensitive.CheckedAt.In(logger.ShanghaiLocation()).Format(sensitiveOriginalTimeLayout)
			response.SensitiveCheckedAt = &formatted
		}
		if sensitive.Flag {
			// 命中行：遮挡形态按命中词精确替换，与列表接口完全同源。
			words := service.SensitiveWordListFromScopes(service.ParseSensitiveWordDetailJSON(sensitive.Words))
			response.MaskedDescription = maskOrBlank(entry.Description, words)
			response.MaskedOldAnswer = maskOrBlank(entry.OldAnswer, words)
			response.MaskedNewAnswer = maskOrBlank(entry.NewAnswer, words)
		}
	}

	c.JSON(http.StatusOK, response)
}

// sensitiveWordFields 是「一行日志的敏感标记」在控制器内的读取视图。
type sensitiveWordFields struct {
	Flag      bool
	Words     string
	CheckedAt *time.Time
	Error     string
}

// loadSensitiveWordFieldsForLog 按主键读一行的敏感标记；不存在返回 false。
//
// 失败静默为「未检测」：标记读取只是展示增强，不能让原文接口整体报错。
func loadSensitiveWordFieldsForLog(db *gorm.DB, logID int32) (sensitiveWordFields, bool) {
	if db == nil || logID <= 0 {
		return sensitiveWordFields{}, false
	}
	var row entity.LogSensitive
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", logID).Take(&row).Error; err != nil {
		return sensitiveWordFields{}, false
	}
	if row.CheckedAt == nil {
		// 未检测过：标记字段一律不下发，与列表接口保持一致。
		return sensitiveWordFields{}, false
	}
	return sensitiveWordFields{
		Flag:      row.Flag,
		Words:     row.Words,
		CheckedAt: row.CheckedAt,
		Error:     row.CheckError,
	}, true
}
