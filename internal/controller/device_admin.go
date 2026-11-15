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

// 设备管理页的 HTTP 控制器（137-设备体系设计-授权与黑名单.md §8.2 / §9.2）。
//
// 四条路由（全部 Auth + middleware.RequireAdmin，见 internal/api/router.go）：
//
//	GET    /adapter-service/admin/devices                 全部设备（分页 + keyword/status）
//	POST   /adapter-service/admin/devices/:id/unbind      强制解绑（只断绑定，不封设备）
//	POST   /adapter-service/admin/device-bans             封禁（写黑名单 + 立即撤销全部绑定）
//	DELETE /adapter-service/admin/device-bans/:deviceId   解封（删黑名单规则）
//
// # ⚠️ 为什么封禁/解封必须用 /admin/device-bans 而不是 /admin/devices/ban
//
// gin 在同一棵**方法树**上不允许「静态段」与「参数段」处于同一层。
// `POST /admin/devices/:id/unbind` 会把 `devices/` 下的下一段变成参数节点，
// 再注册 `POST /admin/devices/ban` 就会在启动时 panic：
//
//	panic: 'ban' in new path '/api/admin/devices/ban' conflicts with existing
//	wildcard ':id' in existing prefix '/api/admin/devices/:id'
//
// 这不是可以绕过的配置问题：gin 的 radix 树结构决定了它。137 §8.3 给出的解法就是
// 换一个**独立静态前缀**（`/admin/device-bans`），本条照此实现。
//
// # 封禁与用户拉黑是两件事（红线 2）
//
// 这里封禁的 device_id 用户**无法自行解除**：它写的是全局黑名单（kind=device），
// 而用户端 `/user/devices/*` 只操作 user_device_blocks 与自己的绑定行。
// 两个入口互不可达，是结构性保证而不是约定。

// deviceAdminDB 返回当前数据库句柄；未初始化时返回 nil（调用方负责降级）。
//
// 与 blacklistAdminDB / userBanAdminDB 同构：单测里没有 manager 时不能 panic。
var deviceAdminDB = func() *gorm.DB {
	if mg := manager.GetManager(); mg != nil {
		return mg.GetDB()
	}
	return nil
}

var errDeviceAdminDBUnavailable = errors.New("数据库未初始化")

// requireDeviceAdminDB 取一个带优先级的会话。
//
// PriorityLow：设备管理页是纯运营操作，不该抢答题链路的数据库连接。
func requireDeviceAdminDB() (*gorm.DB, error) {
	db := deviceAdminDB()
	if db == nil {
		return nil, errDeviceAdminDBUnavailable
	}
	return withDatabasePriority(db, context.Background(), dbqueue.PriorityLow), nil
}

// AdminDeviceList 返回设备列表（管理员）。
//
// 响应形态：{data, total, page, pageSize, orphanBans, revocationSupported}
//   - orphanBans 是「有封禁规则但没有任何绑定行」的设备（否则那批设备
//     在管理页上不可见 ⇒ 永远无法解封，见 service.OrphanDeviceBan 注释）；
//   - revocationSupported=false 时前端必须禁用「解绑」按钮并解释原因。
func AdminDeviceList(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	db, err := requireDeviceAdminDB()
	if err != nil {
		writeDeviceAdminError(c, err)
		return
	}
	page, pageSize := blacklistPagination(c)
	// userId 是可选过滤（A4-2：用户管理页的「设备」按钮点进来时带上）。
	//
	// 解析失败**不报错、按不过滤处理**：这个参数是给「从用户行跳转」用的，
	// 一个手改的坏 URL 不该让整个设备列表 500。而「静默不过滤」在这种场景下
	// 是可接受的降级 —— 管理员看到的仍然是完整列表，只是没被收窄。
	//
	// ⚠️ 负数与 0 一律视为「不过滤」：userID 从 1 开始，0 是「不指定」的哨兵
	// （见 AdminDeviceListQuery.UserID 的注释）。
	var userID int32
	if raw := strings.TrimSpace(c.Query("userId")); raw != "" {
		if parsed, parseErr := strconv.ParseInt(raw, 10, 32); parseErr == nil && parsed > 0 {
			userID = int32(parsed)
		}
	}
	result, err := service.ListAdminDeviceBindingsWithDB(db, service.AdminDeviceListQuery{
		Keyword: strings.TrimSpace(c.Query("keyword")),
		Status:  strings.TrimSpace(c.Query("status")),
		UserID:  userID,
	}, page, pageSize)
	if err != nil {
		writeDeviceAdminError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

// AdminDeviceUnbind 强制解绑一台设备的绑定行（管理员）。
//
// 路径参数是绑定行主键 id（不是 device_id）：因为强制解绑的对象是
// 「这个账号的这台设备这一行」，而同一台设备可能被多个账号轮流用过。
// 用 device_id 解绑会一次断掉所有账号 —— 那是封禁的口径，不是解绑。
//
// ⚠️ 只写 revoked_at + revoked_reason='admin_unbound'，**绝不写黑名单**。
// 解绑后用户下次请求会静默重绑（137 §7），这是设计意图：解绑 = 释放槽位。
func AdminDeviceUnbind(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	raw := strings.TrimSpace(c.Param("id"))
	id, parseErr := strconv.ParseInt(raw, 10, 64)
	if parseErr != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数 id 无效"})
		return
	}
	db, err := requireDeviceAdminDB()
	if err != nil {
		writeDeviceAdminError(c, err)
		return
	}
	item, err := service.UnbindDeviceBindingWithDB(db, id, time.Now())
	if err != nil {
		writeDeviceAdminError(c, err)
		return
	}
	recordAccountAudit(c, &operator, "device", 2, "强制解绑设备", map[string]any{
		"bindingId":    item.ID,
		"userId":       item.UserID,
		"deviceId":     item.DeviceID,
		"revokedAt":    item.RevokedAt,
		"revokeReason": item.RevokedReason,
	})
	c.JSON(http.StatusOK, item)
}

// AdminDeviceBan 封禁一台设备（管理员）。
//
// 请求体（camelCase）：
//
//	{"deviceId":"exe:a1b2c3","reason":"工作室批量刷题","expiresAt":"2026-12-01"}
//
// 两件事在一个事务里完成（D12）：
//  1. 写 blacklist_entries(kind='device', value_hash=sha256(归一化 device_id))；
//  2. 把该 device_id 的**所有**绑定行写 revoked_at + revoked_reason='device_banned'。
//
// 完成后必须刷新进程内缓存 —— 否则要等 60 秒后台重载才生效，
// 期间「封禁成功」的提示是假的（46-*.md §4 的同一条纪律）。
func AdminDeviceBan(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	var input struct {
		DeviceID string `json:"deviceId"`
		// Reason 必填：封禁是不可自助解除的处置，审计里必须有原因。
		Reason string `json:"reason"`
		// ExpiresAt 空 = 永久。时长（durationAmount/durationUnit）与封禁页同构，
		// 但设备封禁默认永久，前端只在运营显式选择时才传。
		ExpiresAt      *string `json:"expiresAt"`
		DurationAmount int     `json:"durationAmount"`
		DurationUnit   string  `json:"durationUnit"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "请求体格式无效"})
		return
	}
	if strings.TrimSpace(input.DeviceID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "设备 ID 不能为空"})
		return
	}
	if strings.TrimSpace(input.Reason) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "封禁原因不能为空"})
		return
	}
	if input.DurationAmount < 0 {
		// 负数不能静默当成「永久」：那会让一次误操作变成不可撤销的处置。
		c.JSON(http.StatusBadRequest, gin.H{"message": "封禁时长不能为负数"})
		return
	}
	var expiresAt *time.Time
	if input.DurationAmount > 0 {
		// 时长的计算复用账号封禁的同一函数：设备封禁与账号封禁是同类语义，
		// 两套算法会让「封 7 天」在两张页面上差出时区/进位。
		resolved, durationErr := service.ResolveUserBanExpiresAt(input.DurationAmount, input.DurationUnit, time.Now())
		if durationErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": durationErr.Error()})
			return
		}
		expiresAt = resolved
	} else {
		parsed, expiresErr := parseBlacklistExpiresAt(input.ExpiresAt)
		if expiresErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": expiresErr.Error()})
			return
		}
		expiresAt = parsed
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		c.JSON(http.StatusBadRequest, gin.H{"message": "到期时间必须晚于当前时间（留空表示永久封禁）"})
		return
	}
	db, err := requireDeviceAdminDB()
	if err != nil {
		writeDeviceAdminError(c, err)
		return
	}
	result, err := service.BanDevice(db, service.DeviceBanRequest{
		DeviceID:  input.DeviceID,
		Reason:    input.Reason,
		ExpiresAt: expiresAt,
		CreatedBy: operator.ID,
	})
	if err != nil {
		writeDeviceAdminError(c, err)
		return
	}
	reloadBlacklistCacheAfterWrite(db)
	recordAccountAudit(c, &operator, "device", 3, "封禁设备", map[string]any{
		"entryId":         result.EntryID,
		"deviceMasked":    result.ValueMasked,
		"reason":          result.Reason,
		"expiresAt":       result.ExpiresAt,
		"revokedBindings": result.RevokedBindings,
	})
	c.JSON(http.StatusCreated, result)
}

// AdminDeviceUnban 解封一台设备（管理员）。
//
// 路径参数是 device_id（不是规则 id）：解封的语义是「这台设备可以再被使用」，
// 与「作废某一条规则」不同。同名设备只可能有一条生效规则（唯一索引），
// 所以按 device_id 解封是确定的。
//
// ⚠️ device_id 里可能有 `:` 等字符（`exe:a1b2…`），前端必须 encodeURIComponent；
// 这里再做一次 TrimSpace 兜住多余空白。
func AdminDeviceUnban(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !requireAdmin(c) {
		return
	}
	operator, ok := currentOperator(c)
	if !ok {
		return
	}
	deviceID := strings.TrimSpace(c.Param("deviceId"))
	if deviceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "设备 ID 不能为空"})
		return
	}
	db, err := requireDeviceAdminDB()
	if err != nil {
		writeDeviceAdminError(c, err)
		return
	}
	result, err := service.UnbanDevice(db, deviceID)
	if err != nil {
		writeDeviceAdminError(c, err)
		return
	}
	reloadBlacklistCacheAfterWrite(db)
	recordAccountAudit(c, &operator, "device", 4, "解封设备", map[string]any{
		"deviceId": result.DeviceID,
		"deleted":  result.Deleted,
	})
	c.JSON(http.StatusOK, gin.H{
		"deviceId": result.DeviceID,
		"deleted":  result.Deleted,
		// unban 是幂等的：本来没封也返回 200（deleted=0），
		// 因为运营看到「已解封」与「本来就没封」都不需要额外动作。
	})
}

// writeDeviceAdminError 把 service 错误映射成 HTTP 状态。
//
// 映射规则（与黑名单/封禁同口径）：
//
//	非法输入（deviceId / reason / expiresAt / status） → 400（明确报错，不 clamp）
//	绑定行不存在                                      → 404
//	数据库未初始化 / 表结构未就绪                      → 503（不能假装成功）
//	其它                                              → 500（只记日志，不回原始错误）
func writeDeviceAdminError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := "设备操作失败"
	switch {
	case errors.Is(err, service.ErrInvalidDeviceID),
		errors.Is(err, service.ErrInvalidBlacklistPattern):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, service.ErrDeviceBindingNotFound):
		status = http.StatusNotFound
		message = err.Error()
	case errors.Is(err, service.ErrDeviceBindingSchemaUnavailable),
		errors.Is(err, service.ErrDeviceRevocationUnavailable),
		errors.Is(err, errDeviceAdminDBUnavailable),
		errors.Is(err, service.ErrBlacklistCacheNoDatabase):
		status = http.StatusServiceUnavailable
		message = "设备数据暂不可用，请稍后再试"
	}
	if status >= http.StatusInternalServerError {
		logger.SysError("设备管理操作失败: " + logger.RedactSecrets(err.Error()))
	}
	c.JSON(status, gin.H{"message": message})
}
