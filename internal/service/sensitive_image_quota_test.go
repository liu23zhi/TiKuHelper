package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 本文件只测**额度闸门本身**（配置读取、计数原语、自然日键、双闸语义）。
// fixture 构造器与检测编排的用例在 sensitive_image_detect_test.go 里，
// 同属 package service，共用同一套建库/种子函数。

// ===================== 额度与缓存的完整状态机（111 Q1-g 最终口径） =====================
//
// 铁律（111-*.md Q1-g，后轮覆盖 109 §2.2 与 110 §1）：
//
//	缓存命中 → 不查额度、不扣额度。**额度充足时也不扣。**
//
// 三条用例覆盖 111 号 §1.3 的例 1 / 例 2 / 例 3：
//   - 例 1：未命中 + 额度充足 → used +1（这是唯一会扣额的路径）
//   - 例 2：命中   + 额度充足 → used **不变**（下面第一条）
//   - 例 3：命中   + 额度耗尽 → 直接返回结论，used 不变（第二条）
//   - 另一条：未命中 + 额度耗尽 → sensitive_by_quota，不外呼（第三条）
//
// 「额度充足时命中也不扣」是最关键的一条：只用「额度耗尽时不扣」无法区分
// 「命中从不扣」与「命中尽量扣、扣不到就算了」——后一种实现在额度充足时
// 会照样扣，而那正是被 111 号推翻的旧口径。

// 111 例 2：缓存命中 + 额度充足 → used_count **保持不变**（不增加）。
//
// 原文：「④ 缓存命中 status='clean' → 直接返回；结果：通过检测；used_count 仍是 4（不增加）」
// 前置 3 → 例 1 之后 4 → 本用例命中后仍须是 4。
//
// 这条是区分「命中从不扣」与「命中尽量扣」的分水岭：只测「额度满时不扣」时，
// 「尽量扣」的实现在满额下恰好也扣不动，于是两条实现都绿 —— 测不出来。
func TestSensitiveImageQuotaCacheHitWithQuotaAvailableDoesNotConsume(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	data := sensitiveImageTestData("hit-with-quota-available")
	sha := HashSensitiveImage(data)
	seedSensitiveImageCache(t, db, sha, entity.SensitiveImageCacheStatusClean, "")
	// 前置 4（远未到 20 的上限）：额度**充足**，旧实现会在这里扣掉 1 次。
	seedSensitiveImageQuotaUsage(t, db, 7, date, 4)
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)
	// 命中路径「不查额度」—— 用 SQL 触达计数钉，而不是只看 used_count 有没有变
	// （详见 watchSensitiveImageQuotaTable 的注释：满额场景下「不扣」与「尝试扣」
	// 两种实现同形，只有触达计数能区分）。
	watched, quotaTouches := watchSensitiveImageQuotaTable(t, db)

	result, err := DetectSensitiveImageWithDB(context.Background(), watched, 7, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !result.CacheHit {
		t.Fatal("expected a cache hit")
	}
	if result.State != SensitiveImageDetectClean {
		t.Fatalf("expected state clean, got %q", result.State)
	}
	if result.UpstreamCalled || *calls != 0 {
		t.Fatalf("cache hit must not call upstream (calls=%d)", *calls)
	}
	// 核心断言：额度充足也一次都不扣。
	if result.Charged {
		t.Fatal("cache hit must NOT consume quota even when quota is available (111 §1.3 例 2)")
	}
	if got := quotaUsageOf(t, db, 7, date); got != 4 {
		t.Fatalf("used_count must stay 4 (111 例 2: 「仍是 4，不增加」), got %d", got)
	}
	// 111 §1.2 的「不查额度」：命中路径一次都不该碰额度表。
	if got := atomic.LoadInt32(quotaTouches); got != 0 {
		t.Fatalf("the cache-hit path must not touch the quota table at all (111 §1.2「不查额度」), got %d touches", got)
	}
}

// 111 例 3：缓存命中 + 额度已耗尽 → 结论照常返回，额度一次都不扣、也不查。
//
// 原文：「④ 缓存命中 status='clean' → 直接返回 'clean'，【根本不去查额度】」
//
// 断言必须同时钉三件事：
//   - State 仍然是缓存的**正确结论**（不能因额度满就降级）
//   - Charged == false（语义）
//   - used_count 仍等于上限（存储未被写）
func TestSensitiveImageQuotaCacheHitAtLimitDoesNotConsume(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	limit := DefaultSensitiveImageDailyUserLimit
	data := sensitiveImageTestData("hit-at-limit")
	sha := HashSensitiveImage(data)
	seedSensitiveImageCache(t, db, sha, entity.SensitiveImageCacheStatusSensitive, "上游判定为敏感图片")
	seedSensitiveImageQuotaUsage(t, db, 9, date, int32(limit))
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)
	// 满额场景：这个计数器是**唯一**能区分「不扣」与「尝试扣」的判据（见
	// watchSensitiveImageQuotaTable 注释：满额时「尝试扣」也扣不动，used_count 同形）。
	watched, quotaTouches := watchSensitiveImageQuotaTable(t, db)

	result, err := DetectSensitiveImageWithDB(context.Background(), watched, 9, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if result.State != SensitiveImageDetectSensitive {
		t.Fatalf("expected state sensitive (from cache), got %q", result.State)
	}
	if !result.CacheHit {
		t.Fatal("expected a cache hit")
	}
	if result.ScanState != entity.TicketAttachmentScanSensitive {
		t.Fatalf("expected scan state sensitive, got %q", result.ScanState)
	}
	if result.UpstreamCalled || *calls != 0 {
		t.Fatalf("cache hit must not call upstream (calls=%d)", *calls)
	}
	if result.Charged {
		t.Fatal("cache hit at limit must NOT consume quota")
	}
	if got := quotaUsageOf(t, db, 9, date); got != int32(limit) {
		t.Fatalf("used_count must stay at the limit (%d), got %d", limit, got)
	}
	// 命中路径**不查**额度表（111 §1.2「不扣额度、不查额度」），
	// 因此返回值里没有当日读数 —— UsedCount 必须是零值而不是「恰好等于上限」。
	// 若哪天这里变成 limit，说明有人又把它接回了额度表（那正是要防的回归）。
	if result.UsedCount != 0 {
		t.Fatalf("the cache-hit path must not read the quota table, got UsedCount %d", result.UsedCount)
	}
	// 满额场景的关键判据：一次都不该碰到额度表。
	if got := atomic.LoadInt32(quotaTouches); got != 0 {
		t.Fatalf("the cache-hit path must not touch the quota table at all (111 §1.2「不查额度」), got %d touches", got)
	}
}

// 三态之三：额度已耗尽 + 缓存未命中 → 标记 sensitive_by_quota、**不外呼**、工单可提交。
//
// 三条断言对应 107 Q1 ② 的拍板：「图片仍保存，但标记 status=sensitive_by_quota，
// 工单正常提交」。TicketSubmitAllowed 是这条语义的显式载体 —— 没有它，
// 调用方只能靠「State 不是 clean」反推，一旦新增状态就会静默改变行为。
func TestSensitiveImageQuotaExceededWithoutCacheFlagsByQuota(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	limit := DefaultSensitiveImageDailyUserLimit
	data := sensitiveImageTestData("miss-quota-exceeded")
	seedSensitiveImageQuotaUsage(t, db, 11, date, int32(limit))
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)

	result, err := DetectSensitiveImageWithDB(context.Background(), db, 11, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if result.State != SensitiveImageDetectByQuota {
		t.Fatalf("expected state %q, got %q", SensitiveImageDetectByQuota, result.State)
	}
	if result.CacheHit {
		t.Fatal("a brand-new image cannot be a cache hit")
	}
	if result.UpstreamCalled || *calls != 0 {
		t.Fatalf("quota exceeded must not call upstream (calls=%d)", *calls)
	}
	if !result.TicketSubmitAllowed {
		t.Fatal("quota exceeded must NOT block ticket submission")
	}
	if result.Charged {
		t.Fatal("quota exceeded must not charge anything")
	}
	// 按敏感处理：复用既有 fail-closed 展示状态（只有 approved 才显示）。
	if result.ScanState != entity.TicketAttachmentScanSensitive {
		t.Fatalf("expected scan state sensitive, got %q", result.ScanState)
	}
	if got := quotaUsageOf(t, db, 11, date); got != int32(limit) {
		t.Fatalf("used_count must stay at the limit (%d), got %d", limit, got)
	}
	// 绝不能写缓存：写进去会让这张图**永远**拿不到真实结论（下次会被当成缓存命中）。
	if rows := countSensitiveImageCacheRows(t, db); rows != 0 {
		t.Fatalf("quota-exceeded detections must not be cached, found %d rows", rows)
	}
}

// 三态之四：未超额 + 缓存未命中 → 扣 1 次且外呼一次，结果写进缓存。
func TestSensitiveImageQuotaMissUnderLimitCallsUpstreamOnce(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	data := sensitiveImageTestData("miss-under-limit")
	sha := HashSensitiveImage(data)
	seedSensitiveImageQuotaUsage(t, db, 13, date, 2)
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: true, Reason: "上游判定为敏感图片"}, nil)

	result, err := DetectSensitiveImageWithDB(context.Background(), db, 13, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("expected exactly 1 upstream call, got %d", *calls)
	}
	if !result.UpstreamCalled {
		t.Fatal("expected UpstreamCalled")
	}
	if !result.Charged {
		t.Fatal("a new detection under the limit must charge one unit")
	}
	if result.State != SensitiveImageDetectSensitive {
		t.Fatalf("expected state sensitive, got %q", result.State)
	}
	if got := quotaUsageOf(t, db, 13, date); got != 3 {
		t.Fatalf("expected used_count 2 -> 3, got %d", got)
	}
	entry, found, err := lookupSensitiveImageCacheWithDB(db, sha)
	if err != nil || !found {
		t.Fatalf("expected the result to be cached (found=%v err=%v)", found, err)
	}
	if entry.Status != entity.SensitiveImageCacheStatusSensitive {
		t.Fatalf("expected cached status sensitive, got %q", entry.Status)
	}
	if entry.Provider != SensitiveImageProviderUAPI {
		t.Fatalf("expected provider uapi, got %q", entry.Provider)
	}
}

// ===================== 每工单闸门 =====================

// 每工单 50 张：工单已满时不再外呼，标 sensitive_by_quota 且工单仍可提交。
func TestSensitiveImagePerTicketGateBlocksUpstreamButAllowsSubmission(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	ticketID := int64(42)
	limit := DefaultSensitiveImagePerTicketLimit
	// 预置 limit 张附件（表里只关心 ticket_id，内容无关）。
	for i := 0; i < limit; i++ {
		row := entity.TicketAttachment{
			TicketID: ticketID, UserID: 27, MediaType: "image/png", ByteSize: 10,
			SHA256: strings.Repeat("b", 64), ScanState: entity.TicketAttachmentScanPending,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed attachment %d: %v", i, err)
		}
	}
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)
	data := sensitiveImageTestData("per-ticket-full")

	result, err := DetectSensitiveImageWithDB(context.Background(), db, 27, ticketID, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if result.State != SensitiveImageDetectByQuota {
		t.Fatalf("expected state %q, got %q", SensitiveImageDetectByQuota, result.State)
	}
	if result.UpstreamCalled || *calls != 0 {
		t.Fatalf("a full ticket must not call upstream (calls=%d)", *calls)
	}
	if !result.TicketSubmitAllowed {
		t.Fatal("a full ticket must NOT block submission")
	}
	// 工单闸门在每日闸门之前判，所以额度**一次都不该被扣**（否则被拦下的请求白送额度）。
	if got := quotaUsageOf(t, db, 27, date); got != 0 {
		t.Fatalf("the ticket gate must run before the daily gate, used_count=%d", got)
	}
	if result.PerTicketLimit != limit {
		t.Fatalf("expected PerTicketLimit %d, got %d", limit, result.PerTicketLimit)
	}
	if result.TicketCount != int64(limit) {
		t.Fatalf("expected TicketCount %d, got %d", limit, result.TicketCount)
	}
}

// ===================== 额度计数原语 =====================

// ConsumeSensitiveImageDailyQuotaWithDB：并发扣减不会超发（条件 UPDATE 判胜负）。
func TestConsumeSensitiveImageDailyQuotaNeverOverIssues(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	date := "2026-11-13"
	const limit = 3
	const attempts = 10
	consumed := make(chan bool, attempts)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		go func() {
			<-start
			ok, _, err := ConsumeSensitiveImageDailyQuotaWithDB(db, 31, date, limit)
			if err != nil {
				t.Errorf("consume: %v", err)
				consumed <- false
				return
			}
			consumed <- ok
		}()
	}
	close(start)
	granted := 0
	for i := 0; i < attempts; i++ {
		if <-consumed {
			granted++
		}
	}
	if granted != limit {
		t.Fatalf("expected exactly %d grants out of %d attempts, got %d", limit, attempts, granted)
	}
	if got := quotaUsageOf(t, db, 31, date); got != limit {
		t.Fatalf("expected used_count %d, got %d", limit, got)
	}
}

// 不同用户 / 不同自然日互不影响。
func TestSensitiveImageQuotaIsPartitionedByUserAndDate(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	if _, _, err := ConsumeSensitiveImageDailyQuotaWithDB(db, 41, "2026-11-13", 20); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if _, _, err := ConsumeSensitiveImageDailyQuotaWithDB(db, 41, "2026-11-14", 20); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if _, _, err := ConsumeSensitiveImageDailyQuotaWithDB(db, 42, "2026-11-13", 20); err != nil {
		t.Fatalf("consume: %v", err)
	}
	for _, want := range []struct {
		user int32
		date string
	}{
		{41, "2026-11-13"}, {41, "2026-11-14"}, {42, "2026-11-13"},
	} {
		if got := quotaUsageOf(t, db, want.user, want.date); got != 1 {
			t.Fatalf("user %d on %s: expected 1, got %d", want.user, want.date, got)
		}
	}
	if got := quotaUsageOf(t, db, 43, "2026-11-13"); got != 0 {
		t.Fatalf("an untouched user must read 0, got %d", got)
	}
}

// ===================== 自然日键（Asia/Shanghai） =====================

// 日期键必须按 Asia/Shanghai 折算，不是 UTC。
//
// 判据取「上海 00:30」这个点：UTC 下它还是前一天 16:30。
// 用 UTC 会让额度在北京时间早上八点才翻篇 —— 用户看到额度在上班时间重置。
func TestSensitiveImageQuotaDateUsesShanghaiNaturalDay(t *testing.T) {
	shanghai := logger.ShanghaiLocation()
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{
			name: "上海凌晨 00:30 属于当天",
			at:   time.Date(2026, 11, 13, 0, 30, 0, 0, shanghai),
			want: "2026-11-13",
		},
		{
			name: "上海 23:30 仍属于当天（不提前翻篇）",
			at:   time.Date(2026, 11, 13, 23, 30, 0, 0, shanghai),
			want: "2026-11-13",
		},
		{
			name: "UTC 时刻折算成上海自然日",
			// 2026-11-12 16:30 UTC == 2026-11-13 00:30 上海。
			at:   time.Date(2026, 11, 12, 16, 30, 0, 0, time.UTC),
			want: "2026-11-13",
		},
		{
			name: "UTC 边界：2026-11-13 15:59 UTC 仍是上海 23:59",
			at:   time.Date(2026, 11, 13, 15, 59, 0, 0, time.UTC),
			want: "2026-11-13",
		},
		{
			name: "UTC 边界：2026-11-13 16:00 UTC 已是上海次日 00:00",
			at:   time.Date(2026, 11, 13, 16, 0, 0, 0, time.UTC),
			want: "2026-11-14",
		},
	}
	for _, tc := range cases {
		if got := SensitiveImageQuotaDate(tc.at); got != tc.want {
			t.Fatalf("%s: expected %q, got %q", tc.name, tc.want, got)
		}
	}
	// 用 UTC 公式会得到的值与正确值不同 —— 这条断言保证上面的用例真的在测时区，
	// 而不是恰好在两种算法下都成立。
	utcKey := time.Date(2026, 11, 12, 16, 30, 0, 0, time.UTC).Format(sensitiveImageQuotaDateLayout)
	if utcKey == "2026-11-13" {
		t.Fatal("the UTC-based key must differ from the Shanghai one for this fixture")
	}
}

// ===================== 配置读取与写入 =====================

// 缺省：默认开启、50/工单、20/人/日（109-*.md §2.1）。
func TestSensitiveImageQuotaPolicyDefaults(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	policy := LoadSensitiveImageQuotaPolicyWithDB(db)
	if !policy.Enabled {
		t.Fatal("expected the gate to be enabled by default")
	}
	if policy.PerTicketLimit != DefaultSensitiveImagePerTicketLimit {
		t.Fatalf("expected per-ticket limit %d, got %d", DefaultSensitiveImagePerTicketLimit, policy.PerTicketLimit)
	}
	if policy.DailyUserLimit != DefaultSensitiveImageDailyUserLimit {
		t.Fatalf("expected daily limit %d, got %d", DefaultSensitiveImageDailyUserLimit, policy.DailyUserLimit)
	}
	// nil 库也必须返回默认（而不是零值全关）—— 否则「数据库没起来」会静默变成「无额度限制」。
	nilPolicy := LoadSensitiveImageQuotaPolicyWithDB(nil)
	if !nilPolicy.Enabled || nilPolicy.DailyUserLimit != DefaultSensitiveImageDailyUserLimit {
		t.Fatalf("nil database must yield the defaults, got %+v", nilPolicy)
	}
}

// 管理员改配置后立刻生效；非法值被拒且不落库。
func TestSensitiveImageQuotaPolicyUpdate(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	enabled := false
	perTicket := 7
	daily := 3
	if err := SetSensitiveImageQuotaPolicyWithDB(db, &enabled, &perTicket, &daily); err != nil {
		t.Fatalf("update: %v", err)
	}
	policy := LoadSensitiveImageQuotaPolicyWithDB(db)
	if policy.Enabled {
		t.Fatal("expected the gate to be disabled after the update")
	}
	if policy.PerTicketLimit != 7 || policy.DailyUserLimit != 3 {
		t.Fatalf("expected 7/3, got %d/%d", policy.PerTicketLimit, policy.DailyUserLimit)
	}
	// 非法值：<= 0 必须报错，且**不改动**已有配置。
	bad := 0
	if err := SetSensitiveImageQuotaPolicyWithDB(db, nil, &bad, nil); !errors.Is(err, ErrSensitiveImageQuotaInvalidLimit) {
		t.Fatalf("expected ErrSensitiveImageQuotaInvalidLimit, got %v", err)
	}
	if got := LoadSensitiveImageQuotaPolicyWithDB(db).PerTicketLimit; got != 7 {
		t.Fatalf("an invalid update must not change the stored limit, got %d", got)
	}
	// nil 字段 = 不修改。
	enabledTrue := true
	if err := SetSensitiveImageQuotaPolicyWithDB(db, &enabledTrue, nil, nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	policy = LoadSensitiveImageQuotaPolicyWithDB(db)
	if !policy.Enabled {
		t.Fatal("expected the gate to be re-enabled")
	}
	if policy.PerTicketLimit != 7 || policy.DailyUserLimit != 3 {
		t.Fatalf("untouched fields must keep their values, got %d/%d", policy.PerTicketLimit, policy.DailyUserLimit)
	}
}

// 闸门关闭时：既不检查也不计数，结论照常返回。
func TestSensitiveImageQuotaDisabledBypassesBothGates(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	disabled := false
	if err := SetSensitiveImageQuotaPolicyWithDB(db, &disabled, nil, nil); err != nil {
		t.Fatalf("disable: %v", err)
	}
	// 当日额度已满，但闸门关着 —— 必须照常外呼且不计数。
	seedSensitiveImageQuotaUsage(t, db, 51, date, int32(DefaultSensitiveImageDailyUserLimit))
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)

	result, err := DetectSensitiveImageWithDB(context.Background(), db, 51, 0, sensitiveImageTestData("gate-disabled"), "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("expected one upstream call with the gate disabled, got %d", *calls)
	}
	if result.Charged {
		t.Fatal("a disabled gate must not charge")
	}
	if result.State != SensitiveImageDetectClean {
		t.Fatalf("expected state clean, got %q", result.State)
	}
	if got := quotaUsageOf(t, db, 51, date); got != int32(DefaultSensitiveImageDailyUserLimit) {
		t.Fatalf("used_count must not change while disabled, got %d", got)
	}
}

// 每日闸门可配置：把上限压到 1，第二次新图就被拦，且不扣。
func TestSensitiveImageDailyGateHonoursConfiguredLimit(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	limit := 1
	if err := SetSensitiveImageQuotaPolicyWithDB(db, nil, nil, &limit); err != nil {
		t.Fatalf("set limit: %v", err)
	}
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)

	first, err := DetectSensitiveImageWithDB(context.Background(), db, 53, 0, sensitiveImageTestData("daily-1"), "image/png", now)
	if err != nil {
		t.Fatalf("first detect: %v", err)
	}
	if !first.Charged || first.State != SensitiveImageDetectClean {
		t.Fatalf("expected the first image to be charged and clean, got %+v", first)
	}
	second, err := DetectSensitiveImageWithDB(context.Background(), db, 53, 0, sensitiveImageTestData("daily-2"), "image/png", now)
	if err != nil {
		t.Fatalf("second detect: %v", err)
	}
	if second.State != SensitiveImageDetectByQuota {
		t.Fatalf("expected the second image to be flagged by quota, got %q", second.State)
	}
	if *calls != 1 {
		t.Fatalf("expected only the first image to reach upstream, got %d calls", *calls)
	}
	if got := quotaUsageOf(t, db, 53, date); got != 1 {
		t.Fatalf("expected used_count 1, got %d", got)
	}
	// 同一张已缓存的图仍可重复上传（111-*.md §1.4 明确接受的后果）。
	third, err := DetectSensitiveImageWithDB(context.Background(), db, 53, 0, sensitiveImageTestData("daily-1"), "image/png", now)
	if err != nil {
		t.Fatalf("third detect: %v", err)
	}
	if third.State != SensitiveImageDetectClean || !third.CacheHit {
		t.Fatalf("an already-cached image must pass at the quota limit, got %+v", third)
	}
	if third.Charged {
		t.Fatal("a cache hit at the quota limit must not charge")
	}
	if got := quotaUsageOf(t, db, 53, date); got != 1 {
		t.Fatalf("used_count must stay 1, got %d", got)
	}
}

// ReserveSensitiveImageQuotaWithDB 的 charge=false 分支：只判定、不扣减。
func TestReserveSensitiveImageQuotaWithoutChargeOnlyInspects(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	date := "2026-11-13"
	policy := SensitiveImageQuotaPolicy{Enabled: true, PerTicketLimit: 50, DailyUserLimit: 2}

	seedSensitiveImageQuotaUsage(t, db, 61, date, 1)
	allowed, err := ReserveSensitiveImageQuotaWithDB(db, policy, 61, 0, date, false)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if !allowed.Allowed || allowed.Charged {
		t.Fatalf("expected allowed without charge, got %+v", allowed)
	}
	if got := quotaUsageOf(t, db, 61, date); got != 1 {
		t.Fatalf("charge=false must not touch used_count, got %d", got)
	}

	// 已满 → 判定为不允许，但仍然不扣（used_count 已经等于上限）。
	if err := db.Model(&entity.SensitiveImageDailyQuota{}).
		Where(map[string]any{"user_id": 61, "quota_date": date}).
		Update("used_count", 2).Error; err != nil {
		t.Fatalf("raise usage to the limit: %v", err)
	}
	full, err := ReserveSensitiveImageQuotaWithDB(db, policy, 61, 0, date, false)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if full.Allowed {
		t.Fatal("expected the daily gate to refuse at the limit")
	}
	if full.Charged {
		t.Fatal("charge=false must never report a charge")
	}
}

// 默认值本身就是安全边界：**完全不配置任何 system_settings** 时，
// 额度闸门必须默认开启，并且真的在默认上限（20）处拦住第 21 张新图。
//
// 为什么必须单独有这条（而不是只要能读到 policy 就够了）：
// 上面每一条端到端用例都依赖默认 policy，但它们的用户都被预置了 used_count，
// 于是「闸门是否默认开启」这个前提从未被**行为层**验证过。
// 实测：把 `Enabled: true` 翻成 `false` 做变异时，只有 TestSensitiveImageQuotaPolicyDefaults
// 这一条（直接断言字段值的配置用例）变红，26 条端到端用例全绿 ——
// 也就是说「默认开启」在行为层是**零断言**的，只是注释。
// 这条用例钉的正是那个缺口：空配置 → 前 20 张真的外呼、第 21 张真的被拦。
func TestSensitiveImageQuotaDefaultsToEnabledGateInBehaviour(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	// 刻意**不写**任何 system_settings：全部走默认值。
	limit := DefaultSensitiveImageDailyUserLimit
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)

	// 先跑满默认上限：每张都是新图（不同 SHA256），因此每张都该扣 1 次。
	for i := 0; i < limit; i++ {
		result, err := DetectSensitiveImageWithDB(context.Background(), db, 71, 0,
			sensitiveImageTestData(fmt.Sprintf("default-gate-%d", i)), "image/png", now)
		if err != nil {
			t.Fatalf("detect #%d: %v", i, err)
		}
		if result.State != SensitiveImageDetectClean {
			t.Fatalf("detect #%d: expected clean under the limit, got %q", i, result.State)
		}
		if !result.Charged {
			t.Fatalf("detect #%d: a default-enabled gate must charge each new image", i)
		}
	}
	if got := quotaUsageOf(t, db, 71, date); got != int32(limit) {
		t.Fatalf("expected used_count to reach the default limit %d, got %d", limit, got)
	}
	if *calls != limit {
		t.Fatalf("expected %d upstream calls so far, got %d", limit, *calls)
	}

	// 第 21 张（新图）：默认闸门必须拦住，不外呼。
	beyond, err := DetectSensitiveImageWithDB(context.Background(), db, 71, 0,
		sensitiveImageTestData("default-gate-beyond"), "image/png", now)
	if err != nil {
		t.Fatalf("detect beyond limit: %v", err)
	}
	if beyond.State != SensitiveImageDetectByQuota {
		t.Fatalf("the DEFAULT-ENABLED gate must stop the 21st new image; got state %q "+
			"(若这里是 clean，说明额度闸门默认关闭了 —— 安全边界被翻转)", beyond.State)
	}
	if *calls != limit {
		t.Fatalf("the 21st image must not reach upstream: expected %d calls, got %d", limit, *calls)
	}
	if !beyond.TicketSubmitAllowed {
		t.Fatal("quota exceeded must still allow ticket submission")
	}
}

// 额度表读写失败必须让调用方拿到 error（不能静默当成「还有余量」）。
func TestSensitiveImageQuotaUsageReportsDatabaseErrors(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	// 把表删掉，制造一次真实的查询失败。
	if err := db.Migrator().DropTable(&entity.SensitiveImageDailyQuota{}); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := SensitiveImageQuotaUsageWithDB(db, 1, "2026-11-13"); err == nil {
		t.Fatal("expected a missing table to be reported as an error")
	}
	if _, _, err := ConsumeSensitiveImageDailyQuotaWithDB(db, 1, "2026-11-13", 20); err == nil {
		t.Fatal("expected a missing table to be reported as an error")
	}
	var _ *gorm.DB = db
}
