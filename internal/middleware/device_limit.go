package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// DeviceIDHeader 是客户端上报设备标识的请求头（137 §6、141 §3.1）。
//
// 值来自扩展/客户端的**随机生成** uuid（141 R1），服务端只做归一化与比对，
// 绝不从 User-Agent/IP 反推设备身份 —— 那是「同一个人被记成好几台设备」
// 与「整个校园网被当成一台设备」的成因（device_pool_hook.go 的
// BuildDevicePoolDeviceID 注释里记着这笔账）。
const DeviceIDHeader = "X-Device-Id"

// DeviceInstanceHeader 是客户端上报「本次运行实例」的请求头（141 §4.3）。
//
// ⚠️ 它与 DeviceIDHeader 的语义**必须分开**，这是整个冲突检测成立的前提：
//
//	device_id  → 持久化（chrome.storage），表示「这个浏览器 profile / exe 宿主」
//	instance   → **每次启动新生成、绝不落盘**，表示「这次运行」
//
// 镜像复制会让 device_id 一起被复制，但 instance 每次启动都新生成，
// 所以「同 device_id + 不同 instance」才能识别出「一套镜像被复制多份」。
// 若 instance 也持久化，镜像两份的 instance 相同 → 被判成「同一实例重连」
// → 冲突检测完全失效（141 §4.3 的局限说明）。
const DeviceInstanceHeader = "X-Device-Instance"

// DeviceIDRequiredCode / DeviceIDRequiredMessage 是「无头请求」的拒绝体。
//
// 文案是产品需求：老客户端不带 header 时，用户必须知道「去升级」而不是
// 「设备坏了」。与 137 §5 第 6 步「设备数已满」的文案同一个动机。
const (
	DeviceIDRequiredCode    = "DEVICE_ID_REQUIRED"
	DeviceIDRequiredMessage = "请升级客户端以支持设备识别"
)

// DeviceLimit 是设备池**准入**中间件（137 §5 + 141 §9）。
//
// # 这是全批唯一会改变所有现存客户端行为的改动
//
// 当前始终启用，按账号的有效设备上限判定：
//
//	限设备账号 + 不带 X-Device-Id → 403（老客户端必须升级）
//	不限量账号（limit=0）        → 不受数量约束，显式拉黑仍拒绝
//	考试脚本（script_session）   → 不受数量约束，显式拉黑仍拒绝
//
// 所以上线前必须确认「第 0 步的 limit==0 豁免」是有效的：4 个种子等级的
// device_limit 默认全是 0，写错顺序 = 上线瞬间全站 403（141 §9.2）。
//
// # 判定顺序（141 §9.1，**顺序本身就是需求**）
//
//	前  置：辨识账号（拿不到账号就没有可判定对象，交给后续 handler 鉴权）
//	前置二：带设备头时校验显式拉黑，不受不限量豁免影响
//	第 0 步：EffectiveDeviceLimit ⇒ limit == 0（不限）→ 【直接放行】不判不发 403
//	第 1 步：脚本请求（script_session）→ 放行
//	第 2 步：没有 X-Device-Id → 403「请升级客户端以支持设备识别」
//	第 3 步：设备池判定（service.CanUseDevice / service.RegisterDevice，唯一实现）
//
// ⚠️ 第 0 步**必须**排在第 2 步之前。反例（会导致全站 403）：
//
//	if deviceID == "" { return reject403() }   // ← 不限量账号在这里就被拒了
//	limit, _, _ := EffectiveDeviceLimit(...)   // ← 永远走不到
//	if limit == 0 { return allow() }
//
// ⚠️ 第 0 步的判据必须是 `limit == 0` 这**一个**条件，严禁写成
// `source == "level" && limit == 0` 或任何枚举 source 的形式：limit=0 有三种来源
// （level / user_override / level_missing，见 141 §9.3），枚举必漏将来新增的 source。
//
// # 「前置」两项为什么不算破坏上面的顺序
//
// 账号辨识决定「这次请求有没有可判定对象」。
// 显式拉黑先行，避免「不限量」意外恢复被拉黑设备的使用权。
// 账号辨识必须先于第 0 步，因为 EffectiveDeviceLimit 需要 userID；
// 它读取的 script_session 标记在第 1 步才被使用 —— 判定顺序与信息获取顺序
// 是两件事，混为一谈会让「顺序防线」（S13d）退化成一个摆设。
//
// # 成本
//
// 不再读取历史设备池总开关；设备上限直接按当前等级与用户设置判定。
func DeviceLimit() gin.HandlerFunc {
	return deviceLimitHandler(nil)
}

// DeviceLimitWith 返回使用指定数据库句柄的中间件（测试与显式装配用）。
//
// db 显式传入而不是只走 manager 单例：manager.GetManager() 在单测里是 nil，
// 没有注入点就无法用真实库驱动「limit=0 三种来源」的判据（同
// blacklistHandler 的 blacklistDeps.db 取舍）。
func DeviceLimitWith(db *gorm.DB) gin.HandlerFunc {
	return deviceLimitHandler(db)
}

// deviceLimitDatabase 解析本次请求可用的数据库句柄。
//
// 生产路径走 manager 单例；拿不到时返回 nil，由调用方按 fail-open 放行 ——
// 设备池是**权益限制不是安全边界**（device_pool.go 文件头第 2 条），
// 拿不到句柄就拦人等于「DB 抖一下全站搜不了题」。
func deviceLimitDatabase(injected *gorm.DB) *gorm.DB {
	if injected != nil {
		return injected
	}
	if mgr := manager.GetManager(); mgr != nil {
		return mgr.GetDB()
	}
	return nil
}

func deviceLimitHandler(injected *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c == nil || c.Request == nil {
			c.Next()
			return
		}
		db := deviceLimitDatabase(injected)
		if db == nil {
			logger.SysLog("设备门禁降级放行（fail-open）：取不到数据库句柄")
			c.Next()
			return
		}

		// ── 前置：辨识账号，显式设备拉黑不受容量豁免影响 ──
		userID, scriptSession, tokenID, ok := deviceLimitIdentity(c, db)
		if !ok {
			// 辨识不到（匿名请求 / 凭据无效）：设备池按**账号**计，没有账号就没有
			// 可判定的对象。这里不写 401 —— 鉴权语义属于后续 handler
			// （/api/search 支持匿名默认账号，在这里 401 会把该功能打死）。
			c.Next()
			return
		}
		if blocked := service.CheckDeviceBlockWithDB(db, userID, c.GetHeader(DeviceIDHeader)); !blocked.Allowed {
			deviceLimitReject(c, blocked)
			return
		}
		// Script leases are independently authorized. Even an unlimited account
		// cannot turn a script request header into an ordinary binding.
		if scriptSession {
			c.Next()
			return
		}

		// ══════════ 第 0 步：limit == 0（不限）→ 直接放行 ══════════
		//
		// 必须在「无头检查」之前（141 §9.2）：4 个种子等级的 device_limit 默认全为 0，
		// 顺序写反 = 所有不限量账号在无头请求上被拒 = 上线瞬间全站 403。
		// source 被刻意丢弃（`_`）：判据只看 limit，不看来源。把 source 接进
		// 判定条件就是 141 §9.3 明令禁止的写法（枚举必漏将来新增的 source）。
		limit, _, err := service.EffectiveDeviceLimit(db, userID)
		if err != nil {
			// 读不到上限（表缺列/查询失败）：fail-open 放行并留痕。
			logger.SysLog("设备门禁降级放行（fail-open）：读取设备上限失败 userID=" +
				formatDeviceLimitUserID(userID) + " err=" + logger.RedactSecrets(err.Error()))
			c.Next()
			return
		}
		if limit == service.DevicePoolUnlimited {
			// 单一条件 limit == 0，不枚举 source（见函数头的第二条 ⚠️）。
			//
			// 141 §9.4：豁免 ≠ 不登记。带 device 头时异步登记一次（fail-open），
			// 让管理员在「设备管理」页仍能看到这些账号的设备；不带头的请求无从登记。
			deviceLimitRegisterUnlimited(db, userID, c.GetHeader(DeviceIDHeader), tokenID, c.ClientIP())
			c.Next()
			return
		}

		// ══════════ 第 2 步：没有 X-Device-Id → 403 ══════════
		//
		// 只有走到这里才拒绝：limit==0 的账号已在第 0 步放行。
		deviceID := service.NormalizeDevicePoolDeviceID(c.GetHeader(DeviceIDHeader))
		if deviceID == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    DeviceIDRequiredCode,
				"message": DeviceIDRequiredMessage,
				// limit 一并回传：客户端据此判断「升不升级」是否影响自己
				// （不限量账号永远拿不到这个响应，所以不存在泄露面）。
				"limit":  limit,
				"reason": "device-id-required",
			})
			return
		}

		// ══════════ 第 3 步：设备池判定（service 层唯一实现，不在这里重写） ══════════
		decision := service.CanUseDeviceWithClientIP(db, userID, deviceID, c.ClientIP())
		if !decision.Allowed {
			deviceLimitReject(c, decision)
			return
		}
		// 新设备且有空槽：必须落一条绑定行，否则「已用台数」永远是 0，
		// 上限形同虚设（每一次请求都会被判成「新设备且未满」）。
		// 已绑定时 CanUseDeviceWithClientIP 内部已刷新最近使用时间与合法来源 IP，
		// 不再重复写 —— 热路径上少一次 UPDATE。
		if decision.Reason == service.DevicePoolReasonWithinLimit {
			// ⚠️ 这里**不能**用 `_` 丢弃返回的 decision（2026-11-15 修，C2 接线缺口）。
			//
			// RegisterDeviceWithOptions 内部第一条就是 DecideDeviceRegistration：
			// 判定冲突时它返回 reason=device-id-conflict 且**不写任何行**。
			// 原先写成 `if _, err := ...` 把这个结论整个丢掉 —— 于是冲突检测虽然
			// 实现完整、单测全绿，但在真实请求路径上**永远发不出 409**，客户端
			// 只会看到「设备没登记上」，下次请求又被判成新设备，槽位静默不增长。
			//
			// 409 是 141 §6.3 的契约：客户端收到它要重新生成 device_id 并重试一次
			// （扩展侧 device-identity.ts 的实现依赖这个响应）。
			registered, err := service.RegisterDeviceWithOptions(db, userID, deviceID,
				service.DeviceBindingRegisterOptions{
					OwnerKind:      entity.DeviceBindingOwnerKindToken,
					TokenID:        tokenID,
					ClientInstance: c.GetHeader(DeviceInstanceHeader),
					// 来源 IP：与 controller.recordAccountDeviceBinding 同源（c.ClientIP
					// 已按可信代理名单解析），让「我的设备」页能显示设备从哪儿连进来。
					ClientIP: service.NormalizeDeviceClientIP(c.ClientIP()),
				})
			if err != nil {
				// 判定已放行、只是这一笔没写进去：fail-open（同 RegisterDevice 的契约）。
				logger.SysLog("设备登记失败（fail-open，不影响本次请求）：userID=" +
					formatDeviceLimitUserID(userID) + " err=" + logger.RedactSecrets(err.Error()))
			} else if !registered.Allowed {
				if registered.Reason == service.DevicePoolReasonDeviceIDConflict {
					body := service.DeviceConflictDecisionBody()
					c.AbortWithStatusJSON(http.StatusConflict, gin.H{"code": body["code"], "message": body["message"]})
				} else {
					// A block or capacity change may have committed since the first check.
					deviceLimitReject(c, registered)
				}
				return
			}
		}
		c.Next()
	}
}

// deviceLimitReject 写出设备池的拒绝响应（403）。
//
// 用 403 而不是 400/401：「不是填错、不是没登录，是这台设备不给你用」。
// 与 controller.enforceAccountDeviceLimit 的响应结构保持一致（同义来源不同入口，
// 前端只需处理一种形状），但**不共用函数**：controller 依赖 middleware 会成环。
func deviceLimitReject(c *gin.Context, decision service.DevicePoolDecision) {
	message := decision.Message
	if strings.TrimSpace(message) == "" {
		message = "该设备当前无法使用，请在个人中心移除不用的设备后重试"
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"code":    "DEVICE_LIMIT_EXCEEDED",
		"message": message,
		"limit":   decision.Limit,
		"used":    decision.Used,
		"reason":  decision.Reason,
	})
}

// deviceLimitIdentity 从请求里辨识出「这次请求是谁、是不是脚本会话」。
//
// 两个来源，按可靠性排序：
//
//  1. gin.Context 里的 "user"（Auth / AuthAccessToken / AuthOrAccessToken 已解析）。
//     这是绝大多数挂载点的情况，零额外查询。
//  2. 自己解析一次 Authorization + X-Script-Identity。仅 /api/search 需要：
//     那条路由**没有**鉴权中间件（它支持匿名默认账号，提前 401 会打死该功能），
//     凭据在 handler 内部才解析。不在这里补一次解析的话，设备门禁在最主要的
//     搜题入口上会完全失效 —— 那正是本中间件存在的理由。
//
// 解析失败一律返回 ok=false（交给 handler 按原逻辑给出 401/匿名语义），
// 本函数**从不**写响应。
//
// 返回值 tokenID 用于登记行的 owner_kind=token / token_id（137 §3.2）。
func deviceLimitIdentity(c *gin.Context, db *gorm.DB) (userID int32, scriptSession bool, tokenID int32, ok bool) {
	if c == nil {
		return 0, false, 0, false
	}
	if value, exists := c.Get("user"); exists {
		if user, isUser := value.(*entity.User); isUser && user != nil && user.ID > 0 {
			// script_session 由 AuthAccessToken / AuthOrAccessToken 在
			// X-Script-Identity 解析成功时写入（user.go:292/328）。
			_, isScript := c.Get("script_session")
			return user.ID, isScript, deviceLimitTokenID(c), true
		}
	}

	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	identity := strings.TrimSpace(c.GetHeader("X-Script-Identity"))
	if authorization == "" && identity == "" {
		return 0, false, 0, false
	}
	authCtx, session, err := service.ResolveSearchTokenWithScriptIdentity(db, authorization, identity, c.ClientIP())
	if err != nil || authCtx.User.ID <= 0 {
		return 0, false, 0, false
	}
	return authCtx.User.ID, session.ID > 0, authCtx.AccessToken.ID, true
}

// deviceLimitTokenID 读上下文里的访问令牌 id（没有则 0）。
func deviceLimitTokenID(c *gin.Context) int32 {
	if c == nil {
		return 0
	}
	if value, exists := c.Get("access_token"); exists {
		if token, isToken := value.(*entity.AccessToken); isToken && token != nil {
			return token.ID
		}
	}
	return 0
}

// deviceLimitRegisterUnlimited 在「不 limit 的账号」带 device 头时异步登记一次。
//
// 141 §9.4：第 0 步放行时**可以**不登记（不限量账号不受约束），但那样管理员在
// 「设备管理」页看不到这些账号的设备。这里取「带 device 头就登记」的折中：
// 可见性保留，限流仍然不生效。
//
// 异步 + fail-open：登记失败只留痕，绝不影响本次请求（与
// controller.recordAccountDeviceBinding 同一纪律）。goroutine 里必须重新取
// 一个干净句柄（Session{NewDB:true}），否则跨 model 复用会
// `panic: reflect: Field index out of range`（本仓实测踩过）。
func deviceLimitRegisterUnlimited(db *gorm.DB, userID int32, rawDeviceID string, tokenID int32, clientIP string) {
	deviceID := service.NormalizeDevicePoolDeviceID(rawDeviceID)
	if db == nil || userID <= 0 || deviceID == "" {
		return
	}
	// 归一化在 goroutine 之前做（纯字符串运算，不需要请求上下文）；
	// 绝不能把 c 带进 goroutine —— 响应写完后 gin.Context 不可再读。
	normalizedIP := service.NormalizeDeviceClientIP(clientIP)
	go func() {
		fresh := db.Session(&gorm.Session{NewDB: true})
		if _, err := service.RegisterDeviceWithOptions(fresh, userID, deviceID,
			service.DeviceBindingRegisterOptions{
				OwnerKind: entity.DeviceBindingOwnerKindToken,
				TokenID:   tokenID,
				ClientIP:  normalizedIP,
			}); err != nil {
			logger.SysLog("不限量账号设备登记失败（fail-open，不影响本次请求）：userID=" +
				formatDeviceLimitUserID(userID) + " err=" + logger.RedactSecrets(err.Error()))
		}
	}()
}

// formatDeviceLimitUserID 把 userID 转成日志用字符串（避免 fmt 依赖进热路径）。
func formatDeviceLimitUserID(userID int32) string {
	if userID == 0 {
		return "0"
	}
	var digits []byte
	negative := userID < 0
	value := int64(userID)
	if negative {
		value = -value
	}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if len(digits) == 0 {
		return "0"
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
