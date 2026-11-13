package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 设备指纹的**采集侧**接线（T-25 的另一半）。
//
// service/device_pool.go 已经备好 `RegisterDevice` / `CanUseDevice` /
// `CanUseDeviceWithSwitch` / `NormalizeDevicePoolDeviceID`，但全仓零生产调用方 ——
// 于是 `account_device_bindings` 永远是空表，上限判定恒为「不限」。
// 本文件只做一件事：**在既有的登录/换绑成功路径上，把设备登记这一次写挂上去**。
//
// ## 三条设计纪律
//
//  1. **不重写写入逻辑**，一律走 `service.RegisterDevice`（它内部已含
//     CanUseDevice 判定 + OnConflict 去重 + last_seen_at 刷新）。
//     自己另写一套 Create 会绕过唯一索引语义，正是 T-25 花力气避免的那类分叉。
//
//  2. **fail-open**：登记失败只记 `logger.SysLog`，绝不影响登录/换绑结果。
//     这不是「额外加固」而是与既有设计一致 —— device_pool.go 的判定本身就是
//     fail-open（表坏/查询出错一律放行），写侧没有理由更严。
//     设备池是**权益限制不是安全边界**：写不进去只意味着这次没统计到，
//     下次登录会再记一次；而把它变成登录失败会让「DB 抖一下全站登不进」。
//
//  3. **不阻塞主路径**：写入放独立 goroutine。登录是热路径，多一次同步 DB 写
//     就是多一次 p99 抬升；而设备统计晚 100ms 落地没有任何业务影响。
//
// ## ⚠️ goroutine 里为什么不能直接用请求的 *gorm.DB
//
// 请求句柄可能随响应结束被回收（也带着即将取消的 request context）。
// 必须重新取一个**干净的**句柄：`Session(&gorm.Session{NewDB: true})`
// 既清掉生成式 DAO 残留的 model/schema（否则 Create 跨 model 会
// `panic: reflect: Field index out of range`，本仓实测踩过），
// 又保留同一条连接。本文件照抄 `admin_login.go` 的 adminLoginDB() 写法。

// devicePoolDBOverride 允许测试注入数据库（nil = 走正常解析路径）。
//
// 与 adminLoginDBOverride 同一动机：本包大量用例会改写 dao 全局单例，
// 注入点让「异步写入落到哪个库」是确定的，而不是取决于用例执行顺序。
var devicePoolDBOverride *gorm.DB

// DevicePoolUserAgentHashBytes 是 User-Agent 哈希取前多少字节。
//
// 16 字节 = 32 位十六进制字符。碰撞概率对「同一 client_kind 下的设备区分」
// 完全够用（生日界下 2^64 量级），同时让 device_id 总长稳定在
// 「client_kind(≤13) + ':' + 32」≈ 46，远低于列宽 191。
const DevicePoolUserAgentHashBytes = 16

// devicePoolBackgroundDB 返回 goroutine 里可以安全使用的数据库句柄。
//
// 三档回落与 adminLoginDB() 一致：测试注入 → manager 裸句柄 → dao 底层连接。
// 每一档都强制 `NewDB: true`：清 Statement 的 model/schema，保留连接与事务上下文。
func devicePoolBackgroundDB() *gorm.DB {
	if devicePoolDBOverride != nil {
		return devicePoolDBOverride.Session(&gorm.Session{NewDB: true})
	}
	if mgr := manager.GetManager(); mgr != nil {
		if db := mgr.GetDB(); db != nil {
			return db.Session(&gorm.Session{NewDB: true})
		}
	}
	if dao.User == nil {
		return nil
	}
	raw := dao.User.UnderlyingDB()
	if raw == nil {
		return nil
	}
	return raw.Session(&gorm.Session{NewDB: true})
}

// devicePoolRequestClientKind 判定本次请求的调用来源（三值之一，永不返回空串）。
//
// 直接复用 T-11 的 `service.ResolveClientKind`，不自己写一套 header/UA 嗅探：
// 判定顺序（显式头 > 令牌记录 > 扩展特征 > 客户端特征 > unknown）是
// task_doc/111 §6.3 定案的，另写一份必然漂移。
func devicePoolRequestClientKind(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return service.ClientKindUnknown
	}
	return service.ResolveClientKind(service.ClientKindInput{
		DeclaredKind:   c.GetHeader(service.ClientKindHeader),
		UserAgent:      c.Request.UserAgent(),
		Origin:         c.GetHeader("Origin"),
		ScriptIdentity: c.GetHeader("X-Script-Identity"),
		ScriptToken:    c.GetHeader("X-Script-Token"),
	})
}

// BuildDevicePoolDeviceID 构造设备标识：`<client_kind>:<sha256(ua)[:16]>`。
//
// ## 为什么是「client_kind + UA 哈希」
//
//   - `client_kind` 单独不够：它只有 exam_client / extension / unknown 三值，
//     区分不了「两台都装了扩展的不同电脑」。
//   - 只哈希 UA 也不够：同一台机器上「考试客户端」与「浏览器扩展」是**两个不同的
//     使用入口**，用同一个 device_id 会让它们互相占槽。
//   - 两者拼接后：同一入口 + 同一 UA = 同一设备；换入口或换浏览器版本 = 新设备。
//
// ## 为什么**不用 IP**
//
//  1. IP 会变（DHCP / 移动网络 / NAT 出口切换），同一台设备一天能换好几个；
//     用它做设备标识会把「一个人」记成「好几台设备」，上限被自己正常使用打满。
//  2. 同一出口 IP（校园网 / 公司 NAT / 运营商大内网）下几十台设备共用一个 IP，
//     用 IP 做标识等于把所有人当成同一台设备 —— 上限彻底失效。
//  3. 本项目已有独立的 IP 段归属体系（location_cache_entries / ip_segments）
//     承载「这个 IP 是哪里的」这一语义。设备池再吃 IP 会让两套体系互相污染：
//     改 IP 段数据会连带改变设备计数。
//
// ## 为什么哈希
//
// UA 是长文本（新版 Chrome 可超 200 字节），直接存会顶到列宽 191 的上限并被
// 截断 —— 而截断会把两条不同的 UA 折成同一个前缀，静默并成一台设备。
// 哈希后定长 32 字符，没有截断风险，也不会因为 UA 里带版本号以外的内容而膨胀。
//
// 返回的字符串仍然要过 `service.NormalizeDevicePoolDeviceID`（调用方/service 内部会做），
// 保证空白与超长在落库前统一收敛到唯一索引认得的那一种字面量。
func BuildDevicePoolDeviceID(clientKind string, userAgent string) string {
	kind := service.NormalizeClientKind(clientKind)
	sum := sha256.Sum256([]byte(kind + "\x00" + strings.TrimSpace(userAgent)))
	return kind + ":" + hex.EncodeToString(sum[:DevicePoolUserAgentHashBytes])
}

// devicePoolDeviceIDOf 从请求里摘出设备标识。
func devicePoolDeviceIDOf(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return BuildDevicePoolDeviceID(devicePoolRequestClientKind(c), c.Request.UserAgent())
}

// registerAccountDeviceBindingNow 同步执行一次登记（goroutine 与测试共用）。
//
// 返回 error 只是为了能记日志/让测试观察；**业务调用方一律忽略**（见 recordAccountDeviceBinding）。
func registerAccountDeviceBindingNow(userID int32, deviceID string) error {
	if userID <= 0 || strings.TrimSpace(deviceID) == "" {
		return nil
	}
	db := devicePoolBackgroundDB()
	if db == nil {
		logger.SysLog(fmt.Sprintf("设备池登记跳过（fail-open）：userID=%d 取不到数据库句柄", userID))
		return nil
	}
	if _, err := service.RegisterDevice(db, userID, deviceID); err != nil {
		logger.SysLog(fmt.Sprintf("设备池登记失败（fail-open，不影响登录/换绑）：userID=%d err=%v", userID, err))
		return err
	}
	return nil
}

// recordAccountDeviceBinding 在**登录/换绑成功之后**登记本次设备（异步、fail-open）。
//
// 调用点约定：只在「已经确定成功、即将写响应」的位置调用一次；失败分支绝不调用
// （失败的登录不该在设备表里留痕，否则撞库会顺带把账号的设备额度打满）。
//
// 返回值恒为 nil（异步拿不到错误），签名保留返回值只是为了：
//   - 调用方可以写成 `_ = recordAccountDeviceBinding(...)` 显式表达「我不管这个错误」；
//   - 变异验证时可以把它改成同步并在调用方检查错误，从而证明判据④ 真的在测。
//
// **绝不要**把返回值接进响应分支 —— 那是把 fail-open 改成 fail-closed。
func recordAccountDeviceBinding(c *gin.Context, userID int32) error {
	deviceID := devicePoolDeviceIDOf(c)
	if userID <= 0 || deviceID == "" {
		return nil
	}
	go func() {
		_ = registerAccountDeviceBindingNow(userID, deviceID)
	}()
	return nil
}

// enforceAccountDeviceLimit 是设备池的**准入判定**（T-25 判定侧）。
//
// 与 recordAccountDeviceBinding 的分工：
//
//	enforceAccountDeviceLimit  同步、在签发 token **之前** —— 它决定这次能不能登录成功
//	recordAccountDeviceBinding 异步、在签发 token **之后** —— 它只负责登记
//
// 为什么准入必须同步：异步判定的结论写不进已经发出的响应，用户已经拿到 cookie 了，
// 再判「该拒绝」就只能靠前端补救 —— 那不是准入，是事后通知。
//
// 返回 true = 放行。以下三种情况一律放行（**fail-open**，与 device_pool.go:107 同一条纪律）：
//   - 开关 `device_limit_enabled` 关闭（默认关，见 CanUseDeviceWithSwitch）
//   - 设备标识拿不到（没有指纹就没法判「是不是新设备」）
//   - 判定过程出错（表不存在 / 查询失败）
//
// 被拒时本函数已经写好 403 响应，调用方只需 `return`。
func enforceAccountDeviceLimit(c *gin.Context, userID int32) bool {
	if c == nil || userID <= 0 {
		return true
	}
	deviceID := devicePoolDeviceIDOf(c)
	if deviceID == "" {
		return true
	}
	db := devicePoolBackgroundDB()
	if db == nil {
		return true
	}
	decision := service.CanUseDeviceWithSwitch(db, userID, deviceID)
	if decision.Allowed {
		return true
	}
	// 被拒：写清楚上限与已用数，让用户知道该删哪台设备。
	//
	// 用 403 而不是 400/401：这不是「你填错了」也不是「你没登录」，
	// 是「你的账号等级允许的设备台数用完了」—— 前端据此引导去个人中心清理设备。
	//
	// Limit < 0 是「禁止档」（Q16 遗留出口：0=不限，所以「禁止」只能用负数表达），
	// 那种情况下说「最多 N 台」是错的，得单独给一句。
	message := fmt.Sprintf("该账号最多可在 %d 台设备上使用，当前已用 %d 台。请先在个人中心移除不再使用的设备。", decision.Limit, decision.Used)
	if decision.Limit < 0 {
		message = "当前账号等级不允许在任何设备上使用，请联系管理员。"
	}
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"code":    "DEVICE_LIMIT_EXCEEDED",
		"message": message,
		"limit":   decision.Limit,
		"used":    decision.Used,
		// reason 是稳定原因串（service.DevicePoolReason*），排障时比 message 可靠。
		"reason": decision.Reason,
	})
	return false
}
