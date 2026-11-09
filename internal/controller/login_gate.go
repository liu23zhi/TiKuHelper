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
		return
	}
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
func (g *loginGate) failIPOnly(message string) {
	if g == nil {
		return
	}
	g.failIP(message)
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
