package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 黑名单管理页的 HTTP 控制器（BL-5，46-黑名单设计.md §6）。
//
// 五条路由（全部 Auth + middleware.RequireAdmin，见 internal/api/router.go）：
//
//	GET    /adapter-service/blacklist                 列表（分页 + kind/enabled/keyword）
//	POST   /adapter-service/blacklist                 新增（归一化 + 校验 + reason 必填）
//	PUT    /adapter-service/blacklist/:id/enabled      启停（单列写，不走 Save）
//	DELETE /adapter-service/blacklist/:id              删除（硬删，命中记录保留）
//	GET    /adapter-service/blacklist/hits             命中记录（分页 + 筛选）
//
// 两条不可协商的性质：
//
//  1. **写操作之后必须刷新进程内缓存**（`service.ReloadDefaultBlacklistCache`）。
//     否则管理页点了「停用」会显示已停用，但中间件还在按旧快照拦人 ——
//     「界面说改了、实际没改」是最难排查的一类故障（46-*.md §4）。
//     刷新失败不影响写结果：旧快照继续生效，只记 SysWarn（规则不会整批失效）。
//
//  2. **响应体不含 Pattern 原文以外的判定细节**。管理页**必须**能看到 Pattern
//     （运营要照着它判断规则写对没有），但命中记录的响应只回脱敏值 ——
//     命中行是「用户请求的值」，泄漏它等于泄漏别人的真实 IP/手机号。

// blacklistAdminDB 返回当前数据库句柄；未初始化时返回 nil（调用方负责降级）。
//
// 与 referralDB / reviewRebateDB 同构：单测里没有 manager 时不能 panic。
var blacklistAdminDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

// reloadBlacklistCache 是写操作后的缓存刷新钩子。
//
// 抽成可替换变量：单测里要能断言「写操作确实刷新了缓存」，
// 而不是只能靠人工点页面观察。
var reloadBlacklistCache = func(db *gorm.DB) error {
	if db == nil {
		return service.ErrBlacklistCacheNoDatabase
	}
	return service.ReloadDefaultBlacklistCache(db)
}

var errBlacklistDBUnavailable = errors.New("数据库未初始化")

// requireBlacklistDB 取一个带优先级的会话，并把「没有数据库」统一成一个错误。
//
// 用 PriorityLow：黑名单管理页是纯运营操作，不该抢答题链路的数据库连接。
func requireBlacklistDB() (*gorm.DB, error) {
	db := blacklistAdminDB()
	if db == nil {
		return nil, errBlacklistDBUnavailable
	}
	return withDatabasePriority(db, context.Background(), dbqueue.PriorityLow), nil
}

// blacklistPagination 解析分页参数。非法值回落到默认（与管理端其它列表一致），
// 不因为一个 pageSize=abc 就整页 400。
func blacklistPagination(c *gin.Context) (int, int) {
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

// parseOptionalBoolQuery 解析 ?enabled=true/false；空串返回 nil（= 不筛选）。
//
// 用指针而不是默认 false：裸 bool 的零值会把「不筛选」退化成「只看停用」，
// 于是列表默认只显示被停用的规则 —— 这是 GORM/JSON 零值陷阱在查询参数上的翻版。
func parseOptionalBoolQuery(value string) (*bool, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseBool(trimmed)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// parseBlacklistEntryID 解析 :id 路径参数。
func parseBlacklistEntryID(c *gin.Context) (int64, bool) {
	value, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || value <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数 id 无效"})
		return 0, false
	}
	return value, true
}

// BlacklistEntryList 返回黑名单规则列表（管理员）。
//
// 响应形态：{data: [...], total, page, pageSize}（与返佣/评价返现列表一致）。
func BlacklistEntryList(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireBlacklistDB()
	if err != nil {
		writeBlacklistAdminError(c, err)
		return
	}
	enabled, enabledErr := parseOptionalBoolQuery(c.Query("enabled"))
	if enabledErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数 enabled 只能是 true 或 false"})
		return
	}
	page, pageSize := blacklistPagination(c)
	result, err := service.ListBlacklistEntriesWithDB(db, service.BlacklistEntryListQuery{
		Kind:    strings.TrimSpace(c.Query("kind")),
		Enabled: enabled,
		Keyword: strings.TrimSpace(c.Query("keyword")),
	}, page, pageSize)
	if err != nil {
		writeBlacklistAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// BlacklistEntryCreate 新增一条黑名单规则（管理员）。
//
// 请求体（camelCase，与前端 BlacklistApi.js 一致）：
//
//	{"kind":"ip","pattern":"203.0.113.7","matchType":"","reason":"...","enabled":true,"expiresAt":"2026-11-11T00:00:00Z"}
//
// 三条校验全部**明确 400**，绝不静默 clamp：
//   - kind 必须在四类粒度内（region 由 service 层拒绝，见 ErrBlacklistRegionNotImplemented）；
//   - pattern 必须能被 NormalizeBlacklistValue 归一化（非法 IP/CIDR/手机号 400）；
//   - reason 必填（46-*.md §2.1：运营要能解释为什么拉黑）。
//
// enabled 缺省 = true：新建规则不写 enabled 时应该立刻生效，
// 否则运营加完一条永久规则发现不生效，还得回头再点一次启用。
func BlacklistEntryCreate(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	var input struct {
		Kind      string  `json:"kind"`
		Pattern   string  `json:"pattern"`
		MatchType string  `json:"matchType"`
		Reason    string  `json:"reason"`
		Enabled   *bool   `json:"enabled"`
		ExpiresAt *string `json:"expiresAt"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	expiresAt, expiresErr := parseBlacklistExpiresAt(input.ExpiresAt)
	if expiresErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": expiresErr.Error()})
		return
	}
	db, err := requireBlacklistDB()
	if err != nil {
		writeBlacklistAdminError(c, err)
		return
	}
	entry, err := service.CreateBlacklistEntryWithDB(db, service.CreateBlacklistEntryInput{
		Kind:      input.Kind,
		Pattern:   input.Pattern,
		MatchType: input.MatchType,
		Reason:    input.Reason,
		Enabled:   enabled,
		ExpiresAt: expiresAt,
		CreatedBy: operator.ID,
	})
	if err != nil {
		writeBlacklistAdminError(c, err)
		return
	}
	reloadBlacklistCacheAfterWrite(db)
	recordAccountAudit(c, &operator, "blacklist", 3, "新增黑名单规则", map[string]any{
		"entryId": entry.ID,
		"kind":    entry.Kind,
		// 审计只记脱敏值与类型：审计日志会被导出排查，不该带上完整规则边界。
		"valueMasked": entry.ValueMasked,
		"enabled":     entry.Enabled,
	})
	c.JSON(http.StatusCreated, entry)
}

// BlacklistEntryToggle 启停一条规则（管理员）。
//
// 请求体：{"enabled":true|false}。缺该字段 = 400（不是「默认停用」）：
// 一个不带目标状态的启停请求没有确定语义，猜一个方向比报错更糟。
func BlacklistEntryToggle(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseBlacklistEntryID(c)
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
		c.JSON(http.StatusBadRequest, gin.H{"message": "缺少 enabled 字段，无法确定目标状态"})
		return
	}
	db, err := requireBlacklistDB()
	if err != nil {
		writeBlacklistAdminError(c, err)
		return
	}
	entry, err := service.UpdateBlacklistEntryEnabledWithDB(db, id, *input.Enabled)
	if err != nil {
		writeBlacklistAdminError(c, err)
		return
	}
	reloadBlacklistCacheAfterWrite(db)
	recordAccountAudit(c, &operator, "blacklist", 2, "启停黑名单规则", map[string]any{
		"entryId": entry.ID,
		"enabled": entry.Enabled,
	})
	c.JSON(http.StatusOK, entry)
}

// BlacklistEntryDelete 删除一条规则（管理员）。
//
// 硬删除：实体没有 DeletedAt 列，46-*.md 也只提示了「删了就丢审计」这个风险，
// 没有要求软删。命中记录（blacklist_hits）保留，因此「这条规则当时拦了多少次」
// 仍然可查。
func BlacklistEntryDelete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseBlacklistEntryID(c)
	if !ok {
		return
	}
	db, err := requireBlacklistDB()
	if err != nil {
		writeBlacklistAdminError(c, err)
		return
	}
	entry, err := service.DeleteBlacklistEntryWithDB(db, id)
	if err != nil {
		writeBlacklistAdminError(c, err)
		return
	}
	reloadBlacklistCacheAfterWrite(db)
	recordAccountAudit(c, &operator, "blacklist", 4, "删除黑名单规则", map[string]any{
		"entryId":     entry.ID,
		"kind":        entry.Kind,
		"valueMasked": entry.ValueMasked,
	})
	c.JSON(http.StatusOK, gin.H{"message": "ok", "id": entry.ID})
}

// BlacklistHitList 返回命中记录（管理员，created_at 倒序）。
//
// 这是「用户为什么被拦」的唯一解释依据（46-*.md §2.2），
// 也是判断规则是否误伤的唯一数据来源。
func BlacklistHitList(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireBlacklistDB()
	if err != nil {
		writeBlacklistAdminError(c, err)
		return
	}
	entryID, entryErr := parseOptionalNonNegativeInt64(c.Query("entryId"))
	if entryErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数 entryId 格式无效"})
		return
	}
	page, pageSize := blacklistPagination(c)
	result, err := service.ListBlacklistHitsWithDB(db, service.BlacklistHitListQuery{
		EntryID:  entryID,
		Kind:     strings.TrimSpace(c.Query("kind")),
		ClientIP: strings.TrimSpace(c.Query("clientIp")),
		Action:   strings.TrimSpace(c.Query("action")),
	}, page, pageSize)
	if err != nil {
		writeBlacklistAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// parseOptionalNonNegativeInt64 解析 ?entryId=123；空串返回 0（= 不筛选）。
func parseOptionalNonNegativeInt64(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || parsed < 0 {
		if err == nil {
			err = strconv.ErrSyntax
		}
		return 0, err
	}
	return parsed, nil
}

// parseBlacklistExpiresAt 解析过期时间（nil = 永久）。
//
// 接受两种形态：RFC3339（2026-11-11T00:00:00Z）与纯日期（2026-11-11）。
// 纯日期按**当天 23:59:59（本地时区）**处理，而不是 00:00 ——
// 运营写「封到 11 月 11 日」的意思是「11 日这天还有效」，
// 按 00:00 处理会让规则在 11 日零点的第一秒就失效，与直觉相反。
func parseBlacklistExpiresAt(raw *string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	value := strings.TrimSpace(*raw)
	if value == "" {
		return nil, nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return &parsed, nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		end := parsed.AddDate(0, 0, 1).Add(-time.Second)
		return &end, nil
	}
	return nil, errors.New("过期时间格式无效：请用 RFC3339 或 YYYY-MM-DD")
}

// reloadBlacklistCacheAfterWrite 是写路径的统一收尾：刷新缓存，失败只记日志。
//
// 为什么失败不能影响响应：规则的写入已经提交成功，此时回 500 会让运营
// 以为没写成而重试；而旧快照继续生效也不会让规则「整批失效」（见 Reload 的语义）。
func reloadBlacklistCacheAfterWrite(db *gorm.DB) {
	if err := reloadBlacklistCache(db); err != nil {
		logger.SysWarn("黑名单规则已写入但缓存刷新失败（沿用旧快照）：" + err.Error())
	}
}

// writeBlacklistAdminError 把 service 错误映射成 HTTP 状态。
//
// 映射规则：
//
//	非法输入（kind/matchType/pattern/reason/expires） → 400（明确报错，不 clamp）
//	规则不存在                                        → 404
//	数据库未初始化 / 表结构问题                        → 503（不能假装成功）
//	其它                                              → 500（只记日志，不回原始错误）
func writeBlacklistAdminError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "操作黑名单规则失败"
	switch {
	case errors.Is(err, service.ErrInvalidBlacklistKind),
		errors.Is(err, service.ErrInvalidBlacklistMatchType),
		errors.Is(err, service.ErrInvalidBlacklistPattern):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrBlacklistRegionNotImplemented):
		// 400 而不是 501：这是「本期不支持」的产品边界，运营该知道自己填错类型了。
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrBlacklistEntryNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		status = http.StatusNotFound
		message = service.ErrBlacklistEntryNotFound.Error()
	case errors.Is(err, errBlacklistDBUnavailable), errors.Is(err, service.ErrBlacklistCacheNoDatabase):
		status = http.StatusServiceUnavailable
		message = "黑名单数据库暂不可用，请稍后再试"
	}
	if status >= http.StatusInternalServerError {
		logger.SysError("黑名单管理操作失败: " + logger.RedactSecrets(err.Error()))
	}
	c.JSON(status, gin.H{"message": message})
}
