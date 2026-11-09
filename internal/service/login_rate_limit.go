package service

import (
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 两套登录限流（45-*.md §0.1.4 Q63 定稿）。
//
// 两套共用同一张 login_attempts 表与同一段计数逻辑，只用 scope 区分：
//
//	account —— 普通模式，按 username 计数，**可被管理员关闭**
//	ip      —— 被拉黑 IP 的管理员入口，按 IP 计数，**不可关闭**
//
// 三条最容易实现错的点，都在这里写死：
//
//  1. **「不允许关闭」是结构性保证**：ip 维度没有 enabled 开关，
//     也没有 `blacklist_admin_login_enabled` 这个键（本文件里搜不到它）。
//     不存在「代码判断 enabled 为 true 才生效」——那种写法意味着有人能把它设成 false。
//  2. **「连续失败」不是「窗口内累计」**：登录成功时 fail_count 归零**且
//     window_start 也重置**。只清 fail_count 不清 window_start 的话，
//     成功之后的下一次失败会被算进旧窗口而立刻触发锁定，「连续」就不成立了。
//  3. **参数越界拒绝保存**，不静默 clamp：管理员填 8 次却存成 10 次，
//     他会以为配的是 8。返回明确错误更好。

// 普通模式（account 维度）的配置键与默认值（45-*.md §0.1.4 二）。
const (
	// LoginRateLimitEnabledSettingKey 控制普通模式限流是否启用，默认 true。
	//
	// ⚠️ 只有**普通模式**有这个开关。管理员入口（ip 维度）没有对应键。
	LoginRateLimitEnabledSettingKey = "login_rate_limit_enabled"
	// LoginMaxAttemptsSettingKey 是普通模式连续失败阈值，默认 10，范围 ≥1 无上限。
	LoginMaxAttemptsSettingKey = "login_max_attempts"
	// LoginAttemptWindowMinutesSettingKey 是普通模式计数窗口（分钟），默认 15，范围 ≥1 无上限。
	LoginAttemptWindowMinutesSettingKey = "login_attempt_window_minutes"
	// LoginLockoutMinutesSettingKey 是普通模式锁定时长（分钟），默认 15，范围 ≥1。
	LoginLockoutMinutesSettingKey = "login_lockout_minutes"
	// LoginIPMaxAttemptsSettingKey 是普通模式**IP 维度**的连续失败阈值，默认 20。
	//
	// 为什么需要它（45-*.md Q67）：账号维度只能拦住「针对单个账号」的爆破。
	// 攻击者做 password spraying 时对 N 个账号各试少数几次 —— 每个账号都不到
	// login_max_attempts（10），于是全部放行。要在 IP 维度上按「总失败数」兜住。
	//
	// 为什么默认 20 而不是 30：30 恰好等于「3 次 × 10 账号」的探测规模，
	// 而撞上阈值不等于触发 —— fail_count 到 30 时攻击者已经完成 10 个账号的探测。
	// 阈值必须**低于**攻击者的典型探测规模。20 能拦住 N≥20 的批量，且正常
	// NAT 出口在 15 分钟内产生 20 次**失败**登录极罕见（要么在撞库，要么整个
	// 办公室同时忘了密码）。
	//
	// 跟随普通模式总开关（login_rate_limit_enabled）：关闭普通模式时不生效。
	// 与 blacklist_admin_login_max_attempts（黑名单 IP，严格 5）是**两个不同阈值**，
	// 同属 ip scope，按「是否被拉黑」选择。
	LoginIPMaxAttemptsSettingKey = "login_ip_max_attempts"
	// LoginRateLimitExemptIPsSettingKey 是 IP 白名单（CIDR 或单个 IP，逗号/换行分隔），默认空。
	//
	// 大公司 NAT 出口确实可能有多人同时输错密码。白名单里的 IP
	// **完全不参与 IP 维度限流**（账号维度仍生效）—— 运营遇到误伤时把出口 IP
	// 加进来即可，不必放宽全局阈值。
	LoginRateLimitExemptIPsSettingKey = "login_rate_limit_exempt_ips"
)

// 被拉黑 IP 的管理员入口（ip 维度）的配置键与默认值。
//
// ⚠️ 这三个键**没有对应的 enabled 键**，这是「不允许关闭」的结构性实现。
// 不要新增 `blacklist_admin_login_enabled`。
const (
	// BlacklistAdminLoginWindowMinutesSettingKey 默认 15，范围 **≥15**（最低 15）。
	BlacklistAdminLoginWindowMinutesSettingKey = "blacklist_admin_login_window_minutes"
	// BlacklistAdminLoginMaxAttemptsSettingKey 默认 5，范围 **1–10**（最高 10）。
	BlacklistAdminLoginMaxAttemptsSettingKey = "blacklist_admin_login_max_attempts"
	// BlacklistAdminLoginLockoutMinutesSettingKey 默认 30，范围 ≥1。
	BlacklistAdminLoginLockoutMinutesSettingKey = "blacklist_admin_login_lockout_minutes"
)

// 默认值（45-*.md §0.1.4 二）。
const (
	DefaultLoginRateLimitEnabled     = true
	DefaultLoginMaxAttempts          = 10
	DefaultLoginAttemptWindowMinutes = 15
	DefaultLoginLockoutMinutes       = 15
	// DefaultLoginIPMaxAttempts 是普通模式 IP 维度阈值，默认 20（45-*.md Q67）。
	//
	// 取 20 而非 30 的推导见 LoginIPMaxAttemptsSettingKey 的注释。
	DefaultLoginIPMaxAttempts            = 20
	DefaultBlacklistAdminLoginWindow     = 15
	DefaultBlacklistAdminLoginMaxAttempt = 5
	DefaultBlacklistAdminLoginLockout    = 30
)

// 参数边界（服务端强制，越界拒绝保存）。
const (
	// MinBlacklistAdminLoginWindowMinutes 是管理员入口窗口的**下限**：不得低于 15 分钟。
	//
	// 为什么是下限而不是无限制：窗口太短会让「连续失败」计数在两次攻击之间
	// 自动过期，等于给攻击者一个免费的冷却期。15 分钟是用户定稿的底线。
	MinBlacklistAdminLoginWindowMinutes = 15
	// MaxBlacklistAdminLoginMaxAttempts 是管理员入口阈值的**上限**：不得高于 10 次。
	//
	// 为什么是上限：阈值太高（如 100）等于这个入口没有防爆破能力，
	// 而它豁免黑名单、是唯一暴露面。10 是用户定稿的上限。
	MaxBlacklistAdminLoginMaxAttempts = 10
)

// 输入校验错误（都是 400，不是 500）。
var (
	// ErrInvalidLoginRateLimitConfig 普通模式配置非法（<1 等）。
	ErrInvalidLoginRateLimitConfig = errors.New("普通登录限流配置必须是正整数（次数 ≥ 1、窗口 ≥ 1 分钟、锁定 ≥ 1 分钟）")
	// ErrBlacklistAdminLoginWindowTooSmall 管理员入口窗口低于 15 分钟下限。
	//
	// 文案里带上具体边界，管理员才知道该填多少 —— 只说「不合法」会被反复试。
	ErrBlacklistAdminLoginWindowTooSmall = fmt.Errorf(
		"被拉黑 IP 的管理员登录窗口不得低于 %d 分钟", MinBlacklistAdminLoginWindowMinutes)
	// ErrBlacklistAdminLoginAttemptsTooLarge 管理员入口阈值高于 10 次上限。
	ErrBlacklistAdminLoginAttemptsTooLarge = fmt.Errorf(
		"被拉黑 IP 的管理员登录失败次数不得高于 %d 次", MaxBlacklistAdminLoginMaxAttempts)
	// ErrInvalidBlacklistAdminLoginLockout 管理员入口锁定时长非法（<1）。
	ErrInvalidBlacklistAdminLoginLockout = errors.New("被拉黑 IP 的管理员登录锁定时长必须 ≥ 1 分钟")
	// ErrInvalidLoginRateLimitExemptIPs IP 白名单里含无法解析的项。
	ErrInvalidLoginRateLimitExemptIPs = errors.New("IP 白名单只能是 IP 或 CIDR 网段")
	// ErrInvalidLoginIPMaxAttempts 普通模式 IP 维度阈值非法（<1）。
	ErrInvalidLoginIPMaxAttempts = errors.New("普通登录 IP 维度失败阈值必须 ≥ 1")
)

// LoginRateLimitPolicy 是一套限流的完整参数。
//
// 两套限流的差异只体现在这个结构体的取值上，判定逻辑完全共用
// （避免「同一语义两份实现」，AGENTS.md 第 13 条）。
type LoginRateLimitPolicy struct {
	// Scope 取 account / ip。
	Scope string
	// Enabled 是否启用。**ip 维度恒为 true**，没有配置能改变它。
	Enabled bool
	// MaxAttempts 连续失败阈值。
	MaxAttempts int
	// WindowMinutes 计数窗口（分钟）。
	WindowMinutes int
	// LockoutMinutes 锁定时长（分钟）。
	LockoutMinutes int
}

// LoginLockout 是一次锁定判定的结果。
type LoginLockout struct {
	Locked bool
	Until  time.Time
	// Remaining 是剩余时长。
	Remaining time.Duration
	// FailCount 是当前连续失败次数。
	FailCount int
	// MaxAttempts 是生效阈值。
	MaxAttempts int
	// Scope 是命中的维度（account / ip），供调用方区分提示与审计。
	Scope string
}

// RemainingMinutesCeil 返回剩余分钟数，**向上取整**，最少 1。
//
// 向上取整是刻意的：剩余 30 秒时提示「请 1 分钟后再试」而不是「请 0 分钟」，
// 后者会让用户以为已经可以重试。
func (l LoginLockout) RemainingMinutesCeil() int {
	if !l.Locked {
		return 0
	}
	minutes := int(math.Ceil(l.Remaining.Minutes()))
	if minutes < 1 {
		return 1
	}
	return minutes
}

// ============================================================================
// 配置读取与校验
// ============================================================================

// GetLoginRateLimitEnabledWithDB 返回普通模式限流开关，默认 true。
//
// 缺表、缺记录、值非法一律按 true 处理 —— 与黑名单的「默认 false」方向相反：
// 限流是防护，读不到配置时**保留防护**比放开更安全（fail-closed）。
// 这与赠金判定同向、与黑名单 fail-open 反向，理由见 46-*.md §3.4 的对照表。
func GetLoginRateLimitEnabledWithDB(db *gorm.DB) bool {
	if db == nil || !db.Migrator().HasTable(entity.TableNameSystemSetting) {
		return DefaultLoginRateLimitEnabled
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, LoginRateLimitEnabledSettingKey, "true"))
	switch strings.ToLower(raw) {
	case "false", "0", "off", "no":
		return false
	case "true", "1", "on", "yes":
		return true
	default:
		// 解析不了 → 保留防护（true），不因为配置写坏而静默关掉限流。
		return DefaultLoginRateLimitEnabled
	}
}

// GetLoginMaxAttemptsWithDB 返回普通模式连续失败阈值，默认 10。
//
// 范围 ≥1、**无上限**。非法值回落默认（读不到时不能变成 0 次阈值 = 一失败就锁）。
func GetLoginMaxAttemptsWithDB(db *gorm.DB) int {
	return getPositiveIntSetting(db, LoginMaxAttemptsSettingKey, DefaultLoginMaxAttempts)
}

// GetLoginAttemptWindowMinutesWithDB 返回普通模式计数窗口（分钟），默认 15，无上限。
func GetLoginAttemptWindowMinutesWithDB(db *gorm.DB) int {
	return getPositiveIntSetting(db, LoginAttemptWindowMinutesSettingKey, DefaultLoginAttemptWindowMinutes)
}

// GetLoginLockoutMinutesWithDB 返回普通模式锁定时长（分钟），默认 15。
func GetLoginLockoutMinutesWithDB(db *gorm.DB) int {
	return getPositiveIntSetting(db, LoginLockoutMinutesSettingKey, DefaultLoginLockoutMinutes)
}

// GetBlacklistAdminLoginWindowMinutesWithDB 返回管理员入口窗口（分钟），默认 15。
//
// 读取时同样强制下限：库里若有历史遗留的小于 15 的值（比如手工改库），
// 读出来按 15 用 —— 判定必须与保存时的校验一致，否则「拒绝保存但判定按小窗口」
// 会让运营看到的行为与配置不符。
func GetBlacklistAdminLoginWindowMinutesWithDB(db *gorm.DB) int {
	value := getPositiveIntSetting(db, BlacklistAdminLoginWindowMinutesSettingKey, DefaultBlacklistAdminLoginWindow)
	if value < MinBlacklistAdminLoginWindowMinutes {
		return MinBlacklistAdminLoginWindowMinutes
	}
	return value
}

// GetBlacklistAdminLoginMaxAttemptsWithDB 返回管理员入口阈值，默认 5。
//
// 读取时强制上限 10：同上的理由（判定与校验一致）。
func GetBlacklistAdminLoginMaxAttemptsWithDB(db *gorm.DB) int {
	value := getPositiveIntSetting(db, BlacklistAdminLoginMaxAttemptsSettingKey, DefaultBlacklistAdminLoginMaxAttempt)
	if value > MaxBlacklistAdminLoginMaxAttempts {
		return MaxBlacklistAdminLoginMaxAttempts
	}
	return value
}

// GetBlacklistAdminLoginLockoutMinutesWithDB 返回管理员入口锁定时长，默认 30。
func GetBlacklistAdminLoginLockoutMinutesWithDB(db *gorm.DB) int {
	return getPositiveIntSetting(db, BlacklistAdminLoginLockoutMinutesSettingKey, DefaultBlacklistAdminLoginLockout)
}

// AccountLoginPolicy 返回普通模式（account 维度）的生效参数。
//
// Enabled 来自 login_rate_limit_enabled —— **只有这个维度有开关**。
func AccountLoginPolicy(db *gorm.DB) LoginRateLimitPolicy {
	return LoginRateLimitPolicy{
		Scope:          entity.LoginAttemptScopeAccount,
		Enabled:        GetLoginRateLimitEnabledWithDB(db),
		MaxAttempts:    GetLoginMaxAttemptsWithDB(db),
		WindowMinutes:  GetLoginAttemptWindowMinutesWithDB(db),
		LockoutMinutes: GetLoginLockoutMinutesWithDB(db),
	}
}

// IPLoginPolicy 返回**被拉黑 IP**（管理员入口 / 黑名单 IP 走普通入口）的生效参数。
//
// ⚠️ **Enabled 恒为 true，不接受任何配置**。这是「不允许关闭」的落点：
// 本函数没有读任何 enabled 键，也不存在这样的键可读。
func IPLoginPolicy(db *gorm.DB) LoginRateLimitPolicy {
	return LoginRateLimitPolicy{
		Scope:          entity.LoginAttemptScopeIP,
		Enabled:        true, // 硬编码：结构性不可关闭
		MaxAttempts:    GetBlacklistAdminLoginMaxAttemptsWithDB(db),
		WindowMinutes:  GetBlacklistAdminLoginWindowMinutesWithDB(db),
		LockoutMinutes: GetBlacklistAdminLoginLockoutMinutesWithDB(db),
	}
}

// NormalIPLoginPolicy 返回**未拉黑 IP** 的普通模式 IP 维度参数（45-*.md Q67）。
//
// 与 IPLoginPolicy 共用同一个 scope（entity.LoginAttemptScopeIP）与同一张表，
// 区别只在阈值：被拉黑 IP 用严格阈值（默认 5），普通 IP 用宽松阈值（默认 20）。
//
// Enabled 跟随普通模式总开关（login_rate_limit_enabled）—— 关掉普通模式限流时
// 这一层也不生效。但被拉黑 IP 的严格层（IPLoginPolicy）**不受该开关影响**，
// 这正是「管理员入口不允许关闭」与「普通模式可关闭」的交汇处。
func NormalIPLoginPolicy(db *gorm.DB) LoginRateLimitPolicy {
	return LoginRateLimitPolicy{
		Scope:          entity.LoginAttemptScopeIP,
		Enabled:        GetLoginRateLimitEnabledWithDB(db),
		MaxAttempts:    GetLoginIPMaxAttemptsWithDB(db),
		WindowMinutes:  GetLoginAttemptWindowMinutesWithDB(db),
		LockoutMinutes: GetLoginLockoutMinutesWithDB(db),
	}
}

// GetLoginIPMaxAttemptsWithDB 返回普通模式 IP 维度阈值，默认 20。
//
// 范围 ≥1、无上限。非法值回落默认（读不到时不能变成 0 次阈值 = 一失败就锁）。
func GetLoginIPMaxAttemptsWithDB(db *gorm.DB) int {
	return getPositiveIntSetting(db, LoginIPMaxAttemptsSettingKey, DefaultLoginIPMaxAttempts)
}

// GetLoginRateLimitExemptIPsWithDB 返回 IP 白名单的原始字符串（默认空）。
func GetLoginRateLimitExemptIPsWithDB(db *gorm.DB) string {
	if db == nil || !db.Migrator().HasTable(entity.TableNameSystemSetting) {
		return ""
	}
	return strings.TrimSpace(getSystemSettingStringWithDefault(db, LoginRateLimitExemptIPsSettingKey, ""))
}

// ParseLoginRateLimitExemptIPs 把白名单文本解析成网段列表。
//
// 接受逗号 / 分号 / 换行 / 空格分隔；每一项可以是单个 IP（按 /32 或 /128 处理）
// 或 CIDR 网段。无法解析的项**跳过并告警**，不让一条写错的项使整份白名单失效。
//
// 为什么支持 CIDR 而不只是单个 IP：运营要放行的往往是整个办公出口网段。
func ParseLoginRateLimitExemptIPs(raw string) []*net.IPNet {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	fields := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	networks := make([]*net.IPNet, 0, len(fields))
	for _, field := range fields {
		item := strings.TrimSpace(field)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			_, network, err := net.ParseCIDR(item)
			if err != nil || network == nil {
				logger.SysWarn("登录限流 IP 白名单项不是合法 CIDR，已跳过：" + item)
				continue
			}
			networks = append(networks, network)
			continue
		}
		ip := net.ParseIP(item)
		if ip == nil {
			logger.SysWarn("登录限流 IP 白名单项不是合法 IP，已跳过：" + item)
			continue
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		networks = append(networks, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return networks
}

// IsLoginRateLimitExemptIP 判断某个 IP 是否在白名单内（完全不参与 IP 维度限流）。
//
// 归一化用 net.ParseIP（与 NormalizeLoginIPKey 一致），因此
// `::ffff:203.0.113.7` 与 `203.0.113.7` 会被同等对待。
func IsLoginRateLimitExemptIP(db *gorm.DB, ipKey string) bool {
	trimmed := strings.TrimSpace(ipKey)
	if trimmed == "" {
		return false
	}
	ip := net.ParseIP(trimmed)
	if ip == nil {
		return false
	}
	for _, network := range ParseLoginRateLimitExemptIPs(GetLoginRateLimitExemptIPsWithDB(db)) {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateLoginRateLimitExemptIPs 校验白名单文本里的每一项都可解析。
//
// 与「解析时跳过坏项」不同：**保存时**要拒绝坏项（返回 400），
// 否则运营填了一个写错的网段会以为已经放行，实际没生效。
// 读取时才有「跳过坏项」的宽容（老库可能已被手工写入坏值）。
func ValidateLoginRateLimitExemptIPs(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	fields := strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	for _, field := range fields {
		item := strings.TrimSpace(field)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			if _, _, err := net.ParseCIDR(item); err != nil {
				return fmt.Errorf("%w：%q 不是合法 CIDR", ErrInvalidLoginRateLimitExemptIPs, item)
			}
			continue
		}
		if net.ParseIP(item) == nil {
			return fmt.Errorf("%w：%q 不是合法 IP", ErrInvalidLoginRateLimitExemptIPs, item)
		}
	}
	return nil
}

// getPositiveIntSetting 读取正整数配置；缺表/缺记录/非法/<=0 一律回落默认。
//
// 抽成一处是为了让「读不到就回落默认，绝不回落成 0」这条纪律只有一份实现。
func getPositiveIntSetting(db *gorm.DB, key string, defaultValue int) int {
	if db == nil || !db.Migrator().HasTable(entity.TableNameSystemSetting) {
		return defaultValue
	}
	raw := strings.TrimSpace(getSystemSettingStringWithDefault(db, key, fmt.Sprintf("%d", defaultValue)))
	value, err := parsePositiveInt(raw)
	if err != nil {
		return defaultValue
	}
	return value
}

// parsePositiveInt 只接受纯十进制正整数（不接受 +、-、小数点、空格）。
func parsePositiveInt(raw string) (int, error) {
	if raw == "" {
		return 0, errors.New("空字符串")
	}
	value := 0
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("不是非负整数：%q", raw)
		}
		if value > (math.MaxInt-9)/10 {
			return 0, fmt.Errorf("整数过大：%q", raw)
		}
		value = value*10 + int(ch-'0')
	}
	if value <= 0 {
		return 0, fmt.Errorf("必须 ≥ 1：%q", raw)
	}
	return value, nil
}

// ValidateLoginRateLimitInput 校验一次系统设置写入里的登录限流参数。
//
// 语义是「省略 = 保持现值」：nil 字段不校验（沿用库里的值）。
// 显式提交的值必须落在边界内，**越界返回错误而不是静默 clamp** ——
// 管理员填了 8 次却存成 10 次，他会以为配的是 8。
//
// 返回的错误按维度区分，便于控制器映射成 400 + 精确文案。
func ValidateLoginRateLimitInput(input UpdateSystemSettingInput) error {
	if input.LoginMaxAttempts != nil && *input.LoginMaxAttempts < 1 {
		return ErrInvalidLoginRateLimitConfig
	}
	if input.LoginAttemptWindowMinutes != nil && *input.LoginAttemptWindowMinutes < 1 {
		return ErrInvalidLoginRateLimitConfig
	}
	if input.LoginLockoutMinutes != nil && *input.LoginLockoutMinutes < 1 {
		return ErrInvalidLoginRateLimitConfig
	}
	if input.BlacklistAdminLoginWindowMinutes != nil &&
		*input.BlacklistAdminLoginWindowMinutes < MinBlacklistAdminLoginWindowMinutes {
		return ErrBlacklistAdminLoginWindowTooSmall
	}
	if input.BlacklistAdminLoginMaxAttempts != nil &&
		*input.BlacklistAdminLoginMaxAttempts > MaxBlacklistAdminLoginMaxAttempts {
		return ErrBlacklistAdminLoginAttemptsTooLarge
	}
	if input.BlacklistAdminLoginMaxAttempts != nil && *input.BlacklistAdminLoginMaxAttempts < 1 {
		return ErrBlacklistAdminLoginAttemptsTooLarge
	}
	if input.BlacklistAdminLoginLockoutMinutes != nil && *input.BlacklistAdminLoginLockoutMinutes < 1 {
		return ErrInvalidBlacklistAdminLoginLockout
	}
	// 普通模式 IP 维度阈值：≥1，无上限。
	if input.LoginIPMaxAttempts != nil && *input.LoginIPMaxAttempts < 1 {
		return ErrInvalidLoginIPMaxAttempts
	}
	// IP 白名单：保存时**拒绝**无法解析的项（读取时才是跳过坏项）。
	// 差别很重要：运营填了写错的网段会以为已放行，实际没生效。
	if input.LoginRateLimitExemptIPs != nil {
		if err := ValidateLoginRateLimitExemptIPs(*input.LoginRateLimitExemptIPs); err != nil {
			return err
		}
	}
	return nil
}

// resolveLoginRateLimitExemptIPs 处理「省略 = 保持现值」的白名单写入语义。
//
// 空字符串是**合法值**（表示清空白名单），因此不能用「空值 = 未提交」判断 ——
// 必须靠指针 nil 区分。传空串会真的清空，这正是运营想要的语义。
func resolveLoginRateLimitExemptIPs(db *gorm.DB, requested *string) string {
	if requested == nil {
		return GetLoginRateLimitExemptIPsWithDB(db)
	}
	return strings.TrimSpace(*requested)
}

// resolveLoginRateLimitInt 处理「省略 = 保持现值」的整数写入语义。
//
// 显式值已在 ValidateLoginRateLimitInput 里校验过边界，这里只负责取值。
func resolveLoginRateLimitInt(db *gorm.DB, requested *int, current int) int {
	if requested == nil {
		return current
	}
	return *requested
}

// ============================================================================
// 计数与判定
// ============================================================================

// NormalizeLoginAccountKey 归一化账号维度的计数 key。
//
// 只做 TrimSpace，与登录查询口径一致（45-*.md §0.1.4 五-2）。
// **不转小写**：本项目用户名大小写敏感（登录查询用 Eq 精确匹配），
// 转小写会让「Admin」与「admin」共用计数桶，反而制造串号。
//
// ⚠️ 空 username 返回空串，调用方据此跳过计数 —— 否则所有空用户名请求
// 会共用同一个桶，等于给攻击者一个「不填用户名」的免费通道。
func NormalizeLoginAccountKey(username string) string {
	trimmed := strings.TrimSpace(username)
	if trimmed == "" {
		return ""
	}
	// 按字符截断到 128（与 varchar(128) 对齐，AGENTS.md 第 1 条）。
	runes := []rune(trimmed)
	if len(runes) > 128 {
		return string(runes[:128])
	}
	return trimmed
}

// NormalizeLoginIPKey 归一化 IP 维度的计数 key。
//
// 用 net.ParseIP 规范化（v4-mapped 收敛），否则同一个客户端换一种写法
// 就能绕过锁定。解析不了时原样返回（不返回空串 —— 空串会让所有无法解析的
// 请求共用同一个桶）。
func NormalizeLoginIPKey(clientIP string) string {
	trimmed := strings.TrimSpace(clientIP)
	if trimmed == "" {
		return ""
	}
	ip := net.ParseIP(trimmed)
	if ip == nil {
		// 解析不了（测试里的 "1.2.3.4:5678" 之类）：原样截断返回。
		runes := []rune(trimmed)
		if len(runes) > 128 {
			return string(runes[:128])
		}
		return trimmed
	}
	return ip.String()
}

// CheckLoginLockoutWithDB 查询某个 (scope, subject) 当前的锁定状态（只读）。
//
// policy.Enabled 为 false 时直接返回未锁定（普通模式被管理员关闭时零行为）。
func CheckLoginLockoutWithDB(db *gorm.DB, policy LoginRateLimitPolicy, subjectKey string, now time.Time) (LoginLockout, error) {
	result := LoginLockout{MaxAttempts: policy.MaxAttempts, Scope: policy.Scope}
	if !policy.Enabled || db == nil || subjectKey == "" {
		return result, nil
	}
	if !db.Migrator().HasTable(entity.TableNameLoginAttempt) {
		// 表还没建（迁移未跑/回滚）：按未锁定处理。
		// 限流是防护增强，缺表不该让人登不进来。
		return result, nil
	}
	var row entity.LoginAttempt
	err := db.Where("scope = ? AND subject_key = ?", policy.Scope, subjectKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return result, nil
	}
	if err != nil {
		// 读失败按未锁定处理（fail-open）：DB 抖动时把所有人挡在门外
		// 比暂时少一层防护更糟。
		logger.SysWarn("读取登录限流状态失败，按未锁定处理：" + err.Error())
		return result, nil
	}
	result.FailCount = row.FailCount
	if row.LockedUntil != nil && now.Before(*row.LockedUntil) {
		result.Locked = true
		result.Until = *row.LockedUntil
		result.Remaining = row.LockedUntil.Sub(now)
	}
	return result, nil
}

// RecordLoginFailureWithDB 记一次登录失败（**连续失败语义**）。
//
// 精确实现（45-*.md §0.1.4 三）：
//
//	if now - window_start > window_minutes:
//	    fail_count = 1;  window_start = now     ← 窗口过期，重新计
//	else:
//	    fail_count++
//	if fail_count >= max_attempts:
//	    locked_until = now + lockout_minutes
//
// policy.Enabled 为 false 时**零行为**（不读不写），这是普通模式可关闭的落点。
func RecordLoginFailureWithDB(db *gorm.DB, policy LoginRateLimitPolicy, subjectKey string, now time.Time) (LoginLockout, error) {
	result := LoginLockout{MaxAttempts: policy.MaxAttempts, Scope: policy.Scope}
	if !policy.Enabled || db == nil || subjectKey == "" {
		return result, nil
	}
	if !db.Migrator().HasTable(entity.TableNameLoginAttempt) {
		return result, nil
	}

	var row entity.LoginAttempt
	err := db.Where("scope = ? AND subject_key = ?", policy.Scope, subjectKey).First(&row).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		row = entity.LoginAttempt{Scope: policy.Scope, SubjectKey: subjectKey}
	case err != nil:
		logger.SysWarn("读取登录失败计数失败，本次不计入：" + err.Error())
		return result, nil
	}

	windowDuration := time.Duration(policy.WindowMinutes) * time.Minute
	// 窗口过期 → 重新开始计数（不是累加）。
	if row.WindowStart == nil || now.Sub(*row.WindowStart) > windowDuration {
		row.FailCount = 1
		start := now
		row.WindowStart = &start
	} else {
		row.FailCount++
	}

	if row.FailCount >= policy.MaxAttempts {
		until := now.Add(time.Duration(policy.LockoutMinutes) * time.Minute)
		row.LockedUntil = &until
		// 触发锁定后把连续失败计数归零：锁定期满后从 0 重新累计，
		// 否则期满后第一次失败就会立刻再次锁定（等于永久锁死）。
		row.FailCount = 0
		start := now
		row.WindowStart = &start
		logger.SysWarn(fmt.Sprintf("登录限流触发锁定：scope=%s subject=%s 阈值=%d 时长=%d分钟",
			policy.Scope, maskLoginSubjectKey(policy.Scope, subjectKey), policy.MaxAttempts, policy.LockoutMinutes))
		result.Locked = true
		result.Until = until
		result.Remaining = time.Duration(policy.LockoutMinutes) * time.Minute
	}

	if saveErr := db.Save(&row).Error; saveErr != nil {
		// 复合主键冲突（并发首次插入）：重试一次更新路径。
		if retryErr := db.Model(&entity.LoginAttempt{}).
			Where("scope = ? AND subject_key = ?", policy.Scope, subjectKey).
			Updates(map[string]any{
				"fail_count":   row.FailCount,
				"window_start": row.WindowStart,
				"locked_until": row.LockedUntil,
				"updated_at":   now,
			}).Error; retryErr != nil {
			logger.SysWarn("写入登录失败计数失败：" + retryErr.Error())
			return result, retryErr
		}
	}
	result.FailCount = row.FailCount
	return result, nil
}

// ClearLoginAttemptsOnSuccessWithDB 在登录成功时清空该 key 的连续失败计数与锁定。
//
// ⚠️ **window_start 也要重置**（不只是 fail_count）。这是「连续失败」语义的
// 核心：失败 9 次 → 成功 1 次 → 计数清零且窗口从头开始 → 再失败 10 次才锁。
// 若只清 fail_count 而不动 window_start，成功之后的下一次失败会被算进
// 旧窗口（now - window_start 仍可能小于窗口），「连续」就不成立了。
//
// policy.Enabled 为 false 时零行为。
func ClearLoginAttemptsOnSuccessWithDB(db *gorm.DB, policy LoginRateLimitPolicy, subjectKey string, now time.Time) error {
	if !policy.Enabled || db == nil || subjectKey == "" {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameLoginAttempt) {
		return nil
	}
	return db.Model(&entity.LoginAttempt{}).
		Where("scope = ? AND subject_key = ?", policy.Scope, subjectKey).
		Updates(map[string]any{
			"fail_count":   0,
			"window_start": &now, // 关键：成功时也重置窗口起点
			"locked_until": nil,
			"updated_at":   now,
		}).Error
}

// ClearLoginAttemptsFullyWithDB 是管理台「解除锁定」的入口：连行一起删掉。
//
// 与「成功时清零」的区别：这里删行（回到从未失败过的状态）。
// 运营解除的语义是「这个账号/IP 已排查过」，应当回到全新状态。
func ClearLoginAttemptsFullyWithDB(db *gorm.DB, scope string, subjectKey string) error {
	if db == nil || subjectKey == "" {
		return nil
	}
	if !db.Migrator().HasTable(entity.TableNameLoginAttempt) {
		return nil
	}
	return db.Where("scope = ? AND subject_key = ?", scope, subjectKey).
		Delete(&entity.LoginAttempt{}).Error
}

// LoginAttemptStatus 是给管理端列表用的只读快照。
type LoginAttemptStatus struct {
	Scope      string `json:"scope"`
	SubjectKey string `json:"subjectKey"`
	// SubjectMasked 是脱敏后的展示值（账号中间打码 / IP 只留前三段）。
	SubjectMasked string     `json:"subjectMasked"`
	FailCount     int        `json:"failCount"`
	LockedUntil   *time.Time `json:"lockedUntil,omitempty"`
	Locked        bool       `json:"locked"`
	Remaining     int        `json:"remainingMinutes"`
	WindowStart   *time.Time `json:"windowStart,omitempty"`
}

// ListLoginAttemptLocksWithDB 列出当前锁定中的条目（管理端「解除锁定」列表）。
//
// scope 为空时返回两个维度（account 与 ip）的全部锁定项。
func ListLoginAttemptLocksWithDB(db *gorm.DB, scope string, now time.Time, limit int) ([]LoginAttemptStatus, error) {
	if db == nil || !db.Migrator().HasTable(entity.TableNameLoginAttempt) {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := db.Where("locked_until IS NOT NULL AND locked_until > ?", now)
	if scope != "" {
		query = query.Where("scope = ?", scope)
	}
	var rows []entity.LoginAttempt
	if err := query.Order("locked_until desc").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	statuses := make([]LoginAttemptStatus, 0, len(rows))
	for i := range rows {
		row := rows[i]
		status := LoginAttemptStatus{
			Scope: row.Scope, SubjectKey: row.SubjectKey,
			SubjectMasked: maskLoginSubjectKey(row.Scope, row.SubjectKey),
			FailCount:     row.FailCount, LockedUntil: row.LockedUntil,
			WindowStart: row.WindowStart,
		}
		if row.LockedUntil != nil && now.Before(*row.LockedUntil) {
			status.Locked = true
			status.Remaining = LoginLockout{Locked: true, Remaining: row.LockedUntil.Sub(now)}.RemainingMinutesCeil()
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// maskLoginSubjectKey 按维度脱敏展示值。
//
// account 维度只露首尾各 1 字符；ip 维度用黑名单的既有脱敏函数。
// 列表页给运营看，但不该把完整用户名/完整 IP 摊在页面上。
func maskLoginSubjectKey(scope string, key string) string {
	if scope == entity.LoginAttemptScopeIP {
		return MaskBlacklistValue(BlacklistKindIP, key, BlacklistMatchExact)
	}
	runes := []rune(key)
	switch {
	case len(runes) > 4:
		return string(runes[:1]) + "***" + string(runes[len(runes)-1:])
	case len(runes) > 0:
		return string(runes[:1]) + "***"
	default:
		return "***"
	}
}

// ============================================================================
// 清理（防止表被撞库灌爆）
// ============================================================================

// LoginAttemptCleanupInterval 是清理任务的执行间隔。
//
// 取 10 分钟而不是 1 小时：这张表的行数与「最近一个窗口内的失败 key 数」成正比，
// 而窗口默认只有 15 分钟。间隔太长会让过期行在表里滞留数小时，
// 撞库攻击下就是几十万行。10 分钟的成本是一次带索引的 DELETE。
const LoginAttemptCleanupInterval = 10 * time.Minute

// CleanupStaleLoginAttemptsWithDB 删除「已经不可能再影响判定」的行。
//
// 可删的判据（两者同时成立）：
//
//	locked_until IS NULL OR locked_until <= now      —— 没有生效中的锁定
//	window_start IS NULL OR window_start <= cutoff   —— 计数窗口早已过期
//
// cutoff = now - maxWindow。用**所有 scope 中最大的窗口**作为统一阈值，
// 而不是按 scope 分别算：一行只知道自己的 scope，要按 scope 取不同窗口就得
// 先查配置（两次查询 + 分支），而统一用最大窗口只会让清理**晚一点**发生，
// 不会有正确性问题（多留一会儿行，判定仍按各自的窗口走）。
//
// ⚠️ 判定正确性**不依赖本函数**：RecordLoginFailureWithDB 自己在
// `now - window_start > window` 时会重置计数，所以即使过期行没被删，
// 也不会导致误锁。清理只解决**存储增长**，不参与判定 —— 这一点很重要：
// 它意味着「清理任务挂了」不会让限流失效，只会让表变大。
//
// 返回删除的行数（便于观测与测试断言）。
func CleanupStaleLoginAttemptsWithDB(db *gorm.DB, now time.Time) (int64, error) {
	if db == nil || !db.Migrator().HasTable(entity.TableNameLoginAttempt) {
		return 0, nil
	}
	// 取两个窗口配置里的较大值（普通模式窗口无上限，理论上可能很大；
	// 用 max 而不是硬编码，保证「配置调大窗口」时不会误删仍在计数窗口内的行）。
	maxWindow := GetLoginAttemptWindowMinutesWithDB(db)
	if ipWindow := GetBlacklistAdminLoginWindowMinutesWithDB(db); ipWindow > maxWindow {
		maxWindow = ipWindow
	}
	cutoff := now.Add(-time.Duration(maxWindow) * time.Minute)

	result := db.Where(
		"(locked_until IS NULL OR locked_until <= ?) AND (window_start IS NULL OR window_start <= ?)",
		now, cutoff,
	).Delete(&entity.LoginAttempt{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// StartLoginAttemptCleanup 启动后台清理，返回停止函数（幂等）。
//
// 与 StartIdempotencyCleanup 同构：单 goroutine + ticker + 幂等停止。
func StartLoginAttemptCleanup(db *gorm.DB, interval time.Duration) func() {
	if interval <= 0 {
		interval = LoginAttemptCleanupInterval
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				if _, err := CleanupStaleLoginAttemptsWithDB(db, now); err != nil {
					logger.SysWarn("清理过期登录限流记录失败：" + err.Error())
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
		<-done
	}
}
