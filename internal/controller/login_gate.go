package controller

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// loginGate 把「两套登录限流」的判定收在一个对象里，避免调用方在每条失败分支上
// 各写一遍「记哪个维度、记几次、要不要回锁定提示」。
//
// 两套限流（45-*.md §0.1.4）：
//
//	account —— 普通模式，按 username 计数，**可被管理员关闭**
//	ip      —— 仅当客户端 IP 在**黑名单**中时对 /user/login 生效；
//	           对 /admin-login 恒生效。按 IP 计数，**不可关闭**
//
// 为什么做成对象而不是两个自由函数：`/user/login` 在一次失败里可能要同时记
// 两个维度（账号 + 被拉黑 IP），若各写一遍很容易漏掉其中一个，
// 而漏掉的那个维度就是一条免费爆破路径。
type loginGate struct {
	c     *gin.Context
	db    *gorm.DB
	now   time.Time
	ipKey string
	// accountPolicy 的 Enabled 可能为 false（管理员关闭了普通模式限流）。
	accountPolicy service.LoginRateLimitPolicy
	// ipPolicy 是本次请求使用的 **IP 维度参数**，按「是否被拉黑」在构造时选定：
	//
	//	被拉黑 IP / /admin-login  → service.IPLoginPolicy（严格，默认 5 次，Enabled 恒 true）
	//	普通 IP                  → service.NormalIPLoginPolicy（宽松，默认 20 次，
	//	                            Enabled 跟随 login_rate_limit_enabled）
	//
	// 两者**共用同一个 ip scope 与同一张表**，区别只在阈值与启用条件。
	ipPolicy service.LoginRateLimitPolicy
	// ipApplies 表示本次请求是否要计入 IP 维度。
	//
	// 改造后（45-*.md Q67）**所有 IP 都参与 IP 维度**，只有阈值不同 ——
	// 唯一完全不参与的是 IP 白名单里的地址（见 login_rate_limit_exempt_ips）。
	//
	// 为什么改：账号维度只能拦住「针对单个账号」的爆破。password spraying
	// 对 N 个账号各试少数几次，每个账号都不到 10 次阈值，此前全部放行。
	// 现在普通 IP 用宽松阈值（20）在 IP 维度上兜住总量。
	//
	// 白名单（login_rate_limit_exempt_ips）是为大公司 NAT 出口留的逃逸口：
	// 那里的 IP 完全不参与 IP 维度（账号维度仍生效）。
	ipApplies bool
}

// beginLoginRateLimit 做前置锁定检查；已锁定则直接写响应并返回 nil。
//
// 调用方约定：
//
//	gate := beginLoginRateLimit(c)
//	if gate == nil { return }   // 已回 429
//	... 失败分支调 gate.failAccount(username, msg) ...
//	... 成功分支调 gate.succeed(username) ...
func beginLoginRateLimit(c *gin.Context) *loginGate {
	return beginLoginRateLimitWith(c, true)
}

// beginLoginRateLimitAdminEntry 是 /admin-login 的入口：**只查 IP 维度**。
//
// 该入口不受账号维度约束（45-*.md §0.1.4 五-6）：它是黑名单豁免的独立通道，
// 若也按账号计数，攻击者故意输错某管理员用户名就能顺带锁死他从这个入口进来。
func beginLoginRateLimitAdminEntry(c *gin.Context) *loginGate {
	return beginLoginRateLimitWith(c, false)
}

func beginLoginRateLimitWith(c *gin.Context, includeAccount bool) *loginGate {
	db := adminLoginDB()
	if db == nil {
		// 没有数据库就无法限流。放行（fail-open）—— 限流是防护增强，
		// 不该在 DB 不可用时把所有人挡在门外。
		return &loginGate{c: c, now: time.Now()}
	}
	gate := &loginGate{
		c:             c,
		db:            db,
		now:           time.Now(),
		ipKey:         service.NormalizeLoginIPKey(strings.TrimSpace(c.ClientIP())),
		ipApplies:     true,
		accountPolicy: service.AccountLoginPolicy(db),
	}
	if !includeAccount {
		// /admin-login：只查 IP 维度，且恒用**严格**阈值（该入口不可关闭）。
		gate.accountPolicy.Enabled = false
		gate.ipPolicy = service.IPLoginPolicy(db)
	} else {
		// /user/login 与 /user/login/sms：三档判定（45-*.md Q67）。
		switch {
		case service.IsLoginRateLimitExemptIP(db, gate.ipKey):
			// 白名单：**完全不参与** IP 维度限流（账号维度仍生效）。
			gate.ipApplies = false
			gate.ipPolicy = service.NormalIPLoginPolicy(db)
		case isIPBlacklistedForLogin(db, gate.ipKey):
			// 被拉黑 IP 走普通登录：用**严格**阈值（默认 5，不可关闭）。
			gate.ipPolicy = service.IPLoginPolicy(db)
		default:
			// 普通 IP：用**宽松**阈值（默认 20，跟随普通模式开关）。
			// 这一档是 Q67 新增的 —— 它负责拦住 password spraying。
			gate.ipPolicy = service.NormalIPLoginPolicy(db)
		}
	}

	// 前置检查：任一维度已锁定即拒绝。
	// 顺序：先 IP 后账号 —— IP 维度的锁定影响面更大（整个出口），优先提示更有意义。
	if gate.ipApplies && gate.ipKey != "" && gate.ipPolicy.Enabled {
		if lockout, err := service.CheckLoginLockoutWithDB(db, gate.ipPolicy, gate.ipKey, gate.now); err == nil && lockout.Locked {
			respondLoginLocked(c, lockout)
			return nil
		}
	}
	return gate
}

// beginLoginRateLimitForAccount 是 /user/login 与 /user/login/sms 的入口：
// 在**拿到 username/phone 之后**追加账号维度的前置锁定检查。
//
// ⚠️ 账号维度**必须**在解析出 key 之后立刻检查，而不能等到失败分支才查：
// 否则「已锁定」的账号只要提交**正确密码**就能登录成功 —— 锁定形同虚设。
// 这条是实测发现的缺陷（第一版把账号检查放在 failAccount 里，测试立刻变红）。
//
// 返回 false 表示已写出锁定响应，调用方直接 return。
func (g *loginGate) checkAccountLocked(username string) bool {
	if g == nil {
		return false
	}
	return g.lockedAccount(username)
}

// isIPBlacklistedForLogin 判断该 IP 是否在黑名单中（决定 /user/login 是否叠加 IP 维度）。
//
// 开关关闭时返回 false（黑名单关闭 → 没有「被拉黑 IP」这个概念）。
func isIPBlacklistedForLogin(db *gorm.DB, ipKey string) bool {
	if ipKey == "" {
		return false
	}
	cache := service.DefaultBlacklistCache()
	if cache == nil || !cache.Enabled() {
		return false
	}
	snapshot := cache.Snapshot()
	if snapshot == nil {
		return false
	}
	_, hit := snapshot.MatchIP(ipKey)
	return hit
}

// failAccount 记一次账号维度失败（并在 IP 维度适用时同时记一次）。
//
// ⚠️ **响应只写一次**，由本函数在最末尾统一写。
//
// 这里曾有一个真实缺陷：`failIP` 在**非锁定**时也会写 400 响应，
// 于是账号维度随后再写一次 → 响应体里出现两段 JSON
// （实测：`{"message":"用户名或密码错误",...}{"code":429,...}`），
// 前端解析必然失败。修法是把「写响应」的职责从 failIP 收回，
// failIP 只在**触发锁定**时写 429（那是终止性响应），
// 普通失败一律由调用方在这条唯一出口写。
func (g *loginGate) failAccount(username string, message string) {
	if g == nil {
		return
	}
	if g.lockedAccount(username) {
		// ⚠️ 这条早退**在正常情况下走不到**：controller 层（`user.go:383`）已经先用
		// `checkAccountLocked` 拦过一次，锁定后的请求根本不会进入凭据校验流程。
		// 保留它是防御「调用方忘了前置检查」。
		//
		// **仍然记录一条失败事件**：这是本函数唯一能观察到「锁定后又来一次」的机会。
		// 虽然当前调用链到不了这里，但一旦有新的调用方绕过 `checkAccountLocked`，
		// 这里就是最后的审计点 —— 与「撞库最密集阶段发生在锁定之后」的判断一致。
		g.recordLoginFailureEvent(username, message, true)
		return
	}
	// 在最开头记录：**必须在任何 return 之前**。
	//
	// 理由：本次失败可能因为「触发锁定」而在下面提前 return（`recordIPFailure`
	// 或 `recordAccountFailureLocked` 返回 locked=true）。若把审计写在 return 之后，
	// 那些**恰好触发锁定的失败**就一条记录都没有 —— 而它们恰恰是撞库的峰值。
	g.recordLoginFailureEvent(username, message, false)
	// IP 维度先记：它可能触发锁定并写出 429（终止性响应）。
	var ipLockout *service.LoginLockout
	if g.ipApplies {
		if lockout, locked := g.recordIPFailure(); locked {
			return // 已写 429
		} else if lockout != nil {
			ipLockout = lockout
		}
	}
	// 账号维度再记：同样可能触发锁定。
	accountLockout, accountLocked := g.recordAccountFailureLocked(username)
	if accountLocked {
		return // 已写 429
	}
	// 两个维度都没锁 → 写一次普通失败响应（用账号维度的剩余次数，
	// 没有账号维度时回落到 IP 维度）。
	effective := accountLockout
	if effective.MaxAttempts == 0 {
		if ipLockout != nil {
			effective = *ipLockout
		}
	}
	g.respondFailure(message, effective)
}

// failIPOnly 记一次 IP 维度失败（/admin-login 用）。
//
// ⚠️ 这条路径**不知道 username**（/admin-login 的失败可能发生在账号查不到之前），
// 所以审计里 username 写空串 —— 空串的语义是「本次失败与某个具体账号名无关」，
// 不是「漏填」。
func (g *loginGate) failIPOnly(message string) {
	if g == nil {
		return
	}
	// 必须在 failIP 之前记录：failIP 触发锁定时会提前 return，
	// 写在它后面会让「恰好触发锁定」的那次失败没有记录。
	g.recordLoginFailureEvent("", message, false)
	g.failIP(message)
}

// recordLoginFailureEvent 把一次登录失败写进 login_failure_events（R9 的数据源）。
//
// # 这是旁路审计：任何失败都不得影响登录响应
//
// 三个原则：
//  1. **不改变响应**：写失败只进 SysWarn（service 层负责），本函数不 return error、
//     不影响后续的 respondFailure/429。
//  2. **不 panic**：g.c 或 g.db 为 nil 时直接返回（beginLoginRateLimit 在 DB 不可用
//     时会返回一个只有 c/now 的 gate，那种情形必须容忍）。
//  3. **reason 从 message 近似推断**：准确做法是让 9 个调用点各自传 reason，但那要改
//     9 处 controller 分支（任务书要求「一个都不动」）。已知局限写在 mapLoginFailureReason。
func (g *loginGate) recordLoginFailureEvent(username, message string, locked bool) {
	if g == nil || g.db == nil || g.c == nil {
		return
	}
	clientIP := strings.TrimSpace(g.c.ClientIP())
	if clientIP == "" {
		return
	}
	// user_id：能查到就是真实用户，查不到就是 0（= 账号不存在）。
	//
	// ⚠️ 这里的查询**必须容忍失败**：它是审计的附加信息，不能因为它出错就让登录受影响。
	//
	// ⚠️ 这里用 `Pluck("id", &userID)` 而不是 `Select("id").Scan(&found)`：
	// 后者在本仓实测报 `unsupported data type: <nil>`（GORM 的 Scan 对「只选一列
	// 到一个完整实体」的形态支持不佳），错误被吞掉后 userID 恒为 0 ——
	// 后果是**所有真实用户的失败都被记成「账号不存在」**，R9 的 user_id 维度全错。
	// `Pluck` 是 GORM 里取单列的标准做法。
	var userID int32
	if trimmedUsername := strings.TrimSpace(username); trimmedUsername != "" {
		if err := g.db.Session(&gorm.Session{NewDB: true}).
			Model(&entity.User{}).
			Where("username = ?", trimmedUsername).
			Limit(1).Pluck("id", &userID).Error; err != nil {
			userID = 0
		}
	}
	event := entity.LoginFailureEvent{
		CreatedAt: g.now,
		ClientIP:  clientIP,
		UserID:    userID,
		Username:  strings.TrimSpace(username),
		Scene:     g.failureScene(),
		Reason:    mapLoginFailureReason(message, userID),
		Locked:    locked,
	}
	// 返回值刻意丢弃：旁路契约要求「写失败不影响响应」。
	_ = service.RecordLoginFailureEventWithDB(g.db, event)
}

// failureScene 推断当前失败属于哪个登录场景。
//
// 依据请求路径而不是调用方传参（同样是为了不改 9 处 controller 分支）：
//   - /admin-login → admin_password
//   - 短信登录路径（URL 含 sms）→ sms
//   - 其余 → password
func (g *loginGate) failureScene() string {
	if g == nil || g.c == nil || g.c.Request == nil || g.c.Request.URL == nil {
		return entity.LoginFailureScenePassword
	}
	path := strings.ToLower(g.c.Request.URL.Path)
	switch {
	case strings.Contains(path, "admin-login"):
		return entity.LoginFailureSceneAdminPassword
	case strings.Contains(path, "sms"):
		return entity.LoginFailureSceneSMS
	default:
		return entity.LoginFailureScenePassword
	}
}

// mapLoginFailureReason 把失败文案近似映射成原因码。
//
// ⚠️ **已知局限（必须如实记录）**：
// 这是「从中文文案反推原因」，文案一改映射就失效。但这**不影响 R9 的判据** ——
// R9 的计数不区分原因（所有失败同等计入，见 entity.LoginFailureEvent 的注释），
// reason 只用于人工排查。所以这里的映射偏差不会造成漏报或误报，
// 只会让排障时看到的分类标签不够精确。
//
// 准确的解法是让 9 个 controller 调用点各自传 reason（`failAccount(username, message, reason)`），
// 那要改 9 处分支 —— 按任务书要求本轮不做，已在汇报里标为「将来可改进项」。
//
// userID 参与判断：username 查不到用户（userID==0）时，「用户名或密码错误」
// 一定属于账号不存在（user_not_found）而不是密码错 —— 这是 R9 区分撞库与
// 定向爆破的关键，不能糊成 bad_password。
func mapLoginFailureReason(message string, userID int32) string {
	text := strings.TrimSpace(message)
	switch {
	case strings.Contains(text, "已被禁用"):
		return entity.LoginFailureReasonAccountDisabled
	case strings.Contains(text, "未绑定"):
		return entity.LoginFailureReasonPhoneNotBound
	case strings.Contains(text, "非管理员") || strings.Contains(text, "仅限管理员"):
		return entity.LoginFailureReasonNotAdmin
	case strings.Contains(text, "验证码"):
		return entity.LoginFailureReasonCaptchaFailed
	case strings.Contains(text, "用户名或密码错误"):
		// 这一句同时覆盖「账号不存在」与「密码错误」。用 userID 区分：
		// 查不到用户 ⇒ 账号不存在（撞库特征）；查得到 ⇒ 密码错。
		if userID == 0 {
			return entity.LoginFailureReasonUserNotFound
		}
		return entity.LoginFailureReasonBadPassword
	default:
		return entity.LoginFailureReasonOther
	}
}

// failIP 记一次 IP 维度失败；**只在触发锁定时写响应**（429），否则返回统计供调用方写。
//
// 唯一例外是 failIPOnly（/admin-login）：那里没有账号维度，所以由它自己写普通失败响应。
func (g *loginGate) failIP(message string) {
	if lockout, locked := g.recordIPFailure(); locked {
		return
	} else if lockout != nil {
		g.respondFailure(message, *lockout)
	}
}

// recordIPFailure 记一次 IP 维度失败。
//
// 返回值：
//
//	locked=true  → **已写出 429**，调用方必须立刻 return（终止性响应）
//	lockout!=nil → 未锁定，统计结果交给调用方决定何时写响应
//	两者都空     → 该维度不适用 / 已关闭 / 写库失败，调用方按「无 IP 统计」处理
func (g *loginGate) recordIPFailure() (lockout *service.LoginLockout, locked bool) {
	if g == nil || g.db == nil || !g.ipPolicy.Enabled || !g.ipApplies || g.ipKey == "" {
		return nil, false
	}
	result, err := service.RecordLoginFailureWithDB(g.db, g.ipPolicy, g.ipKey, g.now)
	if err != nil {
		logger.SysWarn("记录 IP 维度登录失败次数失败：" + err.Error())
		return nil, false
	}
	if result.Locked {
		respondLoginLocked(g.c, result)
		return nil, true
	}
	return &result, false
}

// lockedAccount 检查账号维度是否已锁定；已锁定则写响应。
func (g *loginGate) lockedAccount(username string) bool {
	if g == nil || g.db == nil || !g.accountPolicy.Enabled {
		return false
	}
	key := service.NormalizeLoginAccountKey(username)
	if key == "" {
		return false
	}
	lockout, err := service.CheckLoginLockoutWithDB(g.db, g.accountPolicy, key, g.now)
	if err != nil || !lockout.Locked {
		return false
	}
	respondLoginLocked(g.c, lockout)
	return true
}

// recordAccountFailureLocked 记一次账号维度失败。
//
// 返回 (lockout, locked)：
//
//	locked=true  → **已写出 429**
//	其余         → 未锁定，lockout.MaxAttempts==0 表示该维度不适用（开关关闭/空 key）
func (g *loginGate) recordAccountFailureLocked(username string) (service.LoginLockout, bool) {
	if g == nil || g.db == nil || !g.accountPolicy.Enabled {
		return service.LoginLockout{}, false
	}
	key := service.NormalizeLoginAccountKey(username)
	if key == "" {
		// 空 username 不计数（否则所有空用户名请求共用一个桶）。
		return service.LoginLockout{}, false
	}
	lockout, err := service.RecordLoginFailureWithDB(g.db, g.accountPolicy, key, g.now)
	if err != nil {
		logger.SysWarn("记录账号维度登录失败次数失败：" + err.Error())
		return service.LoginLockout{}, false
	}
	if lockout.Locked {
		respondLoginLocked(g.c, lockout)
		return lockout, true
	}
	return lockout, false
}

// respondFailure 写普通的失败响应（400 + 剩余次数）。
//
// **不暴露账号存在性**（45-*.md §0.1.4 五-4）：无论账号存在与否、
// 无论锁的是账号还是 IP，文案与结构完全一致。
func (g *loginGate) respondFailure(message string, lockout service.LoginLockout) {
	if g == nil || g.c == nil {
		return
	}
	payload := gin.H{"message": message}
	// 只有知道阈值时才给剩余次数（被关闭的限流 / 空 policy 时不给，避免显示 -1）。
	if lockout.MaxAttempts > 0 {
		remaining := lockout.MaxAttempts - lockout.FailCount
		if remaining < 0 {
			remaining = 0
		}
		payload["remainingAttempts"] = remaining
	}
	g.c.JSON(400, payload)
}

// succeed 在登录成功时清零两个维度（含 window_start 重置，「连续」语义）。
func (g *loginGate) succeed(username string) {
	if g == nil || g.db == nil {
		return
	}
	if g.accountPolicy.Enabled {
		if key := service.NormalizeLoginAccountKey(username); key != "" {
			if err := service.ClearLoginAttemptsOnSuccessWithDB(g.db, g.accountPolicy, key, g.now); err != nil {
				logger.SysWarn("清除账号维度登录失败计数失败（不影响本次登录）：" + err.Error())
			}
		}
	}
	if g.ipApplies && g.ipKey != "" {
		if err := service.ClearLoginAttemptsOnSuccessWithDB(g.db, g.ipPolicy, g.ipKey, g.now); err != nil {
			logger.SysWarn("清除 IP 维度登录失败计数失败（不影响本次登录）：" + err.Error())
		}
	}
}

// 编译期断言：entity 包被本文件用于 scope 常量（避免 import 被误删）。
var _ = entity.LoginAttemptScopeAccount
