package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

// 「自定义时长封禁」的换算与时区判据（t203）。
//
// # 为什么这些用例必须存在
//
// 「封 7 天」这类需求有两个互相独立的失效方向，而且都不会报错：
//
//	① 换算错（按 24h 手算 vs AddDate）→ 夏令时切换日差 1 小时，且只在某些日期错
//	② 时区混用（本地时间写进库、UTC 读出来）→ 跨时区部署时「7 天」变成 7 天 ± 时差
//
// ② 正是本仓踩过 3 次的「本地绿 CI 红」。所以这里的断言一律**按 UTC 比较**，
// 且用例本身不假设进程时区 —— 在 TZ=UTC 与 TZ=Asia/Shanghai 下结果必须相同。

func newUserBanDurationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := util.SQLiteConcurrentDSN(
		"file:user-ban-duration-" + t.Name() + "?mode=memory&cache=shared")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.UserBan{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

// 判据：单位为小时/天时，到期时刻 = now + N（小时|天），且**返回 UTC**。
//
// 断言用「与 now.Add(...) 相等」而不是「与手算秒数相等」：
// 后者在夏令时切换日会差 1 小时，而 AddDate 是正确语义。
func TestResolveUserBanExpiresAtUnits(t *testing.T) {
	// 固定一个远离夏令时切换的基准时刻，避免用例本身受运行环境时区影响。
	now := time.Date(2026, 3, 10, 8, 30, 0, 0, time.UTC)

	cases := []struct {
		name     string
		amount   int
		unit     string
		expected time.Time
	}{
		{"1 小时", 1, UserBanDurationUnitHour, now.Add(time.Hour)},
		{"24 小时", 24, UserBanDurationUnitHour, now.Add(24 * time.Hour)},
		{"1 天", 1, UserBanDurationUnitDay, now.AddDate(0, 0, 1)},
		{"7 天", 7, UserBanDurationUnitDay, now.AddDate(0, 0, 7)},
		{"30 天", 30, UserBanDurationUnitDay, now.AddDate(0, 0, 30)},
		// 单位别名必须都归一化到同一结果（前端/运营可能传中文或缩写）。
		{"7 天（中文单位）", 7, "天", now.AddDate(0, 0, 7)},
		{"2 小时（中文单位）", 2, "小时", now.Add(2 * time.Hour)},
		{"1 天（缩写 d）", 1, "d", now.AddDate(0, 0, 1)},
	}
	for _, testCase := range cases {
		got, err := ResolveUserBanExpiresAt(testCase.amount, testCase.unit, now)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", testCase.name, err)
		}
		if got == nil {
			t.Fatalf("%s: expected a non-nil expiresAt", testCase.name)
		}
		if !got.Equal(testCase.expected) {
			t.Fatalf("%s: expiresAt = %v, want %v", testCase.name, got, testCase.expected)
		}
		// 必须返回 UTC：跨时区部署时混用本地时间会让时长偏 ±N 小时。
		if got.Location() != time.UTC {
			t.Fatalf("%s: location = %v, want UTC", testCase.name, got.Location())
		}
	}
}

// 判据：amount <= 0 = 永久封禁（返回 nil），不是「立刻到期」。
//
// 这个区分极其关键：返回零值 time.Time 会让封禁一写入就失效，
// 而返回 nil 才是「永不自动解除」。两者在界面上都是「没填时长」。
func TestResolveUserBanExpiresAtPermanentForNonPositiveAmount(t *testing.T) {
	now := time.Now().UTC()
	for _, amount := range []int{0, -1} {
		got, err := ResolveUserBanExpiresAt(amount, UserBanDurationUnitDay, now)
		if err != nil {
			t.Fatalf("amount=%d: unexpected error: %v", amount, err)
		}
		if got != nil {
			t.Fatalf("amount=%d: expected nil expiresAt (= permanent), got %v", amount, *got)
		}
	}
}

// 判据：非法单位必须报错，不能静默退化成永久或任意时长。
//
// ⚠️ 函数名保留 `RejectsInvalidInput` 但**不再含「超上限」**：用户 2026-11-14 指令
// 「封禁时间不要有限制」，365 天上限已删。上限的「不存在」由
// TestResolveUserBanExpiresAtHasNoUpperLimit 正向钉住。
func TestResolveUserBanExpiresAtRejectsInvalidInput(t *testing.T) {
	now := time.Now().UTC()

	// 判据：未知单位 → 错（不能默认按小时，那会让「填了 3 周」变成 3 小时）。
	if _, err := ResolveUserBanExpiresAt(3, "week", now); err == nil {
		t.Fatal("unknown unit must be rejected, not silently defaulted")
	}
	// ⚠️ 这里**不再**有「超过 365 天必须被拒」的断言：用户 2026-11-14 明确指令
	// 「封禁时间不要有限制」，上限已删除。「更长时间请选永久封禁」只是界面引导，不是硬拦。
	//
	// 而「上限不存在」这件事必须有**正向**用例，否则删不删约束都不会红 ——
	// 与「时长分支零覆盖」是同一类静默失效。见 TestResolveUserBanExpiresAtHasNoUpperLimit。
}

// 判据：**没有上限** —— 超过旧上限（365 天）的时长必须正常换算，且结果正确。
//
// # 为什么必须是正向断言（这条用例的全部价值）
//
// 用户 2026-11-14 要求「封禁时间不要有限制」，原本的 `amount > 365 → 400` 已删。
// 如果只保留「365 天以内正常」的用例，那么：
//
//	把上限加回去 → 所有既有用例仍然通过（它们都在 365 以内）→ 无告警
//
// 也就是说「限制是否存在」不可观测。所以这里专门断言 366 天 / 1000 天 / 8760 小时
// **成功返回且 expiresAt 精确正确**：只有上限真的不存在时才通过。
//
// 变异验证方向：把 `if amount > 365 { return 400 }` 加回去 → 本用例必红。
func TestResolveUserBanExpiresAtHasNoUpperLimit(t *testing.T) {
	now := time.Date(2026, 3, 10, 8, 30, 0, 0, time.UTC)

	cases := []struct {
		name     string
		amount   int
		unit     string
		expected time.Time
	}{
		// 旧上限 365 天的两侧各测一个：365 必须通过（边界内），366 也必须通过（边界外）。
		{"边界内 365 天", 365, UserBanDurationUnitDay, now.AddDate(0, 0, 365)},
		{"超出旧上限 366 天", 366, UserBanDurationUnitDay, now.AddDate(0, 0, 366)},
		{"远超旧上限 1000 天", 1000, UserBanDurationUnitDay, now.AddDate(0, 0, 1000)},
		{"极端 9999 天（曾被认为误操作）", 9999, UserBanDurationUnitDay, now.AddDate(0, 0, 9999)},
		// 小时模式同样无上限：8761 小时 = 365 天 + 1 小时，旧上限按天折算会拒掉它。
		{"超出旧上限 8761 小时", 8761, UserBanDurationUnitHour, now.Add(8761 * time.Hour)},
		{"365 天整（8760 小时）", 8760, UserBanDurationUnitHour, now.Add(8760 * time.Hour)},
	}
	for _, testCase := range cases {
		got, err := ResolveUserBanExpiresAt(testCase.amount, testCase.unit, now)
		if err != nil {
			t.Fatalf("%s: 必须通过（用户要求时长无上限），实际报错 %v", testCase.name, err)
		}
		if got == nil {
			t.Fatalf("%s: expected a non-nil expiresAt", testCase.name)
		}
		if !got.Equal(testCase.expected) {
			t.Fatalf("%s: expiresAt = %v, want %v", testCase.name, got, testCase.expected)
		}
		if got.Location() != time.UTC {
			t.Fatalf("%s: location = %v, want UTC", testCase.name, got.Location())
		}
	}
}

// 判据：时长封禁写入**正确的到期时间**，且到期后判定为不封禁。
//
// 端到端：ResolveUserBanExpiresAt → Ban → 读回 user_bans.expires_at → IsBannedAt。
// 断言读回的绝对时刻，而不是只断言「非空」—— 后者在「写成 now」时也通过。
func TestBanWithDurationPersistsCorrectExpiry(t *testing.T) {
	db := newUserBanDurationTestDB(t)
	user := entity.User{Username: "duration-ban", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	now := time.Now().UTC()
	expiresAt, err := ResolveUserBanExpiresAt(7, UserBanDurationUnitDay, now)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := Ban(db, UserBanRequest{
		UserID: user.ID, Reason: "违规刷单", ExpiresAt: expiresAt, OperatorUserID: 1, Now: now,
	}); err != nil {
		t.Fatalf("Ban: %v", err)
	}

	// ① 落库的到期时间必须等于换算结果（按秒比较，规避 SQLite 时间精度差异）。
	var row entity.UserBan
	if err := db.Where("user_id = ?", user.ID).First(&row).Error; err != nil {
		t.Fatalf("load ban row: %v", err)
	}
	if row.ExpiresAt == nil {
		t.Fatal("a duration ban must persist expires_at (nil would make it permanent)")
	}
	if diff := row.ExpiresAt.Sub(*expiresAt); diff > time.Second || diff < -time.Second {
		t.Fatalf("persisted expires_at = %v, want ~%v (diff %v)", *row.ExpiresAt, *expiresAt, diff)
	}

	// ② 封禁中判 true，到期后判 false —— 后者正是「临时封禁实际是永久的」那个缺陷的钉子。
	bannedDuring, err := IsBannedAt(db, user.ID, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("IsBannedAt during: %v", err)
	}
	if !bannedDuring {
		t.Fatal("user must be banned while the duration ban is in effect")
	}
	bannedAfter, err := IsBannedAt(db, user.ID, now.AddDate(0, 0, 8))
	if err != nil {
		t.Fatalf("IsBannedAt after: %v", err)
	}
	if bannedAfter {
		t.Fatal("an expired duration ban must no longer be judged as banned")
	}
}

// 判据：永久封禁（ExpiresAt 为 nil）落库 expires_at 为 NULL，且永不到期。
func TestBanPermanentPersistsNullExpiry(t *testing.T) {
	db := newUserBanDurationTestDB(t)
	user := entity.User{Username: "permanent-ban", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	now := time.Now().UTC()
	if _, err := Ban(db, UserBanRequest{
		UserID: user.ID, Reason: "长期违规", OperatorUserID: 1, Now: now,
	}); err != nil {
		t.Fatalf("Ban: %v", err)
	}
	var row entity.UserBan
	if err := db.Where("user_id = ?", user.ID).First(&row).Error; err != nil {
		t.Fatalf("load ban row: %v", err)
	}
	if row.ExpiresAt != nil {
		t.Fatalf("permanent ban must persist NULL expires_at, got %v", *row.ExpiresAt)
	}
	// 十年后仍然封禁（永久 = 不自动解除）。
	banned, err := IsBannedAt(db, user.ID, now.AddDate(10, 0, 0))
	if err != nil {
		t.Fatalf("IsBannedAt: %v", err)
	}
	if !banned {
		t.Fatal("a permanent ban must still be in effect years later")
	}
}

// 判据：封禁必须带原因 —— 空/纯空白一律拒绝，且不落行。
func TestBanRequiresReason(t *testing.T) {
	db := newUserBanDurationTestDB(t)
	user := entity.User{Username: "no-reason", Status: "enabled", Role: "user"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	now := time.Now().UTC()
	expiresAt, err := ResolveUserBanExpiresAt(1, UserBanDurationUnitDay, now)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, reason := range []string{"", "   ", "\t\n"} {
		if _, banErr := Ban(db, UserBanRequest{
			UserID: user.ID, Reason: reason, ExpiresAt: expiresAt, OperatorUserID: 1, Now: now,
		}); banErr == nil {
			t.Fatalf("reason %q must be rejected", reason)
		}
	}
	var rows int64
	if err := db.Model(&entity.UserBan{}).Count(&rows).Error; err != nil {
		t.Fatalf("count ban rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("rejected bans must not persist rows, got %d", rows)
	}
}
