package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// IP 段归属信息管理页的五条管理员接口（task_doc/71 §3.5）。
//
// 本文件只做 HTTP 适配：鉴权、参数解析、错误分类、响应包装。
// 业务读写在 service/ip_segment_admin.go（列表/按 IP 定位/删除/批量删除/刷新），
// 段命中的查询链路在 service/ip_segment.go —— 这两个文件属于 PR #4 的范围，
// 本任务不改其逻辑，只在其上补 controller 与路由。
//
// 五条接口一律 Auth + RequireAdmin，且 handler 内先跑 requireAdmin：
// 路由层挡住未登录者，handler 层再挡一次「已登录但非管理员」——
// 两层都有才符合本仓其它管理接口的口径（见 controller/referral.go 的写法）。

// 便于测试替换（与 uapi.go 的 refreshLocationCache 等注入风格一致）。
//
// 其中 refreshIPSegment 一定会调上游，因此单测必须替换它才能有确定性的 200 路径；
// 其余四个直接打真实库。
var (
	listIPSegments        = service.ListIPSegmentsWithDB
	lookupIPSegment       = service.LookupIPSegmentWithDB
	deleteIPSegment       = service.DeleteIPSegmentWithDB
	deleteIPSegmentsBatch = service.DeleteIPSegmentsBatchWithDB
	refreshIPSegment      = service.RefreshIPSegmentWithDB
)

// ipSegmentDB 返回管理面使用的数据库句柄（低优先级会话）。
//
// 与 uapi.go 一致：manager / dao 未初始化时返回 nil，由 service 层给出
// 「数据库未初始化」而不是在控制器里空指针崩溃。
func ipSegmentDB(c *gin.Context) *gorm.DB {
	return withDatabasePriority(resolveSearchPersistenceDB(), databaseRequestContext(c), dbqueue.PriorityLow)
}

// IPSegmentList 分页列出 IP 段（支持关键字 / ASN / LLC / 来源筛选）。
//
//	GET /adapter-service/admin/ip-segments?page=1&pageSize=20&keyword=广东&asn=AS9808
//
// 响应直接复用 service.IPSegmentListResult（items / total / page / pageSize / summary），
// summary 是跨全量筛选范围的汇总，不随翻页变化。
func IPSegmentList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	result, err := listIPSegments(ipSegmentDB(c), service.IPSegmentListQuery{
		Keyword: c.Query("keyword"),
		ASN:     c.Query("asn"),
		LLC:     c.Query("llc"),
		Source:  c.Query("source"),
	}, page, pageSize)
	if err != nil {
		writeIPSegmentFailure(c, err, "获取 IP 段列表失败")
		return
	}
	c.JSON(http.StatusOK, result)
}

// IPSegmentLookup 回答「某个 IP 落在哪一段」。
//
//	GET /adapter-service/admin/ip-segments/lookup?ip=120.239.173.110
//
// 未命中**不是错误**：service 用 matched=false + reason 说明原因
// （空输入 / IPv6 / 该段尚未落库），这里照原样 200 下发，
// 管理页据此提示「为什么查不到」而不是笼统报错。
func IPSegmentLookup(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	result, err := lookupIPSegment(ipSegmentDB(c), c.Query("ip"))
	if err != nil {
		writeIPSegmentFailure(c, err, "查询 IP 段归属失败")
		return
	}
	c.JSON(http.StatusOK, result)
}

// IPSegmentDelete 删除一条段记录。
//
//	DELETE /adapter-service/admin/ip-segments/:id
//
// 只删段级缓存，不动 location_cache_entries 的单 IP 缓存 —— 删掉后该段的 IP
// 会重新走上游查询，这是管理员刷新段数据的正常手段（见 service 层注释）。
func IPSegmentDelete(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	id, ok := parseIPSegmentID(c)
	if !ok {
		return
	}
	if err := deleteIPSegment(ipSegmentDB(c), id); err != nil {
		writeIPSegmentFailure(c, err, "删除 IP 段失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// IPSegmentBatchDelete 按筛选条件批量删除。
//
//	POST /adapter-service/admin/ip-segments/batch-delete
//	{"keyword":"广东","asn":"AS9808","llc":"移动","source":"standard"}
//
// **必须至少带一个筛选条件**：无条件的全删属于清库操作，不该由本接口承担。
// service 层也会拒绝，但这里先挡一次，好处是能给明确文案且不把空条件传下去。
func IPSegmentBatchDelete(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var input struct {
		Keyword string `json:"keyword"`
		ASN     string `json:"asn"`
		LLC     string `json:"llc"`
		Source  string `json:"source"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "批量删除参数错误"})
		return
	}
	filter := service.IPSegmentListQuery{
		Keyword: input.Keyword,
		ASN:     input.ASN,
		LLC:     input.LLC,
		Source:  input.Source,
	}
	if strings.TrimSpace(filter.Keyword) == "" && strings.TrimSpace(filter.ASN) == "" &&
		strings.TrimSpace(filter.LLC) == "" && strings.TrimSpace(filter.Source) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "批量删除必须至少指定一个筛选条件"})
		return
	}
	deleted, err := deleteIPSegmentsBatch(ipSegmentDB(c), filter)
	if err != nil {
		writeIPSegmentFailure(c, err, "批量删除 IP 段失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}

// IPSegmentRefresh 用该段的样本 IP 重查一次上游并更新段信息。
//
//	POST /adapter-service/admin/ip-segments/:id/refresh
//
// service 层在上游不再返回段信息时**不删除旧行**（只更新已有字段）：
// 自动删除会让「一次上游抖动」变成「段数据凭空消失」，删除必须由管理员显式操作。
//
// configured 用于区分「没配密钥」与「配了但上游失败」，与 uapi.go 的口径一致。
func IPSegmentRefresh(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	id, ok := parseIPSegmentID(c)
	if !ok {
		return
	}
	apiKey := service.GetUAPIAPIKeyWithDB(resolveSearchPersistenceDB())
	updated, err := refreshIPSegment(ipSegmentDB(c), id, service.LocationQueryOptions{UAPIKey: apiKey})
	if err != nil {
		c.JSON(classifyIPSegmentError(err), gin.H{
			"message":    service.LocationQueryErrorMessage(err),
			"configured": apiKey != "",
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": updated, "configured": apiKey != ""})
}

// parseIPSegmentID 解析路径参数 :id。非法时直接写 400 并返回 false。
func parseIPSegmentID(c *gin.Context) (int64, bool) {
	raw := strings.TrimSpace(c.Param("id"))
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "IP 段 ID 不合法"})
		return 0, false
	}
	return id, true
}

// classifyIPSegmentError 把 service 层错误映射成 HTTP 状态码。
//
// service/ip_segment_admin.go 目前用 fmt.Errorf 返回「段记录不存在」「段参数错误」
// 这类文案，**没有导出哨兵错误**；而那个文件属于 PR #4 的范围（本任务明令不改其逻辑），
// 所以这里只能按文案分类。一旦 service 侧补上哨兵错误（如 ErrIPSegmentNotFound），
// 应把下面两条 strings.Contains 换成 errors.Is，behavior 不变、判据变硬。
func classifyIPSegmentError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	switch {
	case errors.Is(err, service.ErrIPSegmentSchemaUnavailable):
		// 表还没建好：既不是参数错也不是「记录不存在」，用 503 让前端提示「数据表未就绪」。
		return http.StatusServiceUnavailable
	case errors.Is(err, service.ErrLocationInvalidKey):
		return http.StatusBadRequest
	case errors.Is(err, service.ErrLocationNotConfigured), errors.Is(err, service.ErrLocationUnavailable):
		return http.StatusServiceUnavailable
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "不存在"):
		return http.StatusNotFound
	case strings.Contains(message, "参数错误"),
		strings.Contains(message, "必须至少指定一个筛选条件"),
		strings.Contains(message, "无法刷新"):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// writeIPSegmentFailure 输出错误响应。
//
// 参数类 / 未找到类 / schema 未就绪这三类直接把 service 文案下发（都是固定中文，
// 不含上游原文、密钥或内部路径）；其余一律用固定兜底文案，避免把底层错误
// （可能是上游响应片段）暴露给前端。
func writeIPSegmentFailure(c *gin.Context, err error, fallback string) {
	status := classifyIPSegmentError(err)
	message := fallback
	if status == http.StatusBadRequest || status == http.StatusNotFound ||
		errors.Is(err, service.ErrIPSegmentSchemaUnavailable) {
		message = err.Error()
	}
	c.JSON(status, gin.H{"message": message})
}
