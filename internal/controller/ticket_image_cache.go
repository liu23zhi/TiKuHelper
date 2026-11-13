package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"gorm.io/gorm"
)

// 工单图片缓存管理页的四条管理员接口（T-12，用户 2026-11-13 决策
// 「新增**工单图片缓存管理**」）。
//
//	GET    /adapter-service/admin/ticket-image-cache              列表（分页 + 筛选 + 四个汇总数字）
//	DELETE /adapter-service/admin/ticket-image-cache/:sha256      删除单条（ref_count>0 拒绝）
//	POST   /adapter-service/admin/ticket-image-cache/batch-delete 批量删除（同样遵守 ref_count 保护）
//	POST   /adapter-service/admin/ticket-image-cache/clean        立即清理一次（调用 T-11 的清理函数）
//
// 本文件只做 HTTP 适配：鉴权、参数解析、错误分类、响应包装。读写逻辑在
// service/ticket_attachment_blob.go 的 TicketImageCache* 段（只新增，未改既有函数）。
//
// 四条一律 Auth + RequireAdmin，且 handler 内先跑 requireAdmin：
// 路由层挡住未登录者，handler 层再挡一次「已登录但非管理员」——
// 两层都有才符合本仓其它管理接口的口径（见 controller/ip_segment.go）。
//
// ⚠️ 本任务禁止改动 internal/api/router.go，因此这四条路由**尚未注册**。
// 装配时按下面的路径挂到 apiRouter（与 /admin/ip-segments 同组）：
//
//	apiRouter.GET("/admin/ticket-image-cache", controller.Auth, middleware.RequireAdmin, controller.TicketImageCacheList)
//	apiRouter.DELETE("/admin/ticket-image-cache/:sha256", controller.Auth, middleware.RequireAdmin, controller.TicketImageCacheDelete)
//	apiRouter.POST("/admin/ticket-image-cache/batch-delete", controller.Auth, middleware.RequireAdmin, controller.TicketImageCacheBatchDelete)
//	apiRouter.POST("/admin/ticket-image-cache/clean", controller.Auth, middleware.RequireAdmin, controller.TicketImageCacheCleanNow)

// 便于测试替换（与 log_settings.go 的注入风格一致）。
var (
	// resolveTicketImageCacheDB 是可替换的取库入口。
	//
	// 单独抽一个 var 而不是让测试替换 resolveSearchPersistenceDB：后者是
	// package 级 func 声明，测试里既不能赋值，改了又会波及全部 search 用例。
	resolveTicketImageCacheDB = resolveSearchPersistenceDB
	// listTicketImageCache / deleteTicketImageCache / deleteTicketImageCachesBatch
	// 都是 service 层的直通，抽成 var 只是为了让「被引用必须拒绝」这类断言
	// 能在不造真实密文的前提下覆盖 controller 的分支（service 侧另有真库用例）。
	listTicketImageCache         = service.ListTicketImageCacheWithDB
	deleteTicketImageCache       = service.DeleteTicketImageCacheWithDB
	deleteTicketImageCachesBatch = service.DeleteTicketImageCachesBatchWithDB
	// ⚠️ TODO(waiting: T-11)：清理函数由 T-11（ticket_attachment_retention.go）提供。
	//
	// 截至本任务，该文件在**本分支上不存在**（只有 feat/ticket-image-retention
	// worktree 里的未跟踪文件）。按任务书要求：**不得自己写一份清理逻辑**，
	// 因此这里留成 nil 的注入点，handler 在 nil 时返回 available=false + 两个 0。
	//
	// T-11 落地后这里只需改成两行赋值，handler 与前端**一个字都不用动**：
	//
	//	pruneTicketImageCacheByAge  = service.PruneTicketAttachmentsByAge
	//	pruneTicketImageCacheBySize = service.PruneTicketAttachmentsBySize
	pruneTicketImageCacheByAge  func(db *gorm.DB, now time.Time, maxAgeDays int) (int64, error)
	pruneTicketImageCacheBySize func(db *gorm.DB, now time.Time, maxTotalBytes int64) (int64, error)
)

// ticketImageCacheDB 返回管理面使用的数据库句柄（低优先级会话）。
//
// 与 ip_segment.go 一致：manager / dao 未初始化时返回 nil，由 service 层给出
// 「数据库未初始化」而不是在控制器里空指针崩溃。
func ticketImageCacheDB(c *gin.Context) *gorm.DB {
	return withDatabasePriority(resolveTicketImageCacheDB(), databaseRequestContext(c), dbqueue.PriorityLow)
}

// ticketImageCacheCleanResponse 是「立即清理一次」的响应体。
//
// # 判据 ③：两个数字必须分开
//
// AgePurged / SizePurged **不能合成一个 total**：合成之后无法判断是哪条策略生效，
// 也就无法验证「先按时间、再按大小」这个顺序约定（若先跑大小，
// 会看到 SizePurged>0 而 AgePurged=0）。两个字段名与 T-11 的
// service.TicketAttachmentRetentionResult 一致，映射是直接的。
type ticketImageCacheCleanResponse struct {
	// AgePurged 是按时间策略清理掉的条数。
	AgePurged int64 `json:"agePurged"`
	// SizePurged 是按时间清理之后仍超总量、再按大小清理掉的条数。
	SizePurged int64 `json:"sizePurged"`
	// Available 表示清理函数是否已接线（false = 等 T-11 落地）。
	//
	// 显式给出而不是「两个 0 让前端猜」：0/0 在「没有可清理项」与
	// 「清理能力还没接线」两种情况下完全同形，而这两种情况的下一步动作相反
	// （前者正常，后者要去找开发）。
	Available bool `json:"available"`
	// Retention 是本次清理实际生效的三个保留策略值。
	Retention service.TicketImageCacheRetention `json:"retention"`
	// Message 是给管理员看的人话说明（available=false 时解释原因）。
	Message string `json:"message"`
}

// TicketImageCacheList 分页列出工单图片缓存。
//
//	GET /adapter-service/admin/ticket-image-cache?page=1&pageSize=20
//	    &mediaType=image/png&scanState=approved&refFilter=zero&keyword=ab12
//
// 响应直接复用 service.TicketImageCacheListResult（items / total / page / pageSize / summary）。
// summary 是**跨整个筛选范围**的汇总，不随翻页变化 —— 四个必须显示的数字都在里面：
//
//	① summary.totalBytes    总占用字节
//	② summary.zeroRefCount  ref_count=0 的待清理数
//	③ summary.scanStates    按 scan_state 分组
//	④ summary.retention     当前生效的三个保留策略值
func TicketImageCacheList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	scanState := strings.TrimSpace(c.Query("scanState"))
	if scanState != "" && !isTicketImageCacheScanState(scanState) {
		// 未知 scan_state 直接 400：放下去只会返回空列表，管理员会以为
		// 「这个状态下确实没有数据」，而真实原因是拼错了状态名。
		c.JSON(http.StatusBadRequest, gin.H{"message": "scanState 取值不合法"})
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	result, err := listTicketImageCache(ticketImageCacheDB(c), service.TicketImageCacheListQuery{
		MediaType: c.Query("mediaType"),
		ScanState: scanState,
		RefFilter: c.Query("refFilter"),
		Keyword:   c.Query("keyword"),
	}, page, pageSize)
	if err != nil {
		writeTicketImageCacheFailure(c, err, "获取工单图片缓存列表失败")
		return
	}
	c.JSON(http.StatusOK, result)
}

// isTicketImageCacheScanState 判断 scan_state 是否是已知取值。
//
// 取值一律用 entity 常量而不是字面量：状态名变更时这里随之覆盖，
// 不至于留下一个「校验表和实际写入值不一致」的缝。
func isTicketImageCacheScanState(value string) bool {
	switch value {
	case entity.TicketAttachmentScanPending, entity.TicketAttachmentScanScanning,
		entity.TicketAttachmentScanApproved, entity.TicketAttachmentScanSensitive,
		entity.TicketAttachmentScanFailed:
		return true
	}
	return false
}

// TicketImageCacheDelete 删除一条 blob。
//
//	DELETE /adapter-service/admin/ticket-image-cache/:sha256
//
// 判据 ①：ref_count > 0 时**必须拒绝**并说明被几个工单图片引用 → 409 + 具体数量。
// 这是本接口唯一一条不可妥协的约束：一份 blob 被多个附件行共享，删掉字节会让
// 那些工单的图片全部变 404，且密文已丢、不可恢复。
func TicketImageCacheDelete(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	sha, ok := parseTicketImageCacheSHA256(c)
	if !ok {
		return
	}
	deleted, blocked, err := deleteTicketImageCache(ticketImageCacheDB(c), sha)
	if err != nil {
		writeTicketImageCacheFailure(c, err, "删除工单图片缓存失败")
		return
	}
	if len(blocked) > 0 {
		// 409 而不是 403/400：这不是权限问题也不是参数问题，是**当前状态不允许**。
		// 前端据此展示「被 N 个工单引用」而不是笼统的「操作失败」。
		c.JSON(http.StatusConflict, gin.H{
			"message": blocked[0].Message,
			"deleted": deleted,
			"blocked": blocked,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": deletionMessage(deleted),
		"deleted": deleted,
		"blocked": []service.TicketImageCacheBlockedRef{},
	})
}

// TicketImageCacheBatchDelete 批量删除。
//
//	POST /adapter-service/admin/ticket-image-cache/batch-delete
//	{"sha256List":["aaaa…","bbbb…"]}
//
// 判据 ②：批量删除与单条走同一套 ref_count 保护（service 层逐条判定）。
// 若批量走「不检查引用直接 DELETE」，那么「单条删不掉的东西用批量就能删掉」，
// 保护就形同虚设。
//
// 部分成功是正常结果（有引用的被跳过）：返回 200 + deleted + blocked 列表，
// 页面据此显示「删了 N 条，M 条被引用跳过」。全部被挡住时返回 409，
// 与单条删除的状态码保持一致。
func TicketImageCacheBatchDelete(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	var input struct {
		SHA256List []string `json:"sha256List"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "批量删除参数错误"})
		return
	}
	if len(input.SHA256List) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "批量删除必须至少指定一条记录"})
		return
	}
	if len(input.SHA256List) > service.TicketAttachmentBlobPurgeBatchSize {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "单次批量删除最多 " + strconv.Itoa(service.TicketAttachmentBlobPurgeBatchSize) + " 条",
		})
		return
	}
	normalized := make([]string, 0, len(input.SHA256List))
	for _, raw := range input.SHA256List {
		sha, ok := normalizeTicketImageCacheSHA256(raw)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"message": "工单图片标识不合法（必须是 64 位十六进制 sha256）"})
			return
		}
		normalized = append(normalized, sha)
	}
	deleted, blocked, err := deleteTicketImageCachesBatch(ticketImageCacheDB(c), normalized)
	if err != nil {
		writeTicketImageCacheFailure(c, err, "批量删除工单图片缓存失败")
		return
	}
	status := http.StatusOK
	message := deletionMessage(deleted)
	if len(blocked) > 0 {
		message = message + "；" + strconv.Itoa(len(blocked)) + " 条因仍被工单图片引用而跳过"
		if deleted == 0 {
			status = http.StatusConflict
		}
	}
	c.JSON(status, gin.H{
		"message": message,
		"deleted": deleted,
		"blocked": blocked,
	})
}

// TicketImageCacheCleanNow 立即按当前保留策略清理一次。
//
//	POST /adapter-service/admin/ticket-image-cache/clean
//
// 不接收参数：清理规则一律取库里已保存的配置，避免「按钮传了一套参数、
// 保存的是另一套」导致管理员以为改配置了其实只影响这一次清理。
//
// ⚠️ TODO(waiting: T-11)：清理函数 `PruneTicketAttachmentsByAge` /
// `PruneTicketAttachmentsBySize` 由 T-11 提供，**本任务不自己实现**。
// 在它们落地之前，本 handler 返回 200 + available=false + agePurged=0 + sizePurged=0，
// 并在 message 里说明「清理能力待 T-11 接线」——**不报 5xx**：
// 这个按钮点不通是「功能还没上线」，不是故障，页面要能如实显示而不是弹错误。
//
// 顺序是硬约定（与 T-11 的 RunTicketAttachmentRetentionWithDB 一致）：
// **先按时间、再按大小**。因为按大小清理的判据读的是「先按时间清理之后」的
// 存留总量，先跑大小会看到 SizePurged>0 而 AgePurged=0，两条策略的因果就颠倒了。
func TicketImageCacheCleanNow(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	db := ticketImageCacheDB(c)
	// 保留策略的三个值同时下发：页面要显示「本次按 180 天 / 20 GiB 清理」，
	// 不能只给结果不给口径。
	retention := listTicketImageCacheRetention(db)
	response := ticketImageCacheCleanResponse{Retention: retention}
	if pruneTicketImageCacheByAge == nil || pruneTicketImageCacheBySize == nil {
		response.Message = "清理能力尚未接线（等待 T-11 的 PruneTicketAttachmentsByAge / PruneTicketAttachmentsBySize）"
		c.JSON(http.StatusOK, response)
		return
	}
	response.Available = true
	now := time.Now()
	// 先按时间：陈年数据本来就该走，先清掉它能让按大小的压力最小。
	agePurged, err := pruneTicketImageCacheByAge(db, now, retention.MaxAgeDays)
	response.AgePurged = agePurged
	if err != nil {
		// 「按时间」失败时**仍然下发已拿到的数字**：AgePurged 可能 >0（部分成功），
		// 用 5xx 会把已经删掉的条数藏起来。
		response.Message = "按时间清理失败：" + err.Error()
		c.JSON(http.StatusOK, response)
		return
	}
	// 再按大小：判据读的是上面那步之后的存留总量。
	sizePurged, err := pruneTicketImageCacheBySize(db, now, retention.MaxTotalBytes)
	response.SizePurged = sizePurged
	if err != nil {
		response.Message = "按大小清理失败：" + err.Error()
		c.JSON(http.StatusOK, response)
		return
	}
	response.Message = "已按时间清理 " + strconv.FormatInt(agePurged, 10) +
		" 条，按大小清理 " + strconv.FormatInt(sizePurged, 10) + " 条"
	c.JSON(http.StatusOK, response)
}

// listTicketImageCacheRetention 读取三个保留策略值（service 层的只读入口）。
//
// 单独包一层是因为它在「清理」路径上也要用，而列表接口的返回值里没有独立的
// retention 端点。传入 nil 库时回落默认值（页面照样显示 180 天 / 20 GiB / 60 分钟）。
func listTicketImageCacheRetention(db *gorm.DB) service.TicketImageCacheRetention {
	result, err := listTicketImageCache(db, service.TicketImageCacheListQuery{}, 1, 1)
	if err != nil {
		return service.TicketImageCacheRetention{
			MaxAgeDays:             180,
			MaxTotalGiB:            20,
			MaxTotalBytes:          20 << 30,
			CleanupIntervalMinutes: 60,
		}
	}
	return result.Summary.Retention
}

// parseTicketImageCacheSHA256 解析并校验路径参数 :sha256。
//
// 非法时直接写 400 并返回 false。校验放前面是为了让「不合法标识」与
// 「记录不存在」在响应上可区分：前者是客户端问题（400），后者是幂等成功（200）。
func parseTicketImageCacheSHA256(c *gin.Context) (string, bool) {
	sha, ok := normalizeTicketImageCacheSHA256(c.Param("sha256"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"message": "工单图片标识不合法（必须是 64 位十六进制 sha256）"})
		return "", false
	}
	return sha, true
}

// normalizeTicketImageCacheSHA256 归一化并校验一个 sha256（小写 64 位十六进制）。
//
// 归一化走 service 的同一个函数（大小写不同就是两个不同的 blob，见其注释），
// 校验则必须在这里做：不校验就把任意字符串拼进 WHERE，既是无效查询，
// 也让「记录不存在」这类真实原因被淹没。
func normalizeTicketImageCacheSHA256(raw string) (string, bool) {
	sha := service.NormalizeTicketAttachmentBlobSHA256(raw)
	if len(sha) != 64 {
		return "", false
	}
	for _, char := range sha {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", false
		}
	}
	return sha, true
}

// deletionMessage 拼「删了几条」的人话。
//
// 0 条不写成「删除失败」：记录已被别人删掉是幂等成功，页面重复点删除
// 不该弹错误（与 ip-segment 删除的 404 语义一致，但这里连 404 都不给，
// 因为 blob 的主键就是 sha256，重复删除必然落到「不存在」）。
func deletionMessage(deleted int64) string {
	if deleted <= 0 {
		return "记录已不存在"
	}
	return "已删除 " + strconv.FormatInt(deleted, 10) + " 条工单图片缓存"
}

// classifyTicketImageCacheError 把 service 层错误映射成 HTTP 状态码。
func classifyTicketImageCacheError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	switch {
	case errors.Is(err, service.ErrTicketAttachmentBlobInvalid):
		return http.StatusBadRequest
	case errors.Is(err, service.ErrTicketAttachmentBlobNotFound):
		return http.StatusNotFound
	}
	if strings.Contains(err.Error(), "数据库未初始化") {
		// 与其它管理接口同口径：库没起来是环境问题，用 503 让页面提示「稍后重试」
		// 而不是让管理员以为参数填错了。
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// writeTicketImageCacheFailure 输出错误响应。
//
// 参数类 / 未找到类直接把 service 文案下发（都是固定中文，不含密文、密钥或内部路径）；
// 其余一律用固定兜底文案，避免把底层错误（可能含 SQL 片段）暴露给前端。
func writeTicketImageCacheFailure(c *gin.Context, err error, fallback string) {
	status := classifyTicketImageCacheError(err)
	message := fallback
	if status == http.StatusBadRequest || status == http.StatusNotFound {
		message = err.Error()
	}
	c.JSON(status, gin.H{"message": message})
}

// 编译期确认 entity 的包仍被引用（scan_state 常量在 service 侧使用）。
var _ = entity.TableNameTicketAttachmentBlob
