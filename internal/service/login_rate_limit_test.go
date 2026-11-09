package service

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newLoginRateLimitTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:login-rate-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}, &entity.LoginAttempt{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

func setLoginSetting(t *testing.T, db *gorm.DB, key, value string) {
	t.Helper()
	if err := saveSystemSettingValue(db, key, value, "test"); err != nil {
		t.Fatalf("set %s=%q: %v", key, value, err)
	}
}

// ===========================================================================
// 「不允许关闭」的结构性保证
// ===========================================================================

// **核心验收**：管理员入口（ip 维度）没有任何 enabled 键可读，
// IPLoginPolicy 的 Enabled 恒为 true —— 无论库里写了什么。
func TestIPLoginPolicyIsNeverDisableable(t *testing.T) {
	db := newLoginRateLimitTestDB(t)

	// 就算有人手工往 system_settings 塞一个「看起来能关」的键，
	// ip 维度也不读它（结构上不存在该键的读取路径）。
	for _, key := range []string{
		"blacklist_admin_login_enabled",
		"BlacklistAdminLoginEnabled",
		"admin_login_enabled",
	} {
		setLoginSetting(t, db, key, "false")
	}
	// 把普通模式关掉。
	setLoginSetting(t, db, LoginRateLimitEnabledSettingKey, "false")

	ipPolicy := IPLoginPolicy(db)
	if !ipPolicy.Enabled {
		t.Fatal("ip 维度（管理员入口）必须恒为启用 —— 它不可关闭")
	}
	// 对照：普通模式确实被关掉了（证明上面的 false 写入是生效的，不是没写进去）。
	accountPolicy := AccountLoginPolicy(db)
	if accountPolicy.Enabled {
		t.Fatal("account 维度（普通模式）应当被 login_rate_limit_enabled=false 关闭")
	}
}

// 即使 login_rate_limit_enabled=false，ip 维度仍然计数与锁定。
func TestIPRateLimitStillLocksWhenAccountRateLimitDisabled(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	setLoginSetting(t, db, LoginRateLimitEnabledSettingKey, "false")
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

	ipPolicy := IPLoginPolicy(db)
	var last LoginLockout
	for i := 0; i < DefaultBlacklistAdminLoginMaxAttempt; i++ {
		var err error
		last, err = RecordLoginFailureWithDB(db, ipPolicy, "203.0.113.7", now)
		if err != nil {
			t.Fatalf("record #%d: %v", i, err)
		}
	}
	if !last.Locked {
		t.Fatal("ip 维度必须照常锁定（不可关闭）")
	}
}

// account 维度关闭时**零行为**（不读不写）。
func TestAccountRateLimitDisabledIsZeroBehavior(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	setLoginSetting(t, db, LoginRateLimitEnabledSettingKey, "false")
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

	policy := AccountLoginPolicy(db)
	if policy.Enabled {
		t.Fatal("precondition: account 维度应已关闭")
	}
	for i := 0; i < 50; i++ {
		lockout, err := RecordLoginFailureWithDB(db, policy, "some-user", now)
		if err != nil {
			t.Fatalf("record #%d: %v", i, err)
		}
		if lockout.Locked {
			t.Fatal("关闭的 account 维度不得锁定")
		}
	}
	var count int64
	if err := db.Model(&entity.LoginAttempt{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("关闭的 account 维度不得写行，got %d", count)
	}
}

// ===========================================================================
// 「连续失败」语义（不是窗口内累计）
// ===========================================================================

// **核心验收**：失败 9 次 → 成功 1 次 → 计数清零 → 再失败 10 次才锁。
//
// 这条用例是整个 t27 最容易实现错的地方：若成功时只清 fail_count 而不重置
// window_start，那么成功之后的下一次失败会被算进旧窗口而立刻触发锁定。
func TestConsecutiveFailuresResetOnSuccess(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	setLoginSetting(t, db, LoginRateLimitEnabledSettingKey, "true")
	setLoginSetting(t, db, LoginMaxAttemptsSettingKey, "10")
	setLoginSetting(t, db, LoginAttemptWindowMinutesSettingKey, "15")
	base := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	const user = "alice"
	policy := AccountLoginPolicy(db)

	// 失败 9 次（窗口内）。
	for i := 0; i < 9; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		lockout, err := RecordLoginFailureWithDB(db, policy, user, at)
		if err != nil {
			t.Fatalf("failure #%d: %v", i, err)
		}
		if lockout.Locked {
			t.Fatalf("failure #%d must not lock (threshold 10)", i)
		}
		if lockout.FailCount != i+1 {
			t.Fatalf("failure #%d failCount = %d, want %d", i, lockout.FailCount, i+1)
		}
	}

	// **成功 1 次** → 计数清零且 window_start 重置。
	successAt := base.Add(10 * time.Second)
	if err := ClearLoginAttemptsOnSuccessWithDB(db, policy, user, successAt); err != nil {
		t.Fatalf("clear on success: %v", err)
	}
	var row entity.LoginAttempt
	if err := db.Where("scope = ? AND subject_key = ?", entity.LoginAttemptScopeAccount, user).First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if row.FailCount != 0 {
		t.Fatalf("after success failCount = %d, want 0", row.FailCount)
	}
	// 关键断言：window_start 必须被重置到成功时刻。
	if row.WindowStart == nil || !row.WindowStart.Equal(successAt) {
		t.Fatalf("window_start = %v, want %v（成功时也必须重置窗口起点）", row.WindowStart, successAt)
	}

	// 再失败 9 次：不应锁定（若 window_start 没重置，旧窗口里已经有 9 次，
	// 第 1 次新失败就会被算成第 10 次而立刻锁）。
	for i := 0; i < 9; i++ {
		at := successAt.Add(time.Duration(i+1) * time.Second)
		lockout, err := RecordLoginFailureWithDB(db, policy, user, at)
		if err != nil {
			t.Fatalf("post-success failure #%d: %v", i, err)
		}
		if lockout.Locked {
			t.Fatalf("post-success failure #%d must not lock — 「连续」被破坏（window_start 未重置）", i)
		}
	}
	// 第 10 次才锁。
	lockout, err := RecordLoginFailureWithDB(db, policy, user, successAt.Add(11*time.Second))
	if err != nil {
		t.Fatalf("10th post-success failure: %v", err)
	}
	if !lockout.Locked {
		t.Fatal("10th consecutive failure must lock")
	}
	if lockout.Remaining != 15*time.Minute {
		t.Fatalf("lockout duration = %v, want 15m", lockout.Remaining)
	}
}

// 窗口过期 → 重新开始计数（不是累加）。
func TestFailuresResetAfterWindowExpiry(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	setLoginSetting(t, db, LoginMaxAttemptsSettingKey, "3")
	setLoginSetting(t, db, LoginAttemptWindowMinutesSettingKey, "15")
	base := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	const user = "bob"
	policy := AccountLoginPolicy(db)

	// 窗口内失败 2 次。
	for i := 0; i < 2; i++ {
		if _, err := RecordLoginFailureWithDB(db, policy, user, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	// 超过窗口（16 分钟）后再失败 → 计数重置为 1，不锁定。
	lockout, err := RecordLoginFailureWithDB(db, policy, user, base.Add(16*time.Minute))
	if err != nil {
		t.Fatalf("record after window: %v", err)
	}
	if lockout.Locked {
		t.Fatal("窗口过期后必须重新计数，不得直接锁定")
	}
	if lockout.FailCount != 1 {
		t.Fatalf("failCount after window = %d, want 1", lockout.FailCount)
	}
}

// ===========================================================================
// 默认值与参数边界
// ===========================================================================

func TestLoginRateLimitDefaults(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	if !GetLoginRateLimitEnabledWithDB(db) {
		t.Fatal("login_rate_limit_enabled 默认必须为 true")
	}
	if got := GetLoginMaxAttemptsWithDB(db); got != 10 {
		t.Fatalf("login_max_attempts 默认 = %d, want 10", got)
	}
	if got := GetLoginAttemptWindowMinutesWithDB(db); got != 15 {
		t.Fatalf("login_attempt_window_minutes 默认 = %d, want 15", got)
	}
	if got := GetLoginLockoutMinutesWithDB(db); got != 15 {
		t.Fatalf("login_lockout_minutes 默认 = %d, want 15", got)
	}
	if got := GetBlacklistAdminLoginWindowMinutesWithDB(db); got != 15 {
		t.Fatalf("blacklist_admin_login_window_minutes 默认 = %d, want 15", got)
	}
	if got := GetBlacklistAdminLoginMaxAttemptsWithDB(db); got != 5 {
		t.Fatalf("blacklist_admin_login_max_attempts 默认 = %d, want 5", got)
	}
	if got := GetBlacklistAdminLoginLockoutMinutesWithDB(db); got != 30 {
		t.Fatalf("blacklist_admin_login_lockout_minutes 默认 = %d, want 30", got)
	}
	// nil db / 缺表也回落默认。
	if !GetLoginRateLimitEnabledWithDB(nil) {
		t.Fatal("nil db 时 login_rate_limit_enabled 必须回落 true（保留防护）")
	}
	if got := GetLoginMaxAttemptsWithDB(nil); got != 10 {
		t.Fatalf("nil db login_max_attempts = %d, want 10", got)
	}
}

// 越界值**拒绝保存**（不静默 clamp），返回可映射成 400 的错误。
func TestValidateLoginRateLimitInputRejectsOutOfRange(t *testing.T) {
	below := func(v int) *int { return &v }

	cases := []struct {
		name  string
		input UpdateSystemSettingInput
		want  error
	}{
		{
			name:  "admin window below 15 rejected",
			input: UpdateSystemSettingInput{BlacklistAdminLoginWindowMinutes: below(14)},
			want:  ErrBlacklistAdminLoginWindowTooSmall,
		},
		{
			name:  "admin window 0 rejected",
			input: UpdateSystemSettingInput{BlacklistAdminLoginWindowMinutes: below(0)},
			want:  ErrBlacklistAdminLoginWindowTooSmall,
		},
		{
			name:  "admin attempts above 10 rejected",
			input: UpdateSystemSettingInput{BlacklistAdminLoginMaxAttempts: below(11)},
			want:  ErrBlacklistAdminLoginAttemptsTooLarge,
		},
		{
			name:  "admin attempts 0 rejected",
			input: UpdateSystemSettingInput{BlacklistAdminLoginMaxAttempts: below(0)},
			want:  ErrBlacklistAdminLoginAttemptsTooLarge,
		},
		{
			name:  "admin lockout 0 rejected",
			input: UpdateSystemSettingInput{BlacklistAdminLoginLockoutMinutes: below(0)},
			want:  ErrInvalidBlacklistAdminLoginLockout,
		},
		{
			name:  "account max attempts 0 rejected",
			input: UpdateSystemSettingInput{LoginMaxAttempts: below(0)},
			want:  ErrInvalidLoginRateLimitConfig,
		},
		{
			name:  "account window 0 rejected",
			input: UpdateSystemSettingInput{LoginAttemptWindowMinutes: below(0)},
			want:  ErrInvalidLoginRateLimitConfig,
		},
		{
			name:  "account lockout 0 rejected",
			input: UpdateSystemSettingInput{LoginLockoutMinutes: below(0)},
			want:  ErrInvalidLoginRateLimitConfig,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateLoginRateLimitInput(tc.input); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	// 边界内必须通过。
	ok := []UpdateSystemSettingInput{
		{BlacklistAdminLoginWindowMinutes: below(15)},   // 恰好下限
		{BlacklistAdminLoginWindowMinutes: below(1000)}, // 无上限
		{BlacklistAdminLoginMaxAttempts: below(1)},      // 恰好下限
		{BlacklistAdminLoginMaxAttempts: below(10)},     // 恰好上限
		{LoginMaxAttempts: below(1)},                    // 普通模式无上下限
		{LoginMaxAttempts: below(100000)},               // 无上限
		{LoginAttemptWindowMinutes: below(1000)},        // 无上限
		{},                                              // 全部 nil = 保持现值
	}
	for i, input := range ok {
		if err := ValidateLoginRateLimitInput(input); err != nil {
			t.Fatalf("case %d should pass validation: %v", i, err)
		}
	}
}

// 读取时也强制边界（判定必须与保存校验一致）。
func TestReadsClampToSameBoundaries(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	// 手工写入越界值（模拟历史遗留 / 直接改库）。
	setLoginSetting(t, db, BlacklistAdminLoginWindowMinutesSettingKey, "5")
	setLoginSetting(t, db, BlacklistAdminLoginMaxAttemptsSettingKey, "50")
	if got := GetBlacklistAdminLoginWindowMinutesWithDB(db); got != MinBlacklistAdminLoginWindowMinutes {
		t.Fatalf("window read = %d, want clamped to %d", got, MinBlacklistAdminLoginWindowMinutes)
	}
	if got := GetBlacklistAdminLoginMaxAttemptsWithDB(db); got != MaxBlacklistAdminLoginMaxAttempts {
		t.Fatalf("attempts read = %d, want clamped to %d", got, MaxBlacklistAdminLoginMaxAttempts)
	}
}

// ===========================================================================
// 边界情况
// ===========================================================================

// 账号不存在也计数，且 key 用输入的 username（不查库）。
func TestUnknownAccountStillCounts(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	policy := AccountLoginPolicy(db)
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

	lockout, err := RecordLoginFailureWithDB(db, policy, "no-such-user", now)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if lockout.FailCount != 1 {
		t.Fatalf("failCount = %d, want 1（不存在的账号也要计数）", lockout.FailCount)
	}
}

// 空 username 不计数（否则所有空用户名请求共用一个桶）。
func TestEmptyAccountKeyDoesNotCount(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	policy := AccountLoginPolicy(db)
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		if _, err := RecordLoginFailureWithDB(db, policy, "", now); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	var count int64
	if err := db.Model(&entity.LoginAttempt{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("空 key 不得写行，got %d", count)
	}
}

// IP 归一化：v4-mapped 收敛，无法解析不返回空串。
func TestNormalizeLoginIPKey(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"  203.0.113.7  ":      "203.0.113.7",
		"::ffff:203.0.113.7":   "203.0.113.7",
		"2001:DB8:0:0:0:0:0:1": "2001:db8::1",
	}
	for input, want := range cases {
		if got := NormalizeLoginIPKey(input); got != want {
			t.Fatalf("normalize %q = %q, want %q", input, got, want)
		}
	}
	if got := NormalizeLoginIPKey("   "); got != "" {
		t.Fatalf("blank = %q, want empty", got)
	}
	if got := NormalizeLoginIPKey("not-an-ip"); got == "" {
		t.Fatal("无法解析的 IP 不得归一化成空串（会让所有异常请求共用一个桶）")
	}
}

// 账号 key 只 TrimSpace，不转小写（用户名大小写敏感）。
func TestNormalizeLoginAccountKeyKeepsCase(t *testing.T) {
	if got := NormalizeLoginAccountKey("  AdminUser  "); got != "AdminUser" {
		t.Fatalf("key = %q, want AdminUser（不转小写）", got)
	}
	if got := NormalizeLoginAccountKey("   "); got != "" {
		t.Fatalf("blank = %q, want empty", got)
	}
	// 超长按字符截断到 128。
	long := strings.Repeat("用", 200)
	if got := NormalizeLoginAccountKey(long); len([]rune(got)) != 128 {
		t.Fatalf("truncated runes = %d, want 128", len([]rune(got)))
	}
}

// 锁定期满自动解除（不再有指数退避：每次锁定时长都是配置值）。
func TestLoginLockoutExpiresWithFlatDuration(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	setLoginSetting(t, db, LoginMaxAttemptsSettingKey, "2")
	setLoginSetting(t, db, LoginLockoutMinutesSettingKey, "15")
	base := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	const user = "carol"
	policy := AccountLoginPolicy(db)

	for i := 0; i < 2; i++ {
		if _, err := RecordLoginFailureWithDB(db, policy, user, base); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	status, err := CheckLoginLockoutWithDB(db, policy, user, base.Add(time.Minute))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !status.Locked {
		t.Fatal("must be locked after 2 failures")
	}
	// 期满 → 自动解除。
	status, err = CheckLoginLockoutWithDB(db, policy, user, base.Add(16*time.Minute))
	if err != nil {
		t.Fatalf("check after expiry: %v", err)
	}
	if status.Locked {
		t.Fatal("lockout must expire")
	}
}

// 两个维度互不影响（同一 IP 的 account 锁定不影响另一账号）。
func TestAccountAndIPScopesAreIndependent(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	// ⚠️ 两套限流的阈值是**不同的键**：只设 login_max_attempts 不会影响 ip 维度
	// （默认仍是 5）。这里两个都要设成 1，否则 ip 维度要失败 5 次才锁。
	setLoginSetting(t, db, LoginMaxAttemptsSettingKey, "1")
	setLoginSetting(t, db, BlacklistAdminLoginMaxAttemptsSettingKey, "1")
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

	accountPolicy := AccountLoginPolicy(db)
	ipPolicy := IPLoginPolicy(db)

	// 锁住账号 alice。
	if _, err := RecordLoginFailureWithDB(db, accountPolicy, "alice", now); err != nil {
		t.Fatalf("record account: %v", err)
	}
	// 锁住 IP。
	if _, err := RecordLoginFailureWithDB(db, ipPolicy, "203.0.113.7", now); err != nil {
		t.Fatalf("record ip: %v", err)
	}

	alice, _ := CheckLoginLockoutWithDB(db, accountPolicy, "alice", now)
	bob, _ := CheckLoginLockoutWithDB(db, accountPolicy, "bob", now)
	ip, _ := CheckLoginLockoutWithDB(db, ipPolicy, "203.0.113.7", now)
	otherIP, _ := CheckLoginLockoutWithDB(db, ipPolicy, "203.0.113.8", now)

	if !alice.Locked {
		t.Fatal("alice 应被锁定")
	}
	if bob.Locked {
		t.Fatal("bob 不应受影响")
	}
	if !ip.Locked {
		t.Fatal("该 IP 应被锁定")
	}
	if otherIP.Locked {
		t.Fatal("另一个 IP 不应受影响")
	}
}

// 管理端列表只返回锁定中的条目，且按 scope 过滤。
func TestListLoginAttemptLocksFiltersByScope(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	setLoginSetting(t, db, LoginMaxAttemptsSettingKey, "1")
	setLoginSetting(t, db, BlacklistAdminLoginMaxAttemptsSettingKey, "1")
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

	if _, err := RecordLoginFailureWithDB(db, AccountLoginPolicy(db), "dave", now); err != nil {
		t.Fatalf("record account: %v", err)
	}
	if _, err := RecordLoginFailureWithDB(db, IPLoginPolicy(db), "203.0.113.7", now); err != nil {
		t.Fatalf("record ip: %v", err)
	}

	all, err := ListLoginAttemptLocksWithDB(db, "", now.Add(time.Minute), 100)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all locks = %d, want 2", len(all))
	}
	// 脱敏：列表不得暴露完整用户名/完整 IP。
	for _, item := range all {
		if item.SubjectMasked == item.SubjectKey {
			t.Fatalf("subject must be masked: %+v", item)
		}
	}

	accounts, err := ListLoginAttemptLocksWithDB(db, entity.LoginAttemptScopeAccount, now.Add(time.Minute), 100)
	if err != nil {
		t.Fatalf("list account: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Scope != entity.LoginAttemptScopeAccount {
		t.Fatalf("account locks = %+v, want exactly 1 account entry", accounts)
	}

	// 过期后为空。
	expired, err := ListLoginAttemptLocksWithDB(db, "", now.Add(2*time.Hour), 100)
	if err != nil {
		t.Fatalf("list expired: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("expired locks must not be listed, got %d", len(expired))
	}
}

// 手动解除（连行删除）。
func TestClearLoginAttemptsFullyRemovesRow(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	setLoginSetting(t, db, LoginMaxAttemptsSettingKey, "1")
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	policy := AccountLoginPolicy(db)

	if _, err := RecordLoginFailureWithDB(db, policy, "erin", now); err != nil {
		t.Fatalf("record: %v", err)
	}
	if status, _ := CheckLoginLockoutWithDB(db, policy, "erin", now); !status.Locked {
		t.Fatal("precondition: must be locked")
	}
	if err := ClearLoginAttemptsFullyWithDB(db, entity.LoginAttemptScopeAccount, "erin"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	var count int64
	if err := db.Model(&entity.LoginAttempt{}).
		Where("scope = ? AND subject_key = ?", entity.LoginAttemptScopeAccount, "erin").Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("手动解除必须删行，got %d", count)
	}
}

// RemainingMinutesCeil 向上取整，最少 1。
func TestLoginLockoutRemainingMinutesCeil(t *testing.T) {
	if got := (LoginLockout{}).RemainingMinutesCeil(); got != 0 {
		t.Fatalf("not locked = %d, want 0", got)
	}
	if got := (LoginLockout{Locked: true, Remaining: 30 * time.Second}).RemainingMinutesCeil(); got != 1 {
		t.Fatalf("30s = %d, want 1", got)
	}
	if got := (LoginLockout{Locked: true, Remaining: 61 * time.Second}).RemainingMinutesCeil(); got != 2 {
		t.Fatalf("61s = %d, want 2", got)
	}
}

// 缺表时按未锁定处理（fail-open）。
func TestLoginRateLimitWithoutTableIsNotLocked(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:login-rate-notable-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate settings: %v", err)
	}
	status, err := CheckLoginLockoutWithDB(db, AccountLoginPolicy(db), "someone", time.Now())
	if err != nil {
		t.Fatalf("check without table must not fail: %v", err)
	}
	if status.Locked {
		t.Fatal("缺表必须按未锁定处理")
	}
}

// ===========================================================================
// 清理机制（防止撞库把表灌爆）
// ===========================================================================

// 撞库会灌入大量随机 username → 表必须能自动收缩。
// 这条用例构造「过期且无生效锁定」的行，断言清理会删掉它们。
func TestCleanupStaleLoginAttemptsRemovesExpiredRows(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)

	// 三条过期行（窗口早已过期、无锁定）。
	for _, user := range []string{"attacker-1", "attacker-2", "attacker-3"} {
		if err := db.Create(&entity.LoginAttempt{
			Scope: entity.LoginAttemptScopeAccount, SubjectKey: user,
			FailCount: 3, WindowStart: &old,
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", user, err)
		}
	}
	// 一条**生效中**的锁定（不得被删）。
	future := now.Add(20 * time.Minute)
	if err := db.Create(&entity.LoginAttempt{
		Scope: entity.LoginAttemptScopeIP, SubjectKey: "203.0.113.7",
		FailCount: 0, WindowStart: &old, LockedUntil: &future,
	}).Error; err != nil {
		t.Fatalf("seed locked row: %v", err)
	}
	// 一条窗口内的活跃计数行（不得被删）。
	recent := now.Add(-1 * time.Minute)
	if err := db.Create(&entity.LoginAttempt{
		Scope: entity.LoginAttemptScopeAccount, SubjectKey: "active-user",
		FailCount: 2, WindowStart: &recent,
	}).Error; err != nil {
		t.Fatalf("seed active row: %v", err)
	}

	deleted, err := CleanupStaleLoginAttemptsWithDB(db, now)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("deleted = %d, want 3（只删过期且无生效锁定的行）", deleted)
	}

	var remaining []entity.LoginAttempt
	if err := db.Order("subject_key").Find(&remaining).Error; err != nil {
		t.Fatalf("load remaining: %v", err)
	}
	if len(remaining) != 2 {
		t.Fatalf("remaining = %d, want 2", len(remaining))
	}
	keys := map[string]bool{}
	for _, row := range remaining {
		keys[row.SubjectKey] = true
	}
	if !keys["203.0.113.7"] {
		t.Fatal("生效中的锁定行不得被删除")
	}
	if !keys["active-user"] {
		t.Fatal("窗口内的活跃计数行不得被删除")
	}
}

// **判定不依赖清理**：即使过期行没被删，失败计数也会按窗口重置，不会误锁。
//
// 这条是「清理任务挂了会怎样」的答案：只会让表变大，不会让限流出错。
func TestStaleRowsDoNotCauseFalseLockWithoutCleanup(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	setLoginSetting(t, db, LoginMaxAttemptsSettingKey, "3")
	setLoginSetting(t, db, LoginAttemptWindowMinutesSettingKey, "15")
	base := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	const user = "victim"
	policy := AccountLoginPolicy(db)

	// 造一条「窗口早已过期、fail_count 已经是 2」的脏行（模拟清理没跑）。
	old := base.Add(-2 * time.Hour)
	if err := db.Create(&entity.LoginAttempt{
		Scope: entity.LoginAttemptScopeAccount, SubjectKey: user,
		FailCount: 2, WindowStart: &old,
	}).Error; err != nil {
		t.Fatalf("seed stale row: %v", err)
	}

	// 窗口过期后的第一次失败必须**重置为 1**，而不是累加成 3 触发锁定。
	lockout, err := RecordLoginFailureWithDB(db, policy, user, base)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if lockout.Locked {
		t.Fatal("过期脏行不得导致误锁（窗口过期必须重置计数）")
	}
	if lockout.FailCount != 1 {
		t.Fatalf("failCount = %d, want 1（窗口过期后重新计）", lockout.FailCount)
	}
}

// 清理对缺表 / nil db 安全（不 panic、不报错）。
func TestCleanupStaleLoginAttemptsHandlesMissingTable(t *testing.T) {
	if deleted, err := CleanupStaleLoginAttemptsWithDB(nil, time.Now()); err != nil || deleted != 0 {
		t.Fatalf("nil db = (%d, %v), want (0, nil)", deleted, err)
	}
	db, err := gorm.Open(sqlite.Open("file:login-clean-notable-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate settings: %v", err)
	}
	if deleted, err := CleanupStaleLoginAttemptsWithDB(db, time.Now()); err != nil || deleted != 0 {
		t.Fatalf("missing table = (%d, %v), want (0, nil)", deleted, err)
	}
}

// 后台清理任务可启动、可幂等停止。
func TestStartLoginAttemptCleanupStopsCleanly(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	old := time.Now().Add(-2 * time.Hour)
	if err := db.Create(&entity.LoginAttempt{
		Scope: entity.LoginAttemptScopeAccount, SubjectKey: "stale", FailCount: 1, WindowStart: &old,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	stop := StartLoginAttemptCleanup(db, 10*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		if err := db.Model(&entity.LoginAttempt{}).Count(&count).Error; err != nil {
			stop()
			t.Fatalf("count: %v", err)
		}
		if count == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	stop() // 幂等

	var count int64
	if err := db.Model(&entity.LoginAttempt{}).Count(&count).Error; err != nil {
		t.Fatalf("count after stop: %v", err)
	}
	if count != 0 {
		t.Fatalf("后台清理应删掉过期行，remaining = %d", count)
	}
}

// ===========================================================================
// Q67：普通模式 IP 维度 + 白名单
// ===========================================================================

// 默认值 20（不是 30）：阈值必须低于攻击者典型探测规模。
func TestLoginIPMaxAttemptsDefaultIs20(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	if got := GetLoginIPMaxAttemptsWithDB(db); got != 20 {
		t.Fatalf("login_ip_max_attempts 默认 = %d, want 20", got)
	}
	if got := GetLoginIPMaxAttemptsWithDB(nil); got != 20 {
		t.Fatalf("nil db = %d, want 20", got)
	}
	// 非法值回落默认（不能变成 0 = 一失败就锁）。
	for _, bad := range []string{"", "0", "-1", "abc"} {
		setLoginSetting(t, db, LoginIPMaxAttemptsSettingKey, bad)
		if got := GetLoginIPMaxAttemptsWithDB(db); got != 20 {
			t.Fatalf("值 %q 读出 %d, want 20", bad, got)
		}
	}
	// 合法值可读，无上限。
	setLoginSetting(t, db, LoginIPMaxAttemptsSettingKey, "1000")
	if got := GetLoginIPMaxAttemptsWithDB(db); got != 1000 {
		t.Fatalf("1000 读出 %d", got)
	}
}

// 白名单解析：支持 IP 与 CIDR、多种分隔符；坏项跳过并告警（读取路径宽容）。
func TestParseLoginRateLimitExemptIPs(t *testing.T) {
	// 空 → nil。
	if got := ParseLoginRateLimitExemptIPs("   "); got != nil {
		t.Fatalf("空文本 = %v, want nil", got)
	}
	// 混合：单个 IP + CIDR + 换行/逗号/分号分隔。
	networks := ParseLoginRateLimitExemptIPs("203.0.113.7, 198.51.100.0/24;10.0.0.0/8\n2001:db8::/32")
	if len(networks) != 4 {
		t.Fatalf("解析出 %d 项, want 4", len(networks))
	}
	// 单个 IP 按 /32 处理。
	if !networks[0].Contains(netIP(t, "203.0.113.7")) {
		t.Fatal("单 IP 项应能命中自身")
	}
	if networks[0].Contains(netIP(t, "203.0.113.8")) {
		t.Fatal("单 IP 项不应命中邻居")
	}
	if !networks[1].Contains(netIP(t, "198.51.100.200")) {
		t.Fatal("CIDR 项应能命中段内地址")
	}
	// 坏项跳过（不影响其他项）。
	withBad := ParseLoginRateLimitExemptIPs("203.0.113.7, not-an-ip, 198.51.100.0/24, 1.2.3.4/99")
	if len(withBad) != 2 {
		t.Fatalf("坏项应被跳过，剩 %d 项, want 2", len(withBad))
	}
}

// IsLoginRateLimitExemptIP：v4-mapped 收敛。
func TestIsLoginRateLimitExemptIPNormalizes(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	setLoginSetting(t, db, LoginRateLimitExemptIPsSettingKey, "203.0.113.7")
	if !IsLoginRateLimitExemptIP(db, "203.0.113.7") {
		t.Fatal("应命中白名单")
	}
	if !IsLoginRateLimitExemptIP(db, "::ffff:203.0.113.7") {
		t.Fatal("v4-mapped 写法应命中同一条")
	}
	if IsLoginRateLimitExemptIP(db, "203.0.113.8") {
		t.Fatal("邻居不应命中")
	}
	if IsLoginRateLimitExemptIP(db, "") {
		t.Fatal("空 IP 不应命中")
	}
	if IsLoginRateLimitExemptIP(db, "not-an-ip") {
		t.Fatal("无法解析的值不应命中")
	}
	// 缺表 / nil db 安全。
	if IsLoginRateLimitExemptIP(nil, "203.0.113.7") {
		t.Fatal("nil db 时不得命中")
	}
}

// 保存时**拒绝**坏项（与读取时跳过坏项不同）。
func TestValidateLoginRateLimitExemptIPsRejectsBadItems(t *testing.T) {
	// 合法：空、单 IP、CIDR、混合。
	for _, ok := range []string{"", "   ", "203.0.113.7", "198.51.100.0/24", "203.0.113.7, 10.0.0.0/8", "2001:db8::/32"} {
		if err := ValidateLoginRateLimitExemptIPs(ok); err != nil {
			t.Fatalf("%q 应合法: %v", ok, err)
		}
	}
	// 非法：坏 IP、坏 CIDR、域名。
	for _, bad := range []string{"not-an-ip", "1.2.3.4/99", "example.com", "203.0.113.7 bad"} {
		if err := ValidateLoginRateLimitExemptIPs(bad); err == nil {
			t.Fatalf("%q 应被拒绝", bad)
		} else if !errors.Is(err, ErrInvalidLoginRateLimitExemptIPs) {
			t.Fatalf("%q 错误类型 = %v, want ErrInvalidLoginRateLimitExemptIPs", bad, err)
		}
	}
}

// NormalIPLoginPolicy 跟随普通模式开关；IPLoginPolicy 恒为启用。
func TestNormalIPPolicyFollowsSwitchButStrictPolicyDoesNot(t *testing.T) {
	db := newLoginRateLimitTestDB(t)

	// 开关默认开。
	if !NormalIPLoginPolicy(db).Enabled {
		t.Fatal("普通模式开时 NormalIPLoginPolicy 应启用")
	}
	if got := NormalIPLoginPolicy(db).MaxAttempts; got != 20 {
		t.Fatalf("NormalIPLoginPolicy 阈值 = %d, want 20", got)
	}
	if got := IPLoginPolicy(db).MaxAttempts; got != 5 {
		t.Fatalf("IPLoginPolicy 阈值 = %d, want 5", got)
	}
	// 两者共用同一个 ip scope。
	if NormalIPLoginPolicy(db).Scope != IPLoginPolicy(db).Scope {
		t.Fatal("两套 IP 策略必须共用同一 scope（同一张表、同一批行）")
	}

	// 关掉普通模式。
	setLoginSetting(t, db, LoginRateLimitEnabledSettingKey, "false")
	if NormalIPLoginPolicy(db).Enabled {
		t.Fatal("关闭普通模式后 NormalIPLoginPolicy 应停用")
	}
	if !IPLoginPolicy(db).Enabled {
		t.Fatal("IPLoginPolicy 必须恒为启用（结构性不可关闭）")
	}
}

// 白名单保存语义：nil=保持现值，空串=真的清空。
func TestResolveLoginRateLimitExemptIPsSemantics(t *testing.T) {
	db := newLoginRateLimitTestDB(t)
	setLoginSetting(t, db, LoginRateLimitExemptIPsSettingKey, "203.0.113.7")

	// nil = 未提交 = 保持现值。
	if got := resolveLoginRateLimitExemptIPs(db, nil); got != "203.0.113.7" {
		t.Fatalf("nil = %q, want 保持现值", got)
	}
	// 空串 = 显式清空（不能用「空值=未提交」判断，必须靠指针 nil）。
	empty := ""
	if got := resolveLoginRateLimitExemptIPs(db, &empty); got != "" {
		t.Fatalf("空串 = %q, want 清空", got)
	}
	// 正常值。
	value := "  198.51.100.0/24  "
	if got := resolveLoginRateLimitExemptIPs(db, &value); got != "198.51.100.0/24" {
		t.Fatalf("应 trim，got %q", got)
	}
}

// ValidateLoginRateLimitInput 覆盖新键的边界。
func TestValidateLoginRateLimitInputCoversNewKeys(t *testing.T) {
	zero, ok20 := 0, 20
	badIPs := "not-an-ip"
	goodIPs := "203.0.113.7"

	if err := ValidateLoginRateLimitInput(UpdateSystemSettingInput{LoginIPMaxAttempts: &zero}); !errors.Is(err, ErrInvalidLoginIPMaxAttempts) {
		t.Fatalf("login_ip_max_attempts=0 应拒绝, got %v", err)
	}
	if err := ValidateLoginRateLimitInput(UpdateSystemSettingInput{LoginIPMaxAttempts: &ok20}); err != nil {
		t.Fatalf("20 应合法: %v", err)
	}
	if err := ValidateLoginRateLimitInput(UpdateSystemSettingInput{LoginRateLimitExemptIPs: &badIPs}); !errors.Is(err, ErrInvalidLoginRateLimitExemptIPs) {
		t.Fatalf("坏 IP 白名单应拒绝, got %v", err)
	}
	if err := ValidateLoginRateLimitInput(UpdateSystemSettingInput{LoginRateLimitExemptIPs: &goodIPs}); err != nil {
		t.Fatalf("好 IP 白名单应合法: %v", err)
	}
}

// netIP 是测试用的 IP 解析助手（解析失败直接 fail，避免 nil 静默）。
func netIP(t *testing.T, value string) net.IP {
	t.Helper()
	ip := net.ParseIP(value)
	if ip == nil {
		t.Fatalf("测试内 IP 解析失败：%q", value)
	}
	return ip
}
