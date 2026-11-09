package controller

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/dao"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/registry/manager"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// adminLoginDBOverride 允许测试注入数据库（nil = 走正常解析路径）。
//
// 为什么需要它：`adminLoginDB()` 依赖 manager 单例或 dao 全局，而这两者在
// 同包其它用例里会被改写（`dao.SetDefault` 是全局的），导致本文件的用例
// 可能拿到别的测试库。注入点让用例不依赖全局状态。
var adminLoginDBOverride *gorm.DB

// adminLoginDB 返回用于失败锁定计数的数据库句柄。
//
// ⚠️ 必须是**不带 DAO 元数据的干净 Session**，不能用 `dao.User.UnderlyingDB()`：
// 后者携带 `users` 表的 Statement（schema 已解析成 User 模型），
// 于是 `db.Where("ip = ?", ...).First(&entity.AdminLoginAttempt{})` 会被拼成
// `... FROM users WHERE users.ip = ?` —— 直接报 `no such column: users.ip`。
// 这个坑项目里已有记录（见 withDatabasePriority 的注释：NewDB 就是为了清掉
// 生成式 DAO 残留的 model/schema）。
//
// 用 manager.GetManager().GetDB() 拿到的是裸 *gorm.DB（无 Statement 残留）；
// manager 为 nil（单测/早期启动）时回落到 dao 的底层连接并强制 NewDB。
func adminLoginDB() *gorm.DB {
	if adminLoginDBOverride != nil {
		return adminLoginDBOverride.Session(&gorm.Session{NewDB: true})
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

// AdminLogin 是**管理员专用**登录入口（45-*.md §0.1.2 Q63 定稿）。
//
// 为什么需要它：按 Q63，普通登录（/user/login）现在**会被 IP 黑名单拦**——
// 被拉黑的 IP 打开网页只能看到拉黑页面。若没有这个入口，管理员一旦把自己的
// CIDR 拉黑就再也进不来（自锁），只能改数据库。
//
// 三条不可协商的约束：
//
//  1. **仅账号密码 + 强制人机验证**。不提供短信/微信登录：被拉黑 IP 可能连
//     发短信接口都被拦（那条路径不豁免），会形成「要登录先发短信、发短信被拦」的死锁。
//  2. **只放行管理员**。非管理员账号在这里登录一律拒绝 —— 否则它就成了
//     「绕过黑名单的通用登录入口」，等于把黑名单对所有人作废。
//  3. **失败锁定**（45-*.md §0.1.4）。这个入口豁免黑名单，是整站唯一
//     「被拉黑 IP 仍能打」的写入面，必须自带防爆破。
//
// 与 UserLogin 的关系：**不改 UserLogin 的既有行为**（红线）。两者共用
// `CaptchaSceneLoginPassword` 场景与 `IssueUserLoginJWTWithDB`，但本函数是独立实现：
// 若把管理员逻辑塞进 UserLogin，任何一处改动都会同时影响普通登录，
// 而普通登录是线上主路径，不能为管理员入口承担回归风险。
func AdminLogin(c *gin.Context) {
	var payload struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		CaptchaToken string `json:"captchaToken"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "参数错误"})
		return
	}

	// 管理员入口**只受 IP 维度约束**（45-*.md §0.1.4 四-2 / 五-6）：
	// 这个入口本身豁免黑名单、是唯一暴露面，所以按 IP 计数且不可关闭。
	// 不受账号维度约束 —— 否则攻击者故意输错某管理员用户名就能顺带锁死这个入口。
	gate := beginLoginRateLimitAdminEntry(c)
	if gate == nil {
		return
	}

	if err := util.ValidateBcryptPasswordBytes(payload.Password); err != nil {
		// 超长密码也算失败：不能让它成为不计数的探测路径。
		gate.failIPOnly("用户名或密码错误")
		return
	}

	captchaToken := strings.TrimSpace(payload.CaptchaToken)
	if err := consumeCaptchaTokenWithDB(captchaToken, service.CaptchaSceneLoginPassword, strings.TrimSpace(c.ClientIP())); err != nil {
		// 验证码错误**计入失败**（45-*.md §0.1.4 三）。
		gate.failIPOnly(err.Error())
		return
	}

	us := dao.User
	user, err := us.Where(us.Username.Eq(strings.TrimSpace(payload.Username))).First()
	if err != nil {
		// 账号不存在**计入失败**：否则攻击者可以无限枚举管理员用户名。
		gate.failIPOnly("用户名或密码错误")
		return
	}
	passwordMatches, needsUpgrade := util.VerifyPassword(user.Password, payload.Password)
	if !passwordMatches {
		gate.failIPOnly("用户名或密码错误")
		return
	}
	if !isUserEnabled(user) {
		gate.failIPOnly("用户已被禁用")
		return
	}
	// 只放行管理员。密码正确但角色不是管理员 → 同样计入失败：
	// 否则持有普通账号的人可以在这里无限探测，且不付出锁定代价。
	if !isAdminUser(user) {
		recordAccountAudit(c, user, "auth", logActionLogin, "非管理员尝试使用管理员入口登录",
			map[string]any{"status": "denied", "message": "非管理员", "scene": "admin_password"})
		gate.failIPOnly("该入口仅限管理员使用")
		return
	}

	passwordUpgrade := ""
	if needsUpgrade {
		passwordUpgrade, err = util.HashPassword(payload.Password)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"message": "登录信息更新失败"})
			return
		}
	}
	token, err := service.IssueUserLoginJWTWithDB(gate.db.WithContext(c.Request.Context()), user, strings.TrimSpace(c.ClientIP()), passwordUpgrade)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "登录信息更新失败"})
		return
	}

	// 成功：清零连续失败计数**并把 window_start 也重置**（「连续」语义的核心）。
	gate.succeed("")

	bindSuccessfulVerification(*user, captchaToken, service.CaptchaSceneLoginPassword, service.SMSVerificationEvidence{})
	recordAccountAudit(c, user, "auth", logActionLogin, "管理员通过专用入口登录成功",
		map[string]any{"status": "success", "message": "管理员登录成功", "scene": "admin_password"})
	setAuthCookie(c, token)
	c.JSON(http.StatusOK, gin.H{"message": "admin login", "jwt": token})
}

// respondLoginLocked 输出锁定提示（含剩余分钟数）。
//
// 用 429 而不是 403：429 的语义就是「你请求太频繁/被限流」，
// 且标准上允许带 Retry-After 头，前端可以直接据此倒计时。
// 文案必须带剩余时间 —— 只说「次数过多」会让用户反复重试，把锁定不断续期。
func respondLoginLocked(c *gin.Context, lockout service.LoginLockout) {
	minutes := lockout.RemainingMinutesCeil()
	c.Header("Retry-After", strconv.Itoa(minutes*60))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"code":    http.StatusTooManyRequests,
		"message": "登录失败次数过多，请 " + strconv.Itoa(minutes) + " 分钟后再试",
		// 机器可读的剩余时间，供前端倒计时。
		"retryAfterSeconds": int(lockout.Remaining.Seconds()) + 1,
		"remainingMinutes":  minutes,
	})
}

// respondLoginFailure 记一次失败并按阈值决定是否回锁定提示。
//
// 统一出口的意义：**每一种失败都必须计数**。若在某个分支上直接 c.JSON 返回，
// 就等于给攻击者留了一条「不计数量的爆破路径」。把计数放在唯一的出口函数里，
// 是让「新增失败分支时忘记计数」这个错误在结构上不可能发生。
func respondLoginFailure(c *gin.Context, db *gorm.DB, policy service.LoginRateLimitPolicy, subjectKey string, message string, now time.Time) {
	lockout, err := service.RecordLoginFailureWithDB(db, policy, subjectKey, now)
	if err != nil {
		logger.SysWarn("记录登录失败次数失败：" + err.Error())
	}
	if lockout.Locked {
		// 这一次失败刚好触发了锁定 → 直接告诉用户要等多久，
		// 而不是先回「密码错误」再让下一次请求才发现被锁。
		respondLoginLocked(c, lockout)
		return
	}
	// 未触发锁定时给出**剩余次数**，让用户知道还有几次机会。
	remaining := lockout.MaxAttempts - lockout.FailCount
	if remaining < 0 {
		remaining = 0
	}
	c.JSON(http.StatusBadRequest, gin.H{
		"message":           message,
		"remainingAttempts": remaining,
	})
}

// LoginLockoutList 列出当前锁定中的条目（管理端「解除锁定」页用）。
//
// scope 可选：account（普通模式）/ ip（管理员入口）/ 空（两者都列）。
func LoginLockoutList(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	limit := 100
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			limit = value
		}
	}
	scope := strings.TrimSpace(c.Query("scope"))
	switch scope {
	case "", entity.LoginAttemptScopeAccount, entity.LoginAttemptScopeIP:
	default:
		c.JSON(http.StatusBadRequest, gin.H{"message": "scope 只能是 account 或 ip"})
		return
	}
	db := withDatabasePriority(adminLoginDB(), databaseRequestContext(c), dbqueue.PriorityLow)
	items, err := service.ListLoginAttemptLocksWithDB(db, scope, time.Now(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "读取登录锁定列表失败"})
		return
	}
	accountPolicy := service.AccountLoginPolicy(db)
	ipPolicy := service.IPLoginPolicy(db)
	c.JSON(http.StatusOK, gin.H{
		"items": items,
		"total": len(items),
		// 两套参数分别下发，便于前端渲染说明。
		"account": gin.H{
			"enabled":        accountPolicy.Enabled,
			"maxAttempts":    accountPolicy.MaxAttempts,
			"windowMinutes":  accountPolicy.WindowMinutes,
			"lockoutMinutes": accountPolicy.LockoutMinutes,
		},
		"ip": gin.H{
			// Enabled 恒为 true：管理员入口**不允许关闭**（没有 enabled 键）。
			"enabled":        true,
			"maxAttempts":    ipPolicy.MaxAttempts,
			"windowMinutes":  ipPolicy.WindowMinutes,
			"lockoutMinutes": ipPolicy.LockoutMinutes,
		},
	})
}

// ClearLoginLockout 手动解除某个账号或 IP 的登录锁定（管理端按钮）。
//
// 这是失败锁定的**必要配套**（45-*.md §0.1.4 六）：锁定本身是 DoS 面 ——
// 攻击者知道路径/用户名就能故意失败把真人挡在门外。手动解除给运营一个兜底出口。
//
// 路径参数用 scope 区分两个维度（45-*.md §0.1.4 五-8：两个操作）。
func ClearLoginLockout(c *gin.Context) {
	if !requireAdmin(c) {
		return
	}
	scope := strings.TrimSpace(c.Param("scope"))
	rawKey := strings.TrimSpace(c.Param("key"))
	var subjectKey string
	switch scope {
	case entity.LoginAttemptScopeAccount:
		subjectKey = service.NormalizeLoginAccountKey(rawKey)
	case entity.LoginAttemptScopeIP:
		subjectKey = service.NormalizeLoginIPKey(rawKey)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"message": "scope 只能是 account 或 ip"})
		return
	}
	if subjectKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"message": "解除对象不能为空"})
		return
	}
	db := adminLoginDB()
	if err := service.ClearLoginAttemptsFullyWithDB(db, scope, subjectKey); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "解除登录锁定失败"})
		return
	}
	operator, _ := currentOperator(c)
	recordAccountAudit(c, &operator, "auth", logActionUpdate, "管理员手动解除登录锁定",
		map[string]any{"status": "success", "message": "解除登录锁定", "scope": scope, "subject": subjectKey})
	c.JSON(http.StatusOK, gin.H{"message": "已解除登录锁定", "scope": scope})
}

// 编译期断言：AdminLogin 用到的 entity.User 类型别名保持可用。
// （放在这里是为了让 import 不因后续重构被误删；本文件确实用到 entity.User。）
var _ *entity.User
