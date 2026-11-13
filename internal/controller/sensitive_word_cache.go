package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 敏感词检测记录管理接口（123 号 T-27）。
//
// 为什么需要这个文件：`internal/service/sensitive_words.go` 的缓存读写链路
// 已经完整在用（hash 键 / 命中即返回 / OnConflict upsert / 失败上限 3 次），
// 但 `internal/controller/sensitive_words.go` 里**一个 List 类 handler 都没有**，
// 所以管理端根本看不到这些记录。这里补的就是「列表 + 三态筛选」。
//
// 本文件只读：不写、不清空、不重试缓存行。
// 「同一文本二次检测不产生上游调用」由 service 层保证，不在这里重做。

// resolveSearchPersistenceDBForSensitiveWordCache 是 DB 句柄的间接层。
//
// 生产上它就是 resolveSearchPersistenceDB（dao.Log / manager）；
// 单独抽成变量是为了让测试能注入合成库，而不必伪造全局 manager 状态。
// 与 ai_ocr_cache.go 里 `var aiOCRCacheDatabase = func(...)` 是同一手法。
var resolveSearchPersistenceDBForSensitiveWordCache = func() *gorm.DB {
	return resolveSearchPersistenceDB()
}

// sensitiveWordCacheDatabase 返回一个**低优先级**的只读会话。
//
// 与 ai_ocr_cache.go 的 aiOCRCacheDatabase 同源：管理端列表是旁路查询，
// 不能和答题/计费抢数据库准入名额。
func sensitiveWordCacheDatabase(c *gin.Context) *gorm.DB {
	return withDatabasePriority(resolveSearchPersistenceDBForSensitiveWordCache(), databaseRequestContext(c), dbqueue.PriorityLow)
}

// sensitiveWordCacheListQuery 解析分页与筛选参数。
//
// 分页规则与 ai_ocr_cache.go 的 aiOCRCacheListQuery 一致（page ≥ 1、
// 1 ≤ pageSize ≤ 100、并且防 `(page-1)*pageSize` 溢出）。
func sensitiveWordCacheListQuery(c *gin.Context) (service.SensitiveWordCacheListQuery, int, int, error) {
	filters := service.SensitiveWordCacheListQuery{
		Keyword:     c.Query("keyword"),
		State:       c.Query("state"),
		CheckedFrom: c.Query("checkedFrom"),
		CheckedTo:   c.Query("checkedTo"),
	}
	page, pageErr := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, pageSizeErr := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if pageErr != nil || pageSizeErr != nil || page < 1 || pageSize < 1 || pageSize > 100 ||
		page-1 > int(^uint(0)>>1)/pageSize {
		return filters, 0, 0, errors.New("分页参数格式无效")
	}
	return filters, page, pageSize, nil
}

// SensitiveWordCacheList 返回敏感词检测记录列表。
//
//	GET /adapter-service/admin/sensitive-word-cache?state=hit|clean|failed&page=1&pageSize=20
//
// 判据 ①：能按「命中 / 未命中 / 失败」三态筛选（state 参数）。
// 判据 ②：`MaskedText` 可看、**原文不可看** —— 缓存表按设计不存原文
// （entity/sensitive_word_cache.go:9-11），所以这不是前端隐藏，
// 而是后端根本没有那个字段可下发（test 里有 JSON 级断言）。
func SensitiveWordCacheList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	filters, page, pageSize, err := sensitiveWordCacheListQuery(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	result, err := service.ListSensitiveWordCacheWithDB(sensitiveWordCacheDatabase(c), filters, page, pageSize)
	if err != nil {
		writeSensitiveWordCacheFailure(c, err, "获取敏感词检测记录失败")
		return
	}
	c.JSON(http.StatusOK, result)
}

// SensitiveWordCacheStatus 返回三态计数与最近一次检测时间。
//
//	GET /adapter-service/admin/sensitive-word-cache/status
func SensitiveWordCacheStatus(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	counts, latest, err := service.SensitiveWordCacheStatusWithDB(sensitiveWordCacheDatabase(c))
	if err != nil {
		writeSensitiveWordCacheFailure(c, err, "获取敏感词检测记录状态失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"states": counts, "latestCheckedAt": latest})
}

// writeSensitiveWordCacheFailure 把 service 层错误映射成 HTTP 响应。
//
// 筛选参数非法返回 400（管理员能自己改对），其余 500（系统故障）。
// 不能把 400 混成 500 —— 那会让管理员以为系统坏了而不去改筛选条件。
func writeSensitiveWordCacheFailure(c *gin.Context, err error, fallback string) {
	if errors.Is(err, service.ErrSensitiveWordCacheFilterInvalid) {
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"message": fallback})
}
