package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 白名单管理页的 HTTP 控制器（T-04，task_doc/123：规则模仿黑名单）。
//
// 五条路由（全部 Auth + middleware.RequireAdmin）：
//
//	GET    /adapter-service/whitelist                 列表（分页 + kind/enabled/keyword）
//	POST   /adapter-service/whitelist                 新增（归一化 + 校验 + reason 必填）
//	PUT    /adapter-service/whitelist/:id/enabled      启停（单列写，不走 Save）
//	DELETE /adapter-service/whitelist/:id              删除（硬删 + 快照审计）
//	GET    /adapter-service/whitelist/exempt-ips       按 IP 白名单文本域（T-05）
//
// 两条不可协商的性质（与黑名单那组逐条对齐）：
//
//  1. **写操作之后必须刷新进程内缓存**（service.ReloadDefaultWhitelistCache）。
//     否则管理页点了「启用」会显示已启用，但中间件还按旧快照判定 ——
//     「界面说改了、实际没改」是最难排查的一类故障。
//     刷新失败不影响写结果：旧快照继续生效，只记 SysWarn。
//  2. **响应体只带脱敏值，不带判定细节**。管理页能看到脱敏值（运营据此确认
//     规则写对没有），但 hits 之类「用户请求的原值」不在白名单侧存在（白名单
//     不写命中日志，见 service/whitelist_cache.go 的红线三条）。

// whitelistAdminDB 返回当前数据库句柄；未初始化时返回 nil（调用方负责降级）。
//
// 与 blacklistAdminDB 同构：单测里没有 manager 时不能 panic。
var whitelistAdminDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

// reloadWhitelistCache 是写操作后的缓存刷新钩子。
//
// 抽成可替换变量：单测里要能断言「写操作确实刷新了缓存」，
// 而不是只能靠人工点页面观察。
var reloadWhitelistCache = func(db *gorm.DB) error {
	if db == nil {
		return service.ErrWhitelistCacheNoDatabase
	}
	return service.ReloadDefaultWhitelistCache(db)
}

var errWhitelistDBUnavailable = errors.New("数据库未初始化")

// requireWhitelistDB 取一个带优先级的会话，并把「没有数据库」统一成一个错误。
//
// 用 PriorityLow：白名单管理页是纯运营操作，不该抢答题链路的数据库连接。
func requireWhitelistDB() (*gorm.DB, error) {
	db := whitelistAdminDB()
	if db == nil {
		return nil, errWhitelistDBUnavailable
	}
	return withDatabasePriority(db, context.Background(), dbqueue.PriorityLow), nil
}

// parseWhitelistEntryID 解析 :id 路径参数。
func parseWhitelistEntryID(c *gin.Context) (int64, bool) {
	value, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || value <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数 id 无效"})
		return 0, false
	}
	return value, true
}

// WhitelistEntryList 返回白名单规则列表（管理员）。
//
// 响应形态：{data: [...], total, page, pageSize}（与黑名单列表一致）。
func WhitelistEntryList(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireWhitelistDB()
	if err != nil {
		writeWhitelistAdminError(c, err)
		return
	}
	enabled, enabledErr := parseOptionalBoolQuery(c.Query("enabled"))
	if enabledErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数 enabled 只能是 true 或 false"})
		return
	}
	page, pageSize := blacklistPagination(c)
	result, err := service.ListWhitelistEntriesWithDB(db, service.WhitelistEntryListQuery{
		Kind:    strings.TrimSpace(c.Query("kind")),
		Enabled: enabled,
		Keyword: strings.TrimSpace(c.Query("keyword")),
	}, page, pageSize)
	if err != nil {
		writeWhitelistAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// WhitelistEntryCreate 新增一条白名单规则（管理员）。
//
// 请求体（camelCase，与前端 WhitelistApi.js 一致）：
//
//	{"kind":"ip","pattern":"203.0.113.7","matchType":"","reason":"...","enabled":true,"expiresAt":"2026-11-11T00:00:00Z"}
//
// 校验全部**明确 400**，绝不静默 clamp：
//   - kind 只能是 ip（白名单没有 phone/wechat/region 落点）；
//   - pattern 必须能归一化成 IP 或 CIDR（判据②：非法项拒绝保存）；
//   - reason 必填（日后要能解释「这个网段为什么不必拦」）。
//
// enabled 缺省 = true：新建规则不写 enabled 时应该立刻生效。
func WhitelistEntryCreate(c *gin.Context) {
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
	// 复用黑名单那套时间解析（RFC3339 或 YYYY-MM-DD，日期按当天 23:59:59）。
	expiresAt, expiresErr := parseBlacklistExpiresAt(input.ExpiresAt)
	if expiresErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": expiresErr.Error()})
		return
	}
	db, err := requireWhitelistDB()
	if err != nil {
		writeWhitelistAdminError(c, err)
		return
	}
	entry, err := service.CreateWhitelistEntryWithDB(db, service.CreateWhitelistEntryInput{
		Kind:      input.Kind,
		Pattern:   input.Pattern,
		MatchType: input.MatchType,
		Reason:    input.Reason,
		Enabled:   enabled,
		ExpiresAt: expiresAt,
		CreatedBy: operator.ID,
	})
	if err != nil {
		writeWhitelistAdminError(c, err)
		return
	}
	reloadWhitelistCacheAfterWrite(db)
	recordAccountAudit(c, &operator, "whitelist", 3, "新增白名单规则", map[string]any{
		"entryId": entry.ID,
		"kind":    entry.Kind,
		// 审计只记脱敏值与类型：审计日志会被导出排查，不该带上完整规则边界。
		"valueMasked": entry.ValueMasked,
		"enabled":     entry.Enabled,
	})
	c.JSON(http.StatusCreated, entry)
}

// WhitelistEntryToggle 启停一条规则（管理员）。
//
// 请求体：{"enabled":true|false}。缺该字段 = 400（不是「默认停用」）：
// 一个不带目标状态的启停请求没有确定语义，猜一个方向比报错更糟。
func WhitelistEntryToggle(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseWhitelistEntryID(c)
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
	db, err := requireWhitelistDB()
	if err != nil {
		writeWhitelistAdminError(c, err)
		return
	}
	entry, err := service.UpdateWhitelistEntryEnabledWithDB(db, id, *input.Enabled)
	if err != nil {
		writeWhitelistAdminError(c, err)
		return
	}
	reloadWhitelistCacheAfterWrite(db)
	recordAccountAudit(c, &operator, "whitelist", 2, "启停白名单规则", map[string]any{
		"entryId": entry.ID,
		"enabled": entry.Enabled,
	})
	c.JSON(http.StatusOK, entry)
}

// WhitelistEntryDelete 删除一条规则（管理员）。
//
// 硬删除（实体没有 DeletedAt 列），但**删除前把完整快照写进日志**：
// 复用 rule_audit.go 的 archiveRuleDeleteInTx（同事务「查 → 写快照 → 物理删除」），
// 与黑名单/评价返现/支付方式三条删除路径同一个骨架，不复制第二份删除逻辑。
//
// `?reason=` 必填（与另外三处一致，Q77-6 选 C）。
func WhitelistEntryDelete(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	id, ok := parseWhitelistEntryID(c)
	if !ok {
		return
	}
	reason, ok := requireRuleDeleteReason(c)
	if !ok {
		return
	}
	db, err := requireWhitelistDB()
	if err != nil {
		writeWhitelistAdminError(c, err)
		return
	}
	entry, err := deleteWhitelistEntryWithSnapshot(db, id, operator, reason, c.ClientIP())
	if err != nil {
		writeWhitelistAdminError(c, err)
		return
	}
	reloadWhitelistCacheAfterWrite(db)
	recordAccountAudit(c, &operator, "whitelist", 4, "删除白名单规则", map[string]any{
		"entryId":     entry.ID,
		"kind":        entry.Kind,
		"valueMasked": entry.ValueMasked,
	})
	c.JSON(http.StatusOK, gin.H{"message": "ok", "id": entry.ID})
}

// WhitelistExemptIPsGet 读取按 IP 白名单文本域（T-05）。
//
// 响应：{"value": "...", "count": N, "items": ["203.0.113.7", "198.51.100.0/24"]}
//
// 同时回 count 与 items：运营要能确认「我粘贴的 20 行到底识别出了几个网段」，
// 只回原文的话，格式写错（比如全角逗号）会静默少解析几项而无人发现。
func WhitelistExemptIPsGet(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireWhitelistDB()
	if err != nil {
		writeWhitelistAdminError(c, err)
		return
	}
	raw := service.GetWhitelistExemptIPsWithDB(db)
	items := make([]string, 0)
	for _, network := range service.ParseWhitelistExemptIPs(raw) {
		if network == nil {
			continue
		}
		items = append(items, network.String())
	}
	c.JSON(http.StatusOK, gin.H{"value": raw, "count": len(items), "items": items})
}

// WhitelistExemptIPsPut 保存按 IP 白名单文本域（T-05）。
//
// 请求体：{"value":"203.0.113.7\n198.51.100.0/24"}
//
// **非法项拒绝保存**（400），不是跳过：白名单写错一个网段的表现是
// 「该放行的仍被拦」，运营从现象完全反推不到是格式问题。
// 读取侧仍保留「跳过坏项」的宽容（老库可能已被手工写入坏值）。
func WhitelistExemptIPsPut(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	var input struct {
		Value *string `json:"value"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	if input.Value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "缺少 value 字段"})
		return
	}
	db, err := requireWhitelistDB()
	if err != nil {
		writeWhitelistAdminError(c, err)
		return
	}
	raw := strings.TrimSpace(*input.Value)
	if err := service.ValidateWhitelistExemptIPs(raw); err != nil {
		writeWhitelistAdminError(c, err)
		return
	}
	if err := service.SaveWhitelistExemptIPsWithDB(db, raw); err != nil {
		writeWhitelistAdminError(c, err)
		return
	}
	// 保存后立刻重载：文本域是被快照读进内存的，不刷新等于「界面已保存但判定未变」。
	reloadWhitelistCacheAfterWrite(db)
	recordAccountAudit(c, &operator, "whitelist", 3, "更新按 IP 白名单", map[string]any{
		"count": len(service.ParseWhitelistExemptIPs(raw)),
	})
	c.JSON(http.StatusOK, gin.H{"message": "ok", "count": len(service.ParseWhitelistExemptIPs(raw))})
}

// deleteWhitelistEntryWithSnapshot 删除一条白名单规则，并在同一事务内先写完整快照。
//
// 骨架照抄 deleteBlacklistEntryWithSnapshot：物理删除语句仍然只在
// service.DeleteWhitelistEntryWithDB 里出现一次，这里只是在同一事务里**先**补快照。
func deleteWhitelistEntryWithSnapshot(db *gorm.DB, id int64, operator entity.User,
	reason, clientIP string) (entity.WhitelistEntry, error) {
	var entry entity.WhitelistEntry
	if db == nil {
		return entry, service.ErrWhitelistCacheNoDatabase
	}
	if id <= 0 {
		return entry, service.ErrWhitelistEntryNotFound
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		if !tx.Migrator().HasTable(entity.TableNameWhitelistEntry) {
			return service.ErrWhitelistEntryNotFound
		}
		// 1) 先查：快照必须在 DELETE 之前取。
		if err := tx.Session(&gorm.Session{NewDB: true}).Where("id = ?", id).Take(&entry).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return service.ErrWhitelistEntryNotFound
			}
			return err
		}
		// 2) 再记：同一事务内写快照日志（module=whitelist）。
		target := "白名单规则 " + entry.ValueMasked
		if strings.TrimSpace(entry.ValueMasked) == "" {
			target = "白名单规则 " + entry.Pattern
		}
		if err := archiveRuleDeleteInTx(tx, "whitelist", target,
			whitelistRuleSnapshot(entry), &operator, reason, clientIP); err != nil {
			return err
		}
		// 3) 最后删：失败则连同日志一起回滚。
		_, err := service.DeleteWhitelistEntryWithDB(tx.Session(&gorm.Session{NewDB: true}), id)
		return err
	})
	if err != nil {
		return entity.WhitelistEntry{}, err
	}
	return entry, nil
}

// whitelistRuleSnapshot 记白名单规则**全部业务字段**（与 blacklistRuleSnapshot 同口径）。
//
// 不入快照的字段与黑名单一致：id（重建时由库重新分配）、value_hash / value_masked
// （都是 pattern 的派生物）、updated_at（删除时点没有意义）。
func whitelistRuleSnapshot(entry entity.WhitelistEntry) map[string]any {
	snapshot := map[string]any{
		"kind":      entry.Kind,
		"pattern":   entry.Pattern,
		"matchType": entry.MatchType,
		"reason":    entry.Reason,
		"enabled":   entry.Enabled,
		"createdBy": entry.CreatedBy,
		"createdAt": formatRuleSnapshotTime(entry.CreatedAt),
		"expiresAt": nil,
	}
	// 过期时间必须区分「永久（null）」与「某时刻」，否则重建时会凭空变成永久规则。
	if entry.ExpiresAt != nil {
		snapshot["expiresAt"] = formatRuleSnapshotTime(*entry.ExpiresAt)
	}
	return snapshot
}

// reloadWhitelistCacheAfterWrite 是写路径的统一收尾：刷新缓存，失败只记日志。
//
// 失败不能影响响应：规则的写入已经提交成功，此时回 500 会让运营以为没写成而重试；
// 旧快照继续生效也不会让规则「整批失效」。
func reloadWhitelistCacheAfterWrite(db *gorm.DB) {
	if err := reloadWhitelistCache(db); err != nil {
		logger.SysWarn("白名单规则已写入但缓存刷新失败（沿用旧快照）：" + err.Error())
	}
}

// writeWhitelistAdminError 把 service 错误映射成 HTTP 状态。
//
// 映射规则（与 writeBlacklistAdminError 同构）：
//
//	非法输入（kind/matchType/pattern/reason/expires/exempt_ips） → 400（明确报错）
//	规则不存在                                                   → 404
//	数据库未初始化 / 表结构问题                                   → 503（不能假装成功）
//	其它                                                         → 500（只记日志）
func writeWhitelistAdminError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "操作白名单规则失败"
	switch {
	case errors.Is(err, service.ErrInvalidWhitelistKind),
		errors.Is(err, service.ErrInvalidWhitelistMatchType),
		errors.Is(err, service.ErrInvalidWhitelistPattern),
		errors.Is(err, service.ErrInvalidLoginRateLimitExemptIPs):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrWhitelistEntryNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		status = http.StatusNotFound
		message = service.ErrWhitelistEntryNotFound.Error()
	case errors.Is(err, errWhitelistDBUnavailable), errors.Is(err, service.ErrWhitelistCacheNoDatabase),
		errors.Is(err, service.ErrBlacklistCacheNoDatabase):
		status = http.StatusServiceUnavailable
		message = "白名单数据库暂不可用，请稍后再试"
	}
	if status >= http.StatusInternalServerError {
		logger.SysError("白名单管理操作失败: " + logger.RedactSecrets(err.Error()))
	}
	c.JSON(status, gin.H{"message": message})
}
