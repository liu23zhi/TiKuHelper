package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 账号封禁管理页的 HTTP 控制器（121 §4.8 / 121 决策 6 / 46 号 §5）。
//
// 三条路由（全部 Auth + middleware.RequireAdmin）：
//
//	GET    /adapter-service/admin/user-bans               列表（分页 + userId/active/keyword）
//	POST   /adapter-service/admin/user-bans               封禁（reason 必填，expiresAt 空=永久）
//	DELETE /adapter-service/admin/user-bans/:userId       解封（软撤销，不物理删行）
//
// # 这一层存在的理由
//
// `service.Ban` / `service.Unban` 在本次接线之前**全仓零生产调用方** ——
// 能力完整实现但完全不可达。本文件是它唯一的入口。
//
// # 三条不可协商的性质
//
//  1. **解封是软撤销，绝不 DELETE 行**。解封走 `service.Unban`，它把尚未到期的行的
//     `expires_at` 提前置为当前时刻并复位 `users.status`，行本身完整保留。
//     封禁记录是审计事实（「谁在什么时候因为什么封了谁」），删了就再也解释不清。
//
//  2. **参数校验一律明确 400，绝不静默 clamp**。空 reason、过去的 expiresAt、
//     非法 userId 都返回 400 并给出可读原因 —— 静默接受会让运营看到「封禁成功」
//     而实际没有人被封。
//
//  3. **列表默认只返回生效中的封禁**。`active` 不传等价于 `active=true`：
//     管理页的主要用途是「现在谁被封着」，历史记录需要显式查询。
//     注意这里必须用指针/三态解析，裸 bool 的零值 false 会把默认值翻成「只看全部」。
//
// # ⚠️ 与任务书的两处实现差异（已实测确认，此处如实记录）
//
//   - `user_bans` 表**没有** `revoked_at` / `revoked_by` 两列（`entity/user_bans.go`
//     全文无此字段，列集由 121 §4.8 固定）。解封的事实由 `expires_at` 被提前置位表达。
//     因此「已撤销」与「已过期」在**列上不可直接区分**，见 userBanRecordStatus 的注释。
//   - `service.UserBanRequest` **没有** `source` 字段，「来源」由 `OperatorUserID`
//     表达（0 = 系统自动封禁，非 0 = 某管理员）。列表的 source 列据此派生。

// userBanAdminDB 返回当前数据库句柄；未初始化时返回 nil（调用方负责降级）。
//
// 与 blacklistAdminDB / whitelistAdminDB 同构：单测里没有 manager 时不能 panic。
var userBanAdminDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

var errUserBanAdminDBUnavailable = errors.New("数据库未初始化")

// requireUserBanDB 取一个带优先级的会话，并把「没有数据库」统一成一个错误。
//
// 用 PriorityLow：封禁管理页是纯运营操作，不该抢答题链路的数据库连接。
func requireUserBanDB() (*gorm.DB, error) {
	db := userBanAdminDB()
	if db == nil {
		return nil, errUserBanAdminDBUnavailable
	}
	return withDatabasePriority(db, context.Background(), dbqueue.PriorityLow), nil
}

// 封禁记录在管理页上的三态。
//
// ⚠️ 「已撤销」是**推断**而非事实：表里没有 revoked_at 列，Unban 的实现是
// 「把未到期行的 expires_at 置为 now」，同时更新 updated_at。因此：
//
//	已撤销：expires_at 与 updated_at 相等（两者由同一个 now 写入）
//	已过期：expires_at 早于 updated_at（expires_at 是当初设的，updated_at 是封禁时刻）
//
// 这个推断依赖「Unban 用同一个 now 写两列」这一实现细节。它成立的前提在
// service/user_ban.go:376-380 的 Updates(map[...]{"expires_at": now, "updated_at": now})。
// 若将来 service 改成只写 expires_at，本推断会退化成「全部显示为已过期」——
// 表现为「解封记录看起来像自然过期」，不报错。因此：
//   - 前端不要把「已撤销」当作强语义使用（不给它配撤销原因之类的功能）；
//   - 真正的判据仍是 IsBannedAt（active 与它一致）。
const (
	UserBanRecordStatusActive  = "active"
	UserBanRecordStatusExpired = "expired"
	UserBanRecordStatusRevoked = "revoked"
)

// UserBanAdminItem 是列表返回的一行。
//
// Username 是**展示快照**（join users 得来，只读）：账号改过名之后，
// 历史封禁记录仍应显示「当时是谁」，而不是跟着改名后的值漂移 ——
// 但这张表没有 username 快照列，所以这里只能给当前用户名，
// 并在前端注明「用户名为当前值」。如实标注比假装有快照好。
type UserBanAdminItem struct {
	ID       int64  `json:"id"`
	UserID   int32  `json:"userId"`
	Username string `json:"username"`
	Reason   string `json:"reason"`
	// Source 是派生值：`system`（BannedBy=0）或 `admin`（BannedBy>0）。
	// 同时也是给人看的标签，前端映射成「系统自动」/「管理员」。
	Source   string `json:"source"`
	BannedBy int32  `json:"bannedBy"`
	// BannedAt 是封禁生效时间（业务时间，非行写入时间）。
	BannedAt time.Time `json:"bannedAt"`
	// ExpiresAt 为 null = **永久封禁**。前端必须显示「永久」，不能显示「—」或空：
	// 「没填」与「永久」是相反语义，显示成空会被读成「数据缺失」。
	ExpiresAt *time.Time `json:"expiresAt"`
	// Status 取 active / expired / revoked（见上面三态注释）。
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// UserBanAdminListResult 是列表响应体（与黑名单/白名单列表同形态）。
type UserBanAdminListResult struct {
	Data     []UserBanAdminItem `json:"data"`
	Total    int64              `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"pageSize"`
}

// userBanPagination 解析分页参数。非法值回落到默认，不因为一个 pageSize=abc 整页 400。
func userBanPagination(c *gin.Context) (int, int) {
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

// userBanRecordStatus 由一行记录判定三态。
//
// now 显式传入（而不是内部取 time.Now()）：列表的每一行必须用**同一个基准时刻**
// 判定，否则跨零点翻页时同一批数据会出现「上一条已过期、下一条还生效」的抖动。
func userBanRecordStatus(row entity.UserBan, now time.Time) string {
	if row.ExpiresAt != nil {
		// 撤销判据：Unban 用同一个 now 同时写 expires_at 与 updated_at（见三态注释）。
		// 用 Equal 而不是 ==：time.Time 直接比较会比较单调时钟字段，跨 GORM 读回必不相等。
		if row.ExpiresAt.Equal(row.UpdatedAt) {
			return UserBanRecordStatusRevoked
		}
		if !row.ExpiresAt.After(now) {
			return UserBanRecordStatusExpired
		}
	}
	// ExpiresAt 为 nil（永久）或尚未到期 → 生效中。
	return UserBanRecordStatusActive
}

// UserBanAdminList 返回封禁记录列表（管理员）。
//
// 筛选参数：
//
//	userId  精确匹配某个用户（空 = 不筛选）
//	active  true/缺省 = 只返回生效中；false = 返回全部（含已过期/已撤销）
//	keyword 只匹配 reason 与用户名，**不匹配 userId 的数字**（与黑名单「关键词不匹配规则原文」同约定）
func UserBanAdminList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	db, err := requireUserBanDB()
	if err != nil {
		writeUserBanAdminError(c, err)
		return
	}
	if !db.Migrator().HasTable(entity.TableNameUserBan) {
		// 迁移未跑：返回空列表而不是 500。
		//
		// 这里与 service 的降级口径一致（缺表时只读 users.status），但列表没有
		// 可降级的对象 —— 空列表 + 200 让页面正常渲染「暂无封禁记录」，
		// 而不是把「迁移没登记」伪装成一个页面错误。
		c.JSON(http.StatusOK, UserBanAdminListResult{Data: []UserBanAdminItem{}, Page: 1, PageSize: 20})
		return
	}

	activeArg, activeErr := parseOptionalBoolQuery(c.Query("active"))
	if activeErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数 active 只能是 true 或 false"})
		return
	}
	var userIDFilter int32
	if raw := strings.TrimSpace(c.Query("userId")); raw != "" {
		parsed, parseErr := strconv.ParseInt(raw, 10, 32)
		if parseErr != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"message": "筛选参数 userId 必须是正整数"})
			return
		}
		userIDFilter = int32(parsed)
	}
	keyword := strings.TrimSpace(c.Query("keyword"))
	page, pageSize := userBanPagination(c)

	now := time.Now()
	query := db.Session(&gorm.Session{NewDB: true}).Model(&entity.UserBan{})
	if userIDFilter > 0 {
		query = query.Where("user_id = ?", userIDFilter)
	}
	// active 的三态语义：
	//   nil   → 默认只返回生效中（页面主用途是「现在谁被封着」）
	//   true  → 同 nil（显式要求生效中）
	//   false → 返回全部（含历史）
	//
	// ⚠️ 生效中的 SQL 条件必须与 service.hasUnexpiredUserBan 逐字一致
	// （`expires_at IS NULL OR expires_at > now`），否则会出现
	// 「列表说生效中、判定说没封」这类两边不一致的状态。
	onlyActive := activeArg == nil || *activeArg
	if onlyActive {
		query = query.Where("expires_at IS NULL OR expires_at > ?", now.UTC())
	}
	// keyword 同时匹配 reason 与用户名。
	//
	// ⚠️ 用户名匹配必须落在 **SQL 里**，不能先分页再在内存里过滤：
	// 后者会让 total 与实际返回行数不一致（页面上「共 N 条」是假的，
	// 且翻页会漏行）。做法是先把用户名命中的 user_id 查出来，再作为
	// `reason LIKE ? OR user_id IN ?` 的一部分，分页与统计仍在数据库内完成。
	//
	// 已知方言差异（如实记录，不做跨方言抹平）：SQLite 的 LIKE 对 ASCII 大小写
	// 不敏感，MySQL 取决于列的 collation（utf8mb4_general_ci 同样不敏感）。
	// 中文用户名不受影响；ASCII 用户名在极端 collation 下可能存在大小写差异。
	if keyword != "" {
		matchingUserIDs, idsErr := userBanKeywordUserIDs(db, keyword)
		if idsErr != nil {
			writeUserBanAdminError(c, idsErr)
			return
		}
		pattern := "%" + keyword + "%"
		if len(matchingUserIDs) > 0 {
			query = query.Where("reason LIKE ? OR user_id IN ?", pattern, matchingUserIDs)
		} else {
			query = query.Where("reason LIKE ?", pattern)
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		writeUserBanAdminError(c, fmt.Errorf("统计封禁记录失败: %w", err))
		return
	}

	var rows []entity.UserBan
	if err := query.
		Order("banned_at DESC, id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&rows).Error; err != nil {
		writeUserBanAdminError(c, fmt.Errorf("读取封禁记录失败: %w", err))
		return
	}

	items, err := buildUserBanAdminItems(db, rows, now)
	if err != nil {
		writeUserBanAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, UserBanAdminListResult{
		Data: items, Total: total, Page: page, PageSize: pageSize,
	})
}

// userBanKeywordUserIDs 返回用户名命中 keyword 的 user_id 列表。
//
// 上限 500 条：keyword 是运营手输的片段（如「test」），命中几千个用户时
// `IN (...)` 会把 SQL 撑爆且对运营毫无用处 —— 那种情况下用户应该用 userId
// 精确筛选。到上限就截断，不静默装作「只有这些」：调用方照常过滤，
// 只是超出部分不会命中用户名（用 reason 仍能命中）。
func userBanKeywordUserIDs(db *gorm.DB, keyword string) ([]int32, error) {
	const userBanKeywordUserIDLimit = 500
	var ids []int32
	if err := db.Session(&gorm.Session{NewDB: true}).
		Model(&entity.User{}).
		Where("username LIKE ?", "%"+keyword+"%").
		Limit(userBanKeywordUserIDLimit).
		Pluck("id", &ids).Error; err != nil {
		return nil, fmt.Errorf("按用户名筛选封禁记录失败: %w", err)
	}
	return ids, nil
}

// buildUserBanAdminItems 批量补用户名并按三态标注。
//
// 用户名用**一次 IN 查询**取，不做 N+1：一页最多 100 行，
// 逐行查会把列表接口变成 101 次数据库往返。
func buildUserBanAdminItems(db *gorm.DB, rows []entity.UserBan, now time.Time) ([]UserBanAdminItem, error) {
	items := make([]UserBanAdminItem, 0, len(rows))
	if len(rows) == 0 {
		return items, nil
	}
	ids := make([]int32, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.UserID)
	}
	type usernameRow struct {
		ID       int32
		Username string
	}
	var found []usernameRow
	if err := db.Session(&gorm.Session{NewDB: true}).
		Model(&entity.User{}).
		Select("id", "username").
		Where("id IN ?", ids).
		Find(&found).Error; err != nil {
		return nil, fmt.Errorf("读取封禁记录的用户名失败: %w", err)
	}
	names := make(map[int32]string, len(found))
	for _, item := range found {
		names[item.ID] = item.Username
	}

	for _, row := range rows {
		source := "admin"
		if row.BannedBy == 0 {
			source = "system"
		}
		items = append(items, UserBanAdminItem{
			ID:        row.ID,
			UserID:    row.UserID,
			Username:  names[row.UserID],
			Reason:    row.Reason,
			Source:    source,
			BannedBy:  row.BannedBy,
			BannedAt:  row.BannedAt,
			ExpiresAt: row.ExpiresAt,
			Status:    userBanRecordStatus(row, now),
			CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		})
	}
	return items, nil
}

// UserBanAdminCreate 封禁一个用户（管理员）。
//
// 请求体（camelCase，与前端 UserBanApi.js 一致）：
//
//	{"userId":42,"reason":"违规刷单","expiresAt":"2026-11-20"}
//
// 三条校验全部**明确 400**，绝不静默 clamp：
//
//   - userId 必须是正整数（0/负数/非数字一律 400）；
//   - reason 必填（service 层也拒绝空白，这里提前拦住并给出字段级文案）；
//   - expiresAt 若给出，必须在**未来**（service 同样校验）。
//
// expiresAt 缺省/空串 = 永久封禁。这一点必须在语义上明确：
// 空串不是「不设置」，而是「永不自动解除」。
func UserBanAdminCreate(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	var input struct {
		UserID int32  `json:"userId"`
		Reason string `json:"reason"`
		// ExpiresAt 是**绝对**到期时间（RFC3339 或 YYYY-MM-DD）。留空 = 永久。
		ExpiresAt *string `json:"expiresAt"`
		// DurationAmount + DurationUnit 是**相对**时长（「自定义时长封禁」）。
		//
		// 与 ExpiresAt 二选一，**DurationAmount 优先**：前端封禁弹窗用时长表达
		// （「封 7 天」），精确到具体时刻的诉求才用 ExpiresAt。
		// DurationAmount 为 0 且 ExpiresAt 为空 ⇒ 永久封禁。
		//
		// ⚠️ 为什么要这一层而不是让前端自己算好绝对时间再传 ExpiresAt：
		// 前端算时间会吃到浏览器本地时区与时钟偏差，而「封 7 天」是**服务端语义**
		// （用户的封禁时长不该取决于管理员电脑的时钟）。让服务端按时长自己算，
		// 浏览器时钟偏 10 分钟也不会让封禁时长偏 10 分钟。
		DurationAmount int    `json:"durationAmount"`
		DurationUnit   string `json:"durationUnit"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	if input.UserID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请选择要封禁的用户"})
		return
	}
	if strings.TrimSpace(input.Reason) == "" {
		// 400 而不是 500：这是调用方漏传，不是服务端故障。
		// 也不能静默补默认值 —— 审计里出现「无原因」等于没有审计。
		c.JSON(http.StatusBadRequest, gin.H{"message": "封禁原因不能为空"})
		return
	}
	// 时长优先于绝对时间：两者同时给出时按时长算（页面只会给时长）。
	//
	// 负数必须显式拒绝而不是当成「永久」：`durationAmount: -1` 是明显的调用方 bug，
	// 而把它静默解读成永久封禁会让一次误操作变成不可撤销的处置。
	if input.DurationAmount < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "封禁时长不能为负数"})
		return
	}
	var expiresAt *time.Time
	if input.DurationAmount > 0 {
		resolved, durationErr := service.ResolveUserBanExpiresAt(input.DurationAmount, input.DurationUnit, time.Now())
		if durationErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": durationErr.Error()})
			return
		}
		expiresAt = resolved
	} else {
		parsed, expiresErr := parseUserBanExpiresAt(input.ExpiresAt)
		if expiresErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": expiresErr.Error()})
			return
		}
		expiresAt = parsed
	}
	// 过去时间必须在 controller 层就拒掉，给出明确的字段级文案；
	// service 层也有一份同样的校验（作为最后防线），但它的错误是英文哨兵包装，
	// 直接透出去会让前端拿到不够具体的提示。
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "到期时间必须晚于当前时间（留空表示永久封禁）"})
		return
	}
	db, err := requireUserBanDB()
	if err != nil {
		writeUserBanAdminError(c, err)
		return
	}
	record, err := service.Ban(db, service.UserBanRequest{
		UserID:         input.UserID,
		Reason:         input.Reason,
		ExpiresAt:      expiresAt,
		OperatorUserID: operator.ID,
	})
	if err != nil {
		writeUserBanAdminError(c, err)
		return
	}
	recordAccountAudit(c, &operator, "user_ban", 1, "封禁账号", map[string]any{
		"userId":         record.UserID,
		"reason":         record.Reason,
		"expiresAt":      record.ExpiresAt,
		"durationAmount": input.DurationAmount,
		"durationUnit":   input.DurationUnit,
	})
	c.JSON(http.StatusCreated, gin.H{
		"id":             record.ID,
		"userId":         record.UserID,
		"reason":         record.Reason,
		"expiresAt":      record.ExpiresAt,
		"bannedAt":       record.BannedAt,
		"durationAmount": input.DurationAmount,
		"durationUnit":   input.DurationUnit,
		// permanent 是**派生**的便利字段（expiresAt 为 null 即永久）。
		// 前端据此决定显示「永久」还是具体时刻，不必自己再判一次 null。
		"permanent": record.ExpiresAt == nil,
	})
}

// UserBanAdminRevoke 解除封禁（管理员）。
//
// 路径参数是 **userId**，不是 user_bans 的主键 id。
//
// 为什么用 userId 而不是记录 id：`service.Unban` 的入参是 `UserUnbanRequest{UserID}`，
// 它的语义是「解除这个用户的封禁」—— 而不是「作废这一条记录」。一个用户可能有多条
// 历史封禁行，按记录 id 解封会让调用方以为「只解了这一条」，实际 service 处理的是
// 该用户全部未到期行。用 userId 让路径与语义一致，也避免了「前端传错 id 造成
// 解封了另一个用户」这类跨账号误操作。
//
// 解封是**软撤销**：走 service.Unban，把未到期行的 expires_at 提前置为当前时刻，
// 并复位 users.status，**行本身保留**。绝不 DELETE。
func UserBanAdminRevoke(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	c.Header("Cache-Control", "no-store")
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	raw := strings.TrimSpace(c.Param("userId"))
	parsed, parseErr := strconv.ParseInt(raw, 10, 32)
	if parseErr != nil || parsed <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数 userId 无效"})
		return
	}
	userID := int32(parsed)
	db, err := requireUserBanDB()
	if err != nil {
		writeUserBanAdminError(c, err)
		return
	}
	reason := strings.TrimSpace(c.Query("reason"))
	if err := service.Unban(db, service.UserUnbanRequest{
		UserID:         userID,
		OperatorUserID: operator.ID,
		Reason:         reason,
	}); err != nil {
		writeUserBanAdminError(c, err)
		return
	}
	recordAccountAudit(c, &operator, "user_ban", 2, "解除封禁", map[string]any{
		"userId": userID,
		"reason": reason,
	})
	c.JSON(http.StatusOK, gin.H{"userId": userID, "revoked": true})
}

// parseUserBanExpiresAt 解析 expiresAt。
//
// 接受两种形态（与 parseBlacklistExpiresAt 完全同构，复用同一套运营直觉）：
// RFC3339（2026-11-20T00:00:00Z）与纯日期（2026-11-20）。
// 纯日期按**当天 23:59:59（本地时区）**处理：运营写「封到 11 月 20 日」的意思是
// 20 日这天还有效，按 00:00 处理会让封禁在 20 日第一秒就解除，与直觉相反。
func parseUserBanExpiresAt(raw *string) (*time.Time, error) {
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
	return nil, errors.New("到期时间格式无效：请用 RFC3339 或 YYYY-MM-DD")
}

// writeUserBanAdminError 把 service 错误映射成 HTTP 状态。
//
// 映射规则（与黑名单/白名单同口径）：
//
//	非法输入（userId / reason / expiresAt） → 400（明确报错，不 clamp）
//	目标用户不存在                          → 404
//	数据库未初始化 / 表结构问题              → 503（不能假装成功）
//	其它                                    → 500（只记日志，不回原始错误）
func writeUserBanAdminError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "操作封禁记录失败"
	switch {
	case errors.Is(err, service.ErrUserBanInvalid):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrUserBanUserNotFound):
		status = http.StatusNotFound
		message = service.ErrUserBanUserNotFound.Error()
	case errors.Is(err, errUserBanAdminDBUnavailable), errors.Is(err, service.ErrUserBanDBUnavailable):
		status = http.StatusServiceUnavailable
		message = "封禁数据库暂不可用，请稍后再试"
	}
	if status >= http.StatusInternalServerError {
		logger.SysError("封禁管理操作失败: " + logger.RedactSecrets(err.Error()))
	}
	c.JSON(status, gin.H{"message": message})
}
