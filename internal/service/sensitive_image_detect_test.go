package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ===================== 共享测试脚手架 =====================
//
// 本文件同时承载 sensitive_image_quota_test.go 复用的 fixture 构造器：
// 两个测试文件同属 package service，共用一套库与种子数据，
// 避免两处各自建库导致「同一张图在 A 用例里是 clean、在 B 用例里是 sensitive」的漂移。

// newSensitiveImageTestDB 建一个带 users / ticket_attachments / 两张新表的**内存库**。
//
// DSN 走 util.SQLiteConcurrentDSN（不手写并发参数）：与生产
// internal/registry.sqliteProductionDSN 共用同一份定义，因此「生产改了测试没跟着改」
// 这种漂移在结构上不可能发生。用 Concurrent 而不是 Production，是因为内存库
// 不支持 WAL（Production 会对文件库加 journal_mode=WAL）。
//
// 必须带 _txlock=immediate + busy_timeout：少了它们，并发用例的落败方会全部被
// "database table is locked" 挡下 —— 断言照样是绿的，但覆盖的是锁行为而不是业务语义。
//
// 库名带 t.Name()：`file:x?mode=memory&cache=shared` 在同进程内同名库是共享的，
// 跨用例会互相污染。
func newSensitiveImageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	dsn := util.SQLiteConcurrentDSN("file:sensitive-image-" + name + "?mode=memory&cache=shared")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// ⚠️ 每个模型**单独一次** AutoMigrate。
	// 一次传多个模型时 GORM 会合并建表语句，实测会导致物理表列缺失
	// （表建出来了，但缺列 —— 排查时只会看到 "no such column"）。
	for _, model := range []any{
		&entity.SystemSetting{},
		&entity.User{},
		&entity.TicketAttachment{},
		&entity.SensitiveImageCacheEntry{},
		&entity.SensitiveImageDailyQuota{},
	} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatalf("migrate test db (%T): %v", model, err)
		}
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	// 与生产同款：单连接串行化，写事务排队而不是互相撞锁。
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// sensitiveImageTestNow 是固定的「今天」（上海时区 12:00，远离零点边界）。
func sensitiveImageTestNow() time.Time {
	return time.Date(2026, 11, 13, 12, 0, 0, 0, logger.ShanghaiLocation())
}

// sensitiveImageTestData 返回一段可用的「图片字节」。
//
// 检测编排只对字节做 sha256，不做格式校验（格式校验在 safeTicketAttachmentImage
// 里，属于上传路径）。因此这里用纯字节即可，不必造一张真 PNG。
func sensitiveImageTestData(marker string) []byte {
	return []byte("sensitive-image-fixture:" + marker)
}

// stubSensitiveImageDetector 装一个计数用的假检测通道，并在用例结束时还原。
//
// 生产代码里 sensitiveImageDetectorOverride 恒为 nil，因此这条替换路径只在测试里成立。
// 必须在 t.Cleanup 里还原：它是包级变量，残留会污染同包其它用例（尤其计数断言）。
func stubSensitiveImageDetector(t *testing.T, verdict SensitiveImageVerdict, err error) *int {
	t.Helper()
	calls := 0
	previous := sensitiveImageDetectorOverride
	sensitiveImageDetectorOverride = func(_ context.Context, _ []byte, _ string) (SensitiveImageVerdict, error) {
		calls++
		return verdict, err
	}
	t.Cleanup(func() { sensitiveImageDetectorOverride = previous })
	return &calls
}

// seedSensitiveImageQuotaUsage 把某用户某自然日的已用次数预置成 used。
func seedSensitiveImageQuotaUsage(t *testing.T, db *gorm.DB, userID int32, date string, used int32) {
	t.Helper()
	row := entity.SensitiveImageDailyQuota{UserID: userID, QuotaDate: date, UsedCount: used, UpdatedAt: sensitiveImageTestNow()}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed quota usage: %v", err)
	}
}

// seedSensitiveImageCache 预置一行缓存（模拟「这张图以前已经检测过」）。
func seedSensitiveImageCache(t *testing.T, db *gorm.DB, sha, status, reason string) entity.SensitiveImageCacheEntry {
	t.Helper()
	row := entity.SensitiveImageCacheEntry{
		ImageSHA256: sha,
		ContentType: "image/png",
		FileSize:    128,
		Status:      status,
		Reason:      reason,
		Provider:    SensitiveImageProviderUAPI,
		UpdatedAt:   sensitiveImageTestNow(),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	return row
}

// quotaUsageOf 读回当日已用次数（直接查库，不经过被测函数）。
func quotaUsageOf(t *testing.T, db *gorm.DB, userID int32, date string) int32 {
	t.Helper()
	var row entity.SensitiveImageDailyQuota
	err := db.Where(map[string]any{"user_id": userID, "quota_date": date}).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0
	}
	if err != nil {
		t.Fatalf("read quota usage: %v", err)
	}
	return row.UsedCount
}

// countSensitiveImageCacheRows 数当前缓存表行数（断言「超额时绝不写缓存」）。
func countSensitiveImageCacheRows(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var rows int64
	if err := db.Model(&entity.SensitiveImageCacheEntry{}).Count(&rows).Error; err != nil {
		t.Fatalf("count cache rows: %v", err)
	}
	return rows
}

// watchSensitiveImageQuotaTable 统计此后对**额度表**的 SQL 触达次数（读 + 写）。
//
// 为什么需要这个计数器：111 §1.2 要求缓存命中时「不扣额度、**不查额度**」。
// 只用「used_count 有没有变化」无法证明「没查」—— 因为「命中时尝试扣一次」的
// 实现会在满额时扣不动（条件 UPDATE 的 used_count < limit 不成立，RowsAffected=0），
// 于是 used_count 同样不变、Charged 同样 false。两种实现在满额场景下完全同形，
// 测不出来（这正是本项目记录过的「阈值区间双重冗余」形态）。
//
// 判据必须是**有没有碰过那张表**：正确实现 0 次，任何「尝试扣」的实现 ≥1 次。
//
// 实现方式：给这条用例**专用的一个 gorm 句柄**装一个日志器，从 SQL 文本里数
// 额度表被提及的次数。之所以不用 GORM 的 Query/Update 回调：实测 callback 注册
// 在 *gorm.DB 上会**跨用例泄漏**（Remove 只对注册时那个句柄生效，链式 clone 出的
// 子句柄共享同一份 callbacks），表现为「单独跑 0 次、连着跑 1 次」的幽灵触达。
// 日志器跟着句柄走、不注册全局回调，因此不存在这个问题。
//
// 注意：调用方必须把这个句柄传进被测函数（见下面两条命中用例）。
func watchSensitiveImageQuotaTable(t *testing.T, db *gorm.DB) (*gorm.DB, *int32) {
	t.Helper()
	touches := new(int32)
	watched := db.Session(&gorm.Session{Logger: quotaTouchLogger{touches: touches, inner: gormlogger.Default.LogMode(gormlogger.Silent)}})
	return watched, touches
}

// quotaTouchLogger 在每条 SQL 执行时检查是否提及额度表，只做计数。
type quotaTouchLogger struct {
	touches *int32
	inner   gormlogger.Interface
}

func (l quotaTouchLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return quotaTouchLogger{touches: l.touches, inner: l.inner.LogMode(level)}
}

func (l quotaTouchLogger) Info(ctx context.Context, msg string, data ...any) {
	if strings.Contains(msg, entity.TableNameSensitiveImageQuota) {
		atomic.AddInt32(l.touches, 1)
	}
}

func (l quotaTouchLogger) Warn(ctx context.Context, msg string, data ...any) {
	if strings.Contains(msg, entity.TableNameSensitiveImageQuota) {
		atomic.AddInt32(l.touches, 1)
	}
}

func (l quotaTouchLogger) Error(ctx context.Context, msg string, data ...any) {
	if strings.Contains(msg, entity.TableNameSensitiveImageQuota) {
		atomic.AddInt32(l.touches, 1)
	}
}

// Trace 是真正被 GORM 调用的入口（错误/查询都走这里）。
func (l quotaTouchLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	if strings.Contains(sql, entity.TableNameSensitiveImageQuota) {
		atomic.AddInt32(l.touches, 1)
	}
}

// ===================== 编排：缓存与上游 =====================

// 缓存命中的结论必须原样返回（含 Reason 与 ScanState），且零外呼。
//
// 这条钉的是「结论来自缓存」这件事本身：如果实现改成「命中后仍然外呼复核」，
// 计费会翻倍，而且缓存的意义就没了。
func TestSensitiveImageDetectCacheHitReturnsCachedVerdictWithoutUpstream(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	data := sensitiveImageTestData("cache-hit-verdict")
	sha := HashSensitiveImage(data)
	seedSensitiveImageCache(t, db, sha, entity.SensitiveImageCacheStatusSensitive, "上游判定为敏感图片")
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)

	result, err := DetectSensitiveImageWithDB(context.Background(), db, 71, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if *calls != 0 || result.UpstreamCalled {
		t.Fatalf("a cache hit must not reach upstream (calls=%d)", *calls)
	}
	if !result.CacheHit {
		t.Fatal("expected CacheHit")
	}
	// 结论必须取自缓存，不是「新的判定」。
	if result.State != SensitiveImageDetectSensitive {
		t.Fatalf("expected state sensitive, got %q", result.State)
	}
	if result.Reason != "上游判定为敏感图片" {
		t.Fatalf("expected the cached reason to be returned, got %q", result.Reason)
	}
	if result.ScanState != entity.TicketAttachmentScanSensitive {
		t.Fatalf("expected scan state sensitive, got %q", result.ScanState)
	}
	// 命中计数只增（统计用，不参与额度）。
	entry, found, err := lookupSensitiveImageCacheWithDB(db, sha)
	if err != nil || !found {
		t.Fatalf("expected the entry (found=%v err=%v)", found, err)
	}
	if entry.HitCount != 1 {
		t.Fatalf("expected hit_count 1 after one hit, got %d", entry.HitCount)
	}
	// 命中的图仍然可以再次命中（命中计数继续累加，但结论不变）。
	if _, err := DetectSensitiveImageWithDB(context.Background(), db, 71, 0, data, "image/png", now); err != nil {
		t.Fatalf("second detect: %v", err)
	}
	entry, _, _ = lookupSensitiveImageCacheWithDB(db, sha)
	if entry.HitCount != 2 {
		t.Fatalf("expected hit_count 2 after two hits, got %d", entry.HitCount)
	}
}

// 同一张图被不同用户、不同工单上传：只外呼一次（检测结果跟随 SHA256）。
//
// 这是 117-*.md N1-b「同一 SHA256 只检测一次，结论永久复用」的直接判据。
func TestSensitiveImageDetectSameImageAcrossUsersCallsUpstreamOnce(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	data := sensitiveImageTestData("same-image-two-users")
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)

	for _, userID := range []int32{72, 73} {
		if _, err := DetectSensitiveImageWithDB(context.Background(), db, userID, 0, data, "image/png", now); err != nil {
			t.Fatalf("detect for user %d: %v", userID, err)
		}
	}
	if *calls != 1 {
		t.Fatalf("the same sha256 must reach upstream exactly once across users, got %d", *calls)
	}
	// 只有一个缓存行（主键就是去重键）。
	if rows := countSensitiveImageCacheRows(t, db); rows != 1 {
		t.Fatalf("expected exactly 1 cache row, got %d", rows)
	}
}

// 没配置检测通道：如实返回不可用，且**不留 pending**（否则这张图会卡到超时）。
func TestSensitiveImageDetectWithoutDetectorDoesNotLeavePending(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	data := sensitiveImageTestData("no-detector")
	sha := HashSensitiveImage(data)
	// 生产路径：没有 uapi- 前缀的密钥 ⇒ sensitiveImageDetectorWithDB 返回 nil。
	// 这里不注入 override，并显式把 override 清空（防止同包其它用例残留）。
	previous := sensitiveImageDetectorOverride
	sensitiveImageDetectorOverride = nil
	t.Cleanup(func() { sensitiveImageDetectorOverride = previous })

	result, err := DetectSensitiveImageWithDB(context.Background(), db, 74, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if result.UpstreamCalled {
		t.Fatal("no detector configured means no upstream call")
	}
	if result.State != SensitiveImageDetectPending {
		t.Fatalf("expected state pending, got %q", result.State)
	}
	if result.ScanState != entity.TicketAttachmentScanFailed {
		t.Fatalf("expected scan state failed, got %q", result.ScanState)
	}
	// 关键：占位必须被标成 error（非终态），不能留 pending。
	entry, found, err := lookupSensitiveImageCacheWithDB(db, sha)
	if err != nil || !found {
		t.Fatalf("expected the error row (found=%v err=%v)", found, err)
	}
	if entry.Status != entity.SensitiveImageCacheStatusError {
		t.Fatalf("a missing detector must leave status error (retryable), got %q", entry.Status)
	}
	// 下一次（配置好了）能直接重试，不必等占位超时。
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)
	retry, err := DetectSensitiveImageWithDB(context.Background(), db, 74, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if *calls != 1 || retry.State != SensitiveImageDetectClean {
		t.Fatalf("expected a successful retry, got calls=%d state=%q", *calls, retry.State)
	}
}

// ===================== 编排：并发去重 =====================

// 并发两个相同 SHA256 → 上游只调用一次。
//
// 两条路径都要覆盖（只有一条的话会漏掉真实缺陷）：
//
//  1. **真并发**：两个 goroutine 同时检测同一张新图。SQLite 单连接下它们会排队，
//     落败方读到 leader 写好的终态 → 缓存命中，不再外呼。
//  2. **占位冲突**：预先插一行新鲜的 pending（模拟「别人正在查」），
//     本次请求必须返回 pending 且零外呼（见下一个用例）。这条是唯一能确定性
//     覆盖「占位挡住第二个调用者」的方式 —— 真并发在 _txlock=immediate 下会被
//     串行化，落败方排队到前一次提交之后才读，未必撞得到插入冲突。
func TestSensitiveImageDetectConcurrentSameHashCallsUpstreamOnce(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	data := sensitiveImageTestData("concurrent-same-hash")
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)

	const workers = 2
	type outcome struct {
		result SensitiveImageDetectResult
		err    error
	}
	results := make(chan outcome, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func(userID int32) {
			<-start
			result, err := DetectSensitiveImageWithDB(context.Background(), db, userID, 0, data, "image/png", now)
			results <- outcome{result: result, err: err}
		}(int32(80 + i))
	}
	close(start)
	for i := 0; i < workers; i++ {
		got := <-results
		if got.err != nil {
			t.Fatalf("worker %d: %v", i, got.err)
		}
	}
	if *calls != 1 {
		t.Fatalf("two concurrent detections of the same sha256 must call upstream exactly once, got %d", *calls)
	}
	if rows := countSensitiveImageCacheRows(t, db); rows != 1 {
		t.Fatalf("expected exactly 1 cache row, got %d", rows)
	}
}

// 占位冲突：别人正在查 → 本次返回 pending、零外呼、不扣额度、不覆盖占位。
func TestSensitiveImageDetectPendingPlaceholderBlocksSecondCaller(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	data := sensitiveImageTestData("pending-placeholder")
	sha := HashSensitiveImage(data)
	// 一行「刚刚开始检测」的占位（未超时）。
	seedSensitiveImageCache(t, db, sha, entity.SensitiveImageCacheStatusPending, "")
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)

	result, err := DetectSensitiveImageWithDB(context.Background(), db, 81, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if result.UpstreamCalled || *calls != 0 {
		t.Fatalf("a fresh pending placeholder must block the upstream call (calls=%d)", *calls)
	}
	if result.State != SensitiveImageDetectPending {
		t.Fatalf("expected state %q, got %q", SensitiveImageDetectPending, result.State)
	}
	if result.Charged {
		t.Fatal("a blocked caller must not be charged")
	}
	if got := quotaUsageOf(t, db, 81, date); got != 0 {
		t.Fatalf("expected no quota usage, got %d", got)
	}
	// 占位不能被本次调用改掉（否则 leader 回来写结果时会覆盖到一个已变的行）。
	entry, found, err := lookupSensitiveImageCacheWithDB(db, sha)
	if err != nil || !found {
		t.Fatalf("expected the placeholder to remain (found=%v err=%v)", found, err)
	}
	if entry.Status != entity.SensitiveImageCacheStatusPending {
		t.Fatalf("the placeholder must stay pending, got %q", entry.Status)
	}
}

// 占位超时（> 5 分钟）→ 允许抢占重试，且**不重复扣额度**（同一张图只算一次）。
func TestSensitiveImageDetectStalePendingIsTakenOverWithoutRecharging(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	data := sensitiveImageTestData("stale-pending")
	sha := HashSensitiveImage(data)
	row := entity.SensitiveImageCacheEntry{
		ImageSHA256: sha, ContentType: "image/png", Status: entity.SensitiveImageCacheStatusPending,
		UpdatedAt: now.Add(-sensitiveImagePendingTimeout - time.Minute),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed stale placeholder: %v", err)
	}
	// 这张图已经被扣过一次（leader 当时扣的）。
	seedSensitiveImageQuotaUsage(t, db, 82, date, 1)
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)

	result, err := DetectSensitiveImageWithDB(context.Background(), db, 82, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("a stale placeholder must be taken over (expected 1 upstream call, got %d)", *calls)
	}
	if result.Charged {
		t.Fatal("taking over a stale placeholder must not charge quota again")
	}
	if got := quotaUsageOf(t, db, 82, date); got != 1 {
		t.Fatalf("used_count must stay 1, got %d", got)
	}
	entry, found, err := lookupSensitiveImageCacheWithDB(db, sha)
	if err != nil || !found {
		t.Fatalf("expected the entry (found=%v err=%v)", found, err)
	}
	if entry.Status != entity.SensitiveImageCacheStatusClean {
		t.Fatalf("expected the stale placeholder to be replaced by clean, got %q", entry.Status)
	}
}

// 上游失败：缓存标 error（非终态），下次可重试且不重复扣额度。
func TestSensitiveImageDetectUpstreamFailureIsRetryable(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	data := sensitiveImageTestData("upstream-failure")
	sha := HashSensitiveImage(data)
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{}, errors.New("上游 502"))

	first, err := DetectSensitiveImageWithDB(context.Background(), db, 83, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !first.UpstreamCalled || *calls != 1 {
		t.Fatalf("expected one upstream attempt, got %d", *calls)
	}
	if first.State != SensitiveImageDetectPending {
		t.Fatalf("a failed call must not be reported as a verdict, got %q", first.State)
	}
	entry, found, err := lookupSensitiveImageCacheWithDB(db, sha)
	if err != nil || !found {
		t.Fatalf("expected the error row (found=%v err=%v)", found, err)
	}
	if entry.Status != entity.SensitiveImageCacheStatusError {
		t.Fatalf("expected cached status error, got %q", entry.Status)
	}
	// 重试：error 是**非终态**，必须允许抢占重试，并且不再扣额度。
	retry, err := DetectSensitiveImageWithDB(context.Background(), db, 83, 0, data, "image/png", now)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if *calls != 2 {
		t.Fatalf("expected a retry to reach upstream (calls=%d)", *calls)
	}
	if retry.Charged {
		t.Fatal("retrying an errored entry must not charge quota again")
	}
}

// ===================== 编排：参数校验 =====================

// 缺少用户 / 空字节：明确报错，不写任何缓存与额度。
func TestSensitiveImageDetectRejectsInvalidInput(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	// 没有用户就没有额度主体。
	if _, err := DetectSensitiveImageWithDB(context.Background(), db, 0, 0, sensitiveImageTestData("no-user"), "image/png", now); err == nil {
		t.Fatal("expected an error for a missing user")
	}
	// 空字节无法计算 SHA256。
	if _, err := DetectSensitiveImageWithDB(context.Background(), db, 91, 1, nil, "image/png", now); !errors.Is(err, ErrTicketAttachmentInvalid) {
		t.Fatalf("expected ErrTicketAttachmentInvalid for empty data, got %v", err)
	}
	// nil 库。
	if _, err := DetectSensitiveImageWithDB(context.Background(), db, 91, 0, []byte("x"), "image/png", now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := DetectSensitiveImageWithDB(context.Background(), nil, 91, 0, []byte("x"), "image/png", now); err == nil {
		t.Fatal("expected an error for a nil database")
	}
}

// ===================== 管理页：手动触发与状态同步 =====================

// 手动触发绕过额度闸门，并把 provider 记成 manual（允许覆盖已有结论）。
func TestManualDetectSensitiveImageBypassesQuotaAndOverridesVerdict(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	date := SensitiveImageQuotaDate(now)
	data := sensitiveImageTestData("manual-override")
	sha := HashSensitiveImage(data)
	// 这张图此前被判为敏感，且该用户当天额度已满 —— 手动触发必须照样能跑。
	seedSensitiveImageCache(t, db, sha, entity.SensitiveImageCacheStatusSensitive, "上游判定为敏感图片")
	seedSensitiveImageQuotaUsage(t, db, 95, date, int32(DefaultSensitiveImageDailyUserLimit))
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false, Reason: ""}, nil)

	result, err := ManualDetectSensitiveImageWithDB(context.Background(), db, data, "image/png", now)
	if err != nil {
		t.Fatalf("manual detect: %v", err)
	}
	if *calls != 1 || !result.UpstreamCalled {
		t.Fatalf("manual detection must call upstream regardless of quota (calls=%d)", *calls)
	}
	if result.State != SensitiveImageDetectClean {
		t.Fatalf("expected the verdict to be overridden to clean, got %q", result.State)
	}
	// 额度一次都不该动（管理员复核不是用户上传）。
	if got := quotaUsageOf(t, db, 95, date); got != int32(DefaultSensitiveImageDailyUserLimit) {
		t.Fatalf("manual detection must not touch quota, got %d", got)
	}
	entry, found, err := lookupSensitiveImageCacheWithDB(db, sha)
	if err != nil || !found {
		t.Fatalf("expected the entry (found=%v err=%v)", found, err)
	}
	if entry.Status != entity.SensitiveImageCacheStatusClean {
		t.Fatalf("expected cached status clean, got %q", entry.Status)
	}
	if entry.Provider != SensitiveImageProviderManual {
		t.Fatalf("expected provider manual, got %q", entry.Provider)
	}
}

// 手动触发失败必须把错误**如实返回管理员**（与用户路径的静默降级相反）。
func TestManualDetectSensitiveImageSurfacesUpstreamErrors(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	data := sensitiveImageTestData("manual-failure")
	// 先让他有一次成功记录，确认失败不会把已有结论改成成功。
	seedSensitiveImageCache(t, db, HashSensitiveImage(data), entity.SensitiveImageCacheStatusSensitive, "旧结论")
	stubSensitiveImageDetector(t, SensitiveImageVerdict{}, errors.New("上游 500"))

	if _, err := ManualDetectSensitiveImageWithDB(context.Background(), db, data, "image/png", now); err == nil {
		t.Fatal("expected the upstream error to be returned to the administrator")
	}
	// 旧结论不能被一次失败抹掉（失败只写 error，不改 status 到 clean）。
	entry, found, err := lookupSensitiveImageCacheWithDB(db, HashSensitiveImage(data))
	if err != nil || !found {
		t.Fatalf("expected the entry (found=%v err=%v)", found, err)
	}
	if entry.Status == entity.SensitiveImageCacheStatusClean {
		t.Fatal("a failed manual detection must never be recorded as clean")
	}
}

// 按 SHA256 取不到原始字节时必须报 ErrSensitiveImageBytesUnavailable，
// 而不是假装重检成功（缓存表不存字节，字节只在 ticket_attachments 里）。
func TestLoadSensitiveImageBytesReportsUnavailableWithoutAttachment(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	missing := strings.Repeat("c", 64)
	if _, _, err := LoadSensitiveImageBytesWithDB(db, missing); !errors.Is(err, ErrSensitiveImageBytesUnavailable) {
		t.Fatalf("expected ErrSensitiveImageBytesUnavailable, got %v", err)
	}
	if _, _, err := LoadSensitiveImageBytesWithDB(nil, missing); err == nil {
		t.Fatal("expected an error for a nil database")
	}
}

// SyncSensitiveImageScanState 必须把所有引用同一 SHA256 的附件行一起改掉。
//
// 「检测跟 SHA256 走」的直接推论：缓存一行结论，附件可能有多行。
// 只改缓存不改附件，表现就是「管理页说已重判为 clean，用户看到的图还是隐藏」。
func TestSyncSensitiveImageScanStateUpdatesEveryReferencingAttachment(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	sha := strings.Repeat("d", 64)
	other := strings.Repeat("e", 64)
	for _, row := range []entity.TicketAttachment{
		{TicketID: 1, UserID: 1, MediaType: "image/png", ByteSize: 10, SHA256: sha, ScanState: entity.TicketAttachmentScanSensitive},
		{TicketID: 2, UserID: 2, MediaType: "image/png", ByteSize: 10, SHA256: sha, ScanState: entity.TicketAttachmentScanSensitive},
		{TicketID: 3, UserID: 3, MediaType: "image/png", ByteSize: 10, SHA256: other, ScanState: entity.TicketAttachmentScanSensitive},
	} {
		row := row
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed attachment: %v", err)
		}
	}
	previous := SensitiveImageQuotaNow
	SensitiveImageQuotaNow = func() time.Time { return now }
	t.Cleanup(func() { SensitiveImageQuotaNow = previous })

	updated, err := SyncSensitiveImageScanStateWithDB(db, sha, string(SensitiveImageDetectClean))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if updated != 2 {
		t.Fatalf("expected exactly the 2 attachments sharing the sha256 to be updated, got %d", updated)
	}
	// 同 SHA256 的两行变 approved；别的图不受影响。
	var approved int64
	if err := db.Model(&entity.TicketAttachment{}).
		Where(map[string]any{"sha256": sha, "scan_state": entity.TicketAttachmentScanApproved}).
		Count(&approved).Error; err != nil {
		t.Fatalf("count approved: %v", err)
	}
	if approved != 2 {
		t.Fatalf("expected 2 approved rows, got %d", approved)
	}
	var untouched int64
	if err := db.Model(&entity.TicketAttachment{}).
		Where(map[string]any{"sha256": other, "scan_state": entity.TicketAttachmentScanSensitive}).
		Count(&untouched).Error; err != nil {
		t.Fatalf("count untouched: %v", err)
	}
	if untouched != 1 {
		t.Fatalf("a different sha256 must be untouched, got %d", untouched)
	}
	// 同步到敏感方向的映射也要对。
	if _, err := SyncSensitiveImageScanStateWithDB(db, sha, string(SensitiveImageDetectSensitive)); err != nil {
		t.Fatalf("sync: %v", err)
	}
	var backToSensitive int64
	if err := db.Model(&entity.TicketAttachment{}).
		Where(map[string]any{"sha256": sha, "scan_state": entity.TicketAttachmentScanSensitive}).
		Count(&backToSensitive).Error; err != nil {
		t.Fatalf("count sensitive: %v", err)
	}
	if backToSensitive != 2 {
		t.Fatalf("expected 2 sensitive rows after re-sync, got %d", backToSensitive)
	}
}

// 票据级批量触发：同一张图在一个工单里出现多次时只外呼一次。
//
// 密钥用 t.Setenv 自备（32 字节 base64）：跳过它会让这条「去重真的生效」的断言
// 永远不执行 —— 而 skip 在 CI 看来与通过完全一样（这是本项目反复踩过的假绿形态）。
func TestManualDetectTicketDeduplicatesIdenticalImages(t *testing.T) {
	db := newSensitiveImageTestDB(t)
	now := sensitiveImageTestNow()
	t.Setenv("TIKU_TICKET_ATTACHMENT_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	aead, err := ticketAttachmentCipher()
	if err != nil {
		t.Fatalf("ticket attachment cipher: %v", err)
	}
	previous := SensitiveImageQuotaNow
	SensitiveImageQuotaNow = func() time.Time { return now }
	t.Cleanup(func() { SensitiveImageQuotaNow = previous })

	// 同一张图的明文出现两次（同一个 sha256），外加一张不同的图。
	sameData := sensitiveImageTestData("ticket-dedup")
	sameSHA := HashSensitiveImage(sameData)
	otherData := sensitiveImageTestData("ticket-dedup-other")
	for i, item := range []struct {
		data []byte
		sha  string
	}{
		{sameData, sameSHA},
		{sameData, sameSHA},
		{otherData, HashSensitiveImage(otherData)},
	} {
		row := entity.TicketAttachment{
			TicketID: 9, UserID: 96, MediaType: "image/png", ByteSize: int64(len(item.data)),
			SHA256: item.sha, ScanState: entity.TicketAttachmentScanSensitive,
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed attachment %d: %v", i, err)
		}
		nonce := make([]byte, aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			t.Fatalf("nonce: %v", err)
		}
		ciphertext := aead.Seal(nil, nonce, item.data, ticketAttachmentAAD(row.ID, row.UserID, row.MediaType))
		if err := db.Model(&entity.TicketAttachment{}).Where("id = ?", row.ID).
			Updates(map[string]any{"nonce": nonce, "ciphertext": ciphertext}).Error; err != nil {
			t.Fatalf("write ciphertext %d: %v", i, err)
		}
	}
	calls := stubSensitiveImageDetector(t, SensitiveImageVerdict{Sensitive: false}, nil)

	result, err := ManualDetectSensitiveImageTicketWithDB(context.Background(), db, 9)
	if err != nil {
		t.Fatalf("manual detect ticket: %v", err)
	}
	if result.Total != 3 {
		t.Fatalf("expected Total 3, got %d", result.Total)
	}
	if result.Detected != 2 {
		t.Fatalf("expected 2 distinct images to be detected, got %d", result.Detected)
	}
	if *calls != 2 {
		t.Fatalf("duplicate images in one ticket must not be re-charged: expected 2 upstream calls, got %d", *calls)
	}
	if result.Skipped != 0 {
		t.Fatalf("expected no skipped rows, got %d", result.Skipped)
	}
}
