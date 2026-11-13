package controller

// 公告管理接口（N4，task_doc/112 §4.3 + 113 + 121 §4.5）。
//
// 本文件只做 HTTP 适配：鉴权、参数解析、错误分类、响应包装。
// 业务读写在 service/announcement.go。
//
// 五条路由（全部 Auth + middleware.RequireAdmin，路由注册由 captain 在
// internal/api/router.go 统一接线，本文件不改 router.go）：
//
//	GET    /adapter-service/admin/announcements             列表（kind/enabled/keyword/activeAt）
//	POST   /adapter-service/admin/announcements             新建
//	PUT    /adapter-service/admin/announcements/:id         编辑
//	PUT    /adapter-service/admin/announcements/:id/enabled 启停
//	DELETE /adapter-service/admin/announcements/:id         删除
//
// 鉴权双保险：路由层挂 middleware.RequireAdmin，handler 内再跑一次 requireAdmin
// —— 与本仓其它管理接口（blacklist_admin.go / ip_segment.go / user_level.go）口径一致。
// 理由不是「重复劳动」：`internal/api/ip_segment_middleware_test.go` 实测证明
// 删掉路由层的 RequireAdmin 后 controller 包测试仍全绿，因此 handler 内这一层
// 是「中间件被误删」时的唯一防线（纵深防御）。

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

// announcementAdminDB 返回公告管理使用的数据库句柄。
//
// 抽成可替换变量：单测里没有 manager 时不能 panic，且要能把测试库装进来。
// 与 blacklistAdminDB 同构。
var announcementAdminDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

// announcementNow 是「当前时间」的取用点。
//
// 抽成变量是为了让「窗口边界」这类断言可控：测试把边界时刻直接传进来，
// 而不是靠 sleep 或真等时钟。生产路径与 time.Now 完全一致。
var announcementNow = func() time.Time { return time.Now() }

var errAnnouncementDBUnavailable = errors.New("数据库未初始化")

// requireAnnouncementDB 取一个低优先级的数据库会话。
//
// 用 PriorityLow：公告管理是纯运营操作，不该抢答题链路的连接
// （与 requireBlacklistDB 同一考量）。
func requireAnnouncementDB(c *gin.Context) (*gorm.DB, error) {
	db := announcementAdminDB()
	if db == nil {
		return nil, errAnnouncementDBUnavailable
	}
	return withDatabasePriority(db, announcementRequestContext(c), dbqueue.PriorityLow), nil
}

// announcementRequestContext 取请求 context；handler 单测里 c.Request 可能为 nil。
func announcementRequestContext(c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return context.Background()
	}
	return c.Request.Context()
}

// parseAnnouncementID 解析 :id 路径参数。
func parseAnnouncementID(c *gin.Context) (int64, bool) {
	value, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || value <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数 id 无效"})
		return 0, false
	}
	return value, true
}

// AnnouncementAdminList 返回公告列表（管理员）。
//
// 查询参数（全部可选）：
//
//	kind=normal|incident     按类型筛
//	enabled=true|false       按开关筛（缺省 = 不筛，不是「只看停用」）
//	keyword=...              标题/正文模糊匹配
//	activeAt=2026-11-13T14:00:00Z  只看那一刻生效的公告
//
// 响应形态：{data: [...], total}。管理页必须能看到停用与已过期行 ——
// 它们的语义是「建过这条，现在不生效」，隐藏会让人以为公告被删掉后重复发布。
func AnnouncementAdminList(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	enabled, err := parseOptionalBoolQuery(c.Query("enabled"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数 enabled 只能是 true 或 false"})
		return
	}
	activeAt, activeErr := parseAnnouncementActiveAt(c.Query("activeAt"))
	if activeErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": activeErr.Error()})
		return
	}
	db, dbErr := requireAnnouncementDB(c)
	if dbErr != nil {
		writeAnnouncementAdminError(c, dbErr, "获取公告列表失败")
		return
	}
	items, err := service.ListAllAnnouncements(db, service.AnnouncementListFilter{
		Kind:     strings.TrimSpace(c.Query("kind")),
		Keywords: strings.TrimSpace(c.Query("keyword")),
		Enabled:  enabled,
		ActiveAt: activeAt,
	})
	if err != nil {
		writeAnnouncementAdminError(c, err, "获取公告列表失败")
		return
	}
	if items == nil {
		items = []entity.Announcement{}
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "total": len(items)})
}

// AnnouncementAdminCreate 新建公告（管理员）。
//
// 请求体（camelCase，与 WebUI 一致）：
//
//	{"kind":"incident","title":"题库服务异常","content":"题库源连接超时，正在抢修",
//	 "severity":"critical","enabled":true,"startsAt":"2026-11-13T14:07:00Z","endsAt":"2026-11-13T14:37:00Z"}
//
// 缺省：kind=normal（不填就不要影响 PLUS）、severity=info、enabled=true、
// startsAt=now、endsAt=nil（永久）。显式传非法值一律 400，绝不静默改正 ——
// 把 `kind=maintenance` 静默当成 normal 会得到「本该下发却没下发」的故障公告。
func AnnouncementAdminCreate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	var input service.AnnouncementInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	db, err := requireAnnouncementDB(c)
	if err != nil {
		writeAnnouncementAdminError(c, err, "新建公告失败")
		return
	}
	created, err := service.CreateAnnouncement(db, input, operator.ID, announcementNow())
	if err != nil {
		writeAnnouncementAdminError(c, err, "新建公告失败")
		return
	}
	recordAccountAudit(c, &operator, "announcement", logActionCreate, "新建公告", map[string]any{
		"announcementId": created.ID,
		"kind":           created.Kind,
		"severity":       created.Severity,
		"enabled":        created.Enabled,
		// 标题进审计（运营要能对着日志回溯「谁发过什么」），正文不进：
		// 审计日志会被导出排查，正文可能很长且无检索价值。
		"title": created.Title,
	})
	c.JSON(http.StatusCreated, created)
}

// AnnouncementAdminUpdate 编辑公告（管理员，全量覆盖）。
func AnnouncementAdminUpdate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseAnnouncementID(c)
	if !ok {
		return
	}
	var input service.AnnouncementInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	db, err := requireAnnouncementDB(c)
	if err != nil {
		writeAnnouncementAdminError(c, err, "保存公告失败")
		return
	}
	updated, err := service.UpdateAnnouncement(db, id, input, announcementNow())
	if err != nil {
		writeAnnouncementAdminError(c, err, "保存公告失败")
		return
	}
	recordAccountAudit(c, &operator, "announcement", logActionUpdate, "编辑公告", map[string]any{
		"announcementId": updated.ID,
		"kind":           updated.Kind,
		"severity":       updated.Severity,
		"enabled":        updated.Enabled,
		"title":          updated.Title,
	})
	c.JSON(http.StatusOK, updated)
}

// AnnouncementAdminToggle 启停公告（管理员）。
//
// 请求体：{"enabled":true|false}。缺该字段 = 400（不是「默认停用」）：
// 一个不带目标状态的启停请求没有确定语义，猜一个方向比报错更糟
// （与 BlacklistEntryToggle 同一判据）。
//
// 为什么单独一条而不是复用编辑接口：启停是列表页的一键操作，
// 若必须回传整条公告，前端一份过期快照就会把别人刚改的标题覆盖掉。
func AnnouncementAdminToggle(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseAnnouncementID(c)
	if !ok {
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	if input.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "缺少 enabled 字段"})
		return
	}
	db, err := requireAnnouncementDB(c)
	if err != nil {
		writeAnnouncementAdminError(c, err, "启停公告失败")
		return
	}
	updated, err := service.SetAnnouncementEnabled(db, id, *input.Enabled, announcementNow())
	if err != nil {
		writeAnnouncementAdminError(c, err, "启停公告失败")
		return
	}
	recordAccountAudit(c, &operator, "announcement", logActionUpdate, "启停公告", map[string]any{
		"announcementId": updated.ID,
		"enabled":        updated.Enabled,
		"kind":           updated.Kind,
		"title":          updated.Title,
	})
	c.JSON(http.StatusOK, updated)
}

// AnnouncementAdminDelete 删除公告（管理员，硬删）。
func AnnouncementAdminDelete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseAnnouncementID(c)
	if !ok {
		return
	}
	db, err := requireAnnouncementDB(c)
	if err != nil {
		writeAnnouncementAdminError(c, err, "删除公告失败")
		return
	}
	if err := service.DeleteAnnouncement(db, id); err != nil {
		writeAnnouncementAdminError(c, err, "删除公告失败")
		return
	}
	recordAccountAudit(c, &operator, "announcement", logActionDelete, "删除公告", map[string]any{
		"announcementId": id,
	})
	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

// parseAnnouncementActiveAt 解析 activeAt 查询参数。
//
// 接受 RFC3339（带时区）与 `2006-01-02T15:04:05`（按 UTC 解释，便于手敲）。
// 空串返回 nil = 不按「生效中」筛。
func parseAnnouncementActiveAt(value string) (*time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, trimmed)
		if err == nil {
			utc := parsed.UTC()
			return &utc, nil
		}
	}
	return nil, errors.New("筛选参数 activeAt 须为 RFC3339 时间，例如 2026-11-13T14:00:00Z")
}

// writeAnnouncementAdminError 把 service 层错误映射成 HTTP 状态。
//
// 三条分类（与 writeBlacklistAdminError 同构）：
//   - 校验失败 → 400 且回原文（管理员要看到「哪里填错了」）；
//   - 不存在   → 404；
//   - 其它     → 500 + 固定兜底文案（数据库错误细节不进响应）。
func writeAnnouncementAdminError(c *gin.Context, err error, fallback string) {
	status := http.StatusInternalServerError
	message := fallback

	switch {
	case errors.Is(err, service.ErrAnnouncementTitleRequired),
		errors.Is(err, service.ErrAnnouncementContentRequired),
		errors.Is(err, service.ErrAnnouncementKindInvalid),
		errors.Is(err, service.ErrAnnouncementSeverityInvalid),
		errors.Is(err, service.ErrAnnouncementWindowInvalid):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrAnnouncementNotFound):
		status = http.StatusNotFound
		message = err.Error()
	case errors.Is(err, errAnnouncementDBUnavailable), errors.Is(err, service.ErrAnnouncementDatabaseUnavailable):
		status = http.StatusServiceUnavailable
		message = "数据库未就绪"
	}
	c.JSON(status, gin.H{"message": message})
}
