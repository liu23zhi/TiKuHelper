package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 日志文件清理配置化（task_doc/107 Q14 → 110 §2 → 111 §2）。
//
// 分两层测：
//
//  1. **配置层**（真实 sqlite 内存库）：默认值、非法值拒绝、部分提交、单位换算。
//     这层不碰文件系统。
//
//  2. **生效层**（真实 logger + 真实临时目录）：把配置应用到运行中的 logger，
//     断言「最旧归档先删、最新保留」「按 mtime 删超龄」「活动文件永不被删」。
//
// 第 2 层是本任务的**核心钉子**：删除逻辑在 pkg/logger（本任务不改），
// 但「配置真的被应用到那条链路上」只有真跑一遍才能证明 —— 换成断言
// 「我们调用了一个函数」就退化成了假钉子。

// newLogRetentionTestDB 建只含 system_settings 的内存库。
//
// 名字与 dsn 都独立于其它用例：本包大量用例共用内存 dsn 命名规则，
// 撞名会让 `cache=shared` 的两个库互相看到对方的行。
func newLogRetentionTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := fmt.Sprintf("file:log-retention-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.SystemSetting{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db
}

// setupLogRetentionTestLogger 把全局 logger 指向一个临时目录，返回该目录。
//
// 必须在**造测试文件之前**调用：logger.SetupGinLog 会跑 archiveExistingLogs，
// 把目录里已有的 managed 日志打包进 archive/ 并删掉原文件。
// 先造文件再 setup，文件会被提前搬走，后面的断言就测的是空目录。
func setupLogRetentionTestLogger(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	previousDir := *logger.LogDir
	*logger.LogDir = dir
	if err := logger.SetupGinLog(); err != nil {
		t.Fatalf("setup logger: %v", err)
	}
	t.Cleanup(func() {
		_ = logger.Close()
		*logger.LogDir = previousDir
	})
	return dir
}

// writeLogRetentionArchive 在 archive/ 下造一个批量归档包，并设置 mtime。
func writeLogRetentionArchive(t *testing.T, dir string, name string, size int, modTime time.Time) string {
	t.Helper()

	archiveDir := filepath.Join(dir, "archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		t.Fatalf("create archive dir: %v", err)
	}
	path := filepath.Join(archiveDir, name)
	if err := os.WriteFile(path, []byte(strings.Repeat("A", size)), 0644); err != nil {
		t.Fatalf("write archive %s: %v", name, err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set mtime on %s: %v", name, err)
	}
	return path
}

// writeLogRetentionActiveFile 往活动日志文件里写内容。
//
// 直接写文件而不是走 logger.SysLog：SysLog 会把内容同时写到 stdout 与
// 多个档位，且带时间戳，长度不可控 —— 本测试需要**精确字节数**来算总量。
func writeLogRetentionActiveFile(t *testing.T, dir string, name string, size int) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Repeat("a", size)), 0644); err != nil {
		t.Fatalf("write active log %s: %v", name, err)
	}
	return path
}

// ── 配置层 ─────────────────────────────────────────────────────────────

// 默认值必须是 30 天 / 10 GiB / 100 MiB（Q14 定稿），且字节换算是 1024 进制。
//
// 这条用例同时钉住「10 GB 存成 10 GiB」这个单位口径：
// 若有人把换算写成 1000 进制，10737418240 会变成 10000000000 —— 差 7%，
// 而页面上两者都显示「10 GB」量级，肉眼看不出来。
func TestLogRetentionDefaultsAreThirtyDaysTenGiBAndHundredMiB(t *testing.T) {
	db := newLogRetentionTestDB(t)

	settings := GetLogRetentionSettingsWithDB(db)
	if !settings.Enabled {
		t.Fatal("default must be enabled (Q14④ 默认启用)")
	}
	if settings.MaxAgeDays != 30 {
		t.Fatalf("default maxAgeDays must be 30, got %d", settings.MaxAgeDays)
	}
	if settings.MaxTotalGiB != 10 {
		t.Fatalf("default maxTotalGiB must be 10, got %d", settings.MaxTotalGiB)
	}
	if settings.MaxFileMiB != 100 {
		t.Fatalf("default maxFileMiB must be 100, got %d", settings.MaxFileMiB)
	}
	const wantTotalBytes int64 = 10 * 1024 * 1024 * 1024
	if settings.MaxTotalBytes != wantTotalBytes {
		t.Fatalf("default maxTotalBytes must be %d (10 GiB, 1024 进制), got %d", wantTotalBytes, settings.MaxTotalBytes)
	}
	const wantFileBytes int64 = 100 * 1024 * 1024
	if settings.MaxFileBytes != wantFileBytes {
		t.Fatalf("default maxFileBytes must be %d (100 MiB), got %d", wantFileBytes, settings.MaxFileBytes)
	}
	if settings.CleanupIntervalMinutes != DefaultLogRetentionCleanupIntervalMinutes {
		t.Fatalf("default cleanup interval must be %d minutes, got %d",
			DefaultLogRetentionCleanupIntervalMinutes, settings.CleanupIntervalMinutes)
	}
}

// 非法值必须被拒绝，且必须给出可区分的原因（不能一律「参数错误」）。
//
// 0 与负数是本任务明确点名的两类：0 在 logger 包里是「关闭」的开关语义，
// 在本层却是非法值 —— 放行 0 会让「按 0 字节上限清理」把归档删光。
func TestLogRetentionRejectsNonPositiveValues(t *testing.T) {
	db := newLogRetentionTestDB(t)

	zero := 0
	negative := -1
	totalZero := int64(0)
	totalNegative := int64(-5)
	fileZero := int64(0)
	fileNegative := int64(-1)
	intervalZero := 0

	cases := []struct {
		name  string
		input UpdateLogRetentionSettingsInput
		want  error
	}{
		{"age-zero", UpdateLogRetentionSettingsInput{MaxAgeDays: &zero}, ErrLogRetentionAgeDaysOutOfRange},
		{"age-negative", UpdateLogRetentionSettingsInput{MaxAgeDays: &negative}, ErrLogRetentionAgeDaysOutOfRange},
		{"total-zero", UpdateLogRetentionSettingsInput{MaxTotalGiB: &totalZero}, ErrLogRetentionTotalSizeOutOfRange},
		{"total-negative", UpdateLogRetentionSettingsInput{MaxTotalGiB: &totalNegative}, ErrLogRetentionTotalSizeOutOfRange},
		{"file-zero", UpdateLogRetentionSettingsInput{MaxFileMiB: &fileZero}, ErrLogRetentionMaxFileSizeOutOfRange},
		{"file-negative", UpdateLogRetentionSettingsInput{MaxFileMiB: &fileNegative}, ErrLogRetentionMaxFileSizeOutOfRange},
		{"interval-zero", UpdateLogRetentionSettingsInput{CleanupIntervalMinutes: &intervalZero}, ErrLogRetentionIntervalOutOfRange},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if _, err := UpdateLogRetentionSettingsWithDB(db, item.input); !errors.Is(err, item.want) {
				t.Fatalf("expected %v, got %v", item.want, err)
			}
		})
	}

	// 拒绝之后库里的值必须**一个都没变** —— 仍然全是默认值。
	//
	// 这一条防的是「逐项校验逐项写库」：那样非法请求会留下
	// 「总上限已改、单文件仍是旧值」的半截状态，而接口返回 400 时
	// 管理员会以为什么都没发生。
	settings := GetLogRetentionSettingsWithDB(db)
	if settings.MaxAgeDays != 30 || settings.MaxTotalGiB != 10 || settings.MaxFileMiB != 100 {
		t.Fatalf("rejected writes must not persist anything, got %+v", settings)
	}
}

// 跨字段约束：总上限必须装得下两个单文件，否则「按大小清理」等于只留活动文件。
func TestLogRetentionRejectsTotalSmallerThanTwoFiles(t *testing.T) {
	db := newLogRetentionTestDB(t)

	// 1 GiB 总上限 + 600 MiB 单文件：一个归档都留不住（1 GiB < 2×600 MiB）。
	total := int64(1)
	file := int64(600)
	_, err := UpdateLogRetentionSettingsWithDB(db, UpdateLogRetentionSettingsInput{
		MaxTotalGiB: &total,
		MaxFileMiB:  &file,
	})
	if !errors.Is(err, ErrLogRetentionTotalBelowTwoFiles) {
		t.Fatalf("expected ErrLogRetentionTotalBelowTwoFiles, got %v", err)
	}

	// 边界：恰好两倍必须**通过**（不是严格小于才拒绝）。
	total = int64(2)
	file = int64(1024)
	settings, err := UpdateLogRetentionSettingsWithDB(db, UpdateLogRetentionSettingsInput{
		MaxTotalGiB: &total,
		MaxFileMiB:  &file,
	})
	if err != nil {
		t.Fatalf("exactly two files must be accepted: %v", err)
	}
	if settings.MaxTotalGiB != 2 || settings.MaxFileMiB != 1024 {
		t.Fatalf("stored values mismatch: %+v", settings)
	}
}

// 合法值必须落库、可回读，且键名统一 log_retention_ 前缀（Q14⑤）。
func TestLogRetentionUpdatePersistsValuesAndUsesPrefixedKeys(t *testing.T) {
	db := newLogRetentionTestDB(t)

	age := 45
	total := int64(20)
	file := int64(50)
	interval := 15
	settings, err := UpdateLogRetentionSettingsWithDB(db, UpdateLogRetentionSettingsInput{
		MaxAgeDays:             &age,
		MaxTotalGiB:            &total,
		MaxFileMiB:             &file,
		CleanupIntervalMinutes: &interval,
	})
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if settings.MaxAgeDays != 45 || settings.MaxTotalGiB != 20 || settings.MaxFileMiB != 50 || settings.CleanupIntervalMinutes != 15 {
		t.Fatalf("returned settings mismatch: %+v", settings)
	}

	var rows []entity.SystemSetting
	if err := db.Where("`key` LIKE ?", LogRetentionKeyPrefix+"%").Find(&rows).Error; err != nil {
		t.Fatalf("load settings rows: %v", err)
	}
	values := make(map[string]string, len(rows))
	for _, row := range rows {
		values[row.Key] = row.Value
	}
	want := map[string]string{
		LogRetentionEnabledSettingKey:                "true",
		LogRetentionMaxAgeDaysSettingKey:             "45",
		LogRetentionMaxTotalGiBSettingKey:            "20",
		LogRetentionMaxFileMiBSettingKey:             "50",
		LogRetentionCleanupIntervalMinutesSettingKey: "15",
	}
	for key, expected := range want {
		if got, ok := values[key]; !ok || got != expected {
			t.Fatalf("setting %s: want %q, got %q (present=%v); all keys=%v", key, expected, got, ok, values)
		}
	}

	// 回读：走一次真实库读，确认不是只用返回值自证。
	reloaded := GetLogRetentionSettingsWithDB(db)
	if reloaded.MaxAgeDays != 45 || reloaded.MaxTotalGiB != 20 || reloaded.MaxFileMiB != 50 {
		t.Fatalf("reloaded settings mismatch: %+v", reloaded)
	}
	if reloaded.MaxTotalBytes != 20*(1<<30) || reloaded.MaxFileBytes != 50*(1<<20) {
		t.Fatalf("derived bytes mismatch: total=%d file=%d", reloaded.MaxTotalBytes, reloaded.MaxFileBytes)
	}
}

// 未提交的字段必须保持现值：只改天数的那次 PUT 不能把体积上限写成 0 并因此失败。
func TestLogRetentionPartialUpdateKeepsOtherFields(t *testing.T) {
	db := newLogRetentionTestDB(t)

	age := 90
	if _, err := UpdateLogRetentionSettingsWithDB(db, UpdateLogRetentionSettingsInput{MaxAgeDays: &age}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	file := int64(200)
	settings, err := UpdateLogRetentionSettingsWithDB(db, UpdateLogRetentionSettingsInput{MaxFileMiB: &file})
	if err != nil {
		t.Fatalf("partial update must not fail: %v", err)
	}
	if settings.MaxAgeDays != 90 {
		t.Fatalf("maxAgeDays must keep 90, got %d", settings.MaxAgeDays)
	}
	if settings.MaxFileMiB != 200 {
		t.Fatalf("maxFileMiB must be 200, got %d", settings.MaxFileMiB)
	}
	if settings.MaxTotalGiB != 10 {
		t.Fatalf("maxTotalGiB must keep default 10, got %d", settings.MaxTotalGiB)
	}
}

// 库里存了越界/垃圾值时必须回落默认值，而不是「按那个值清理」。
func TestLogRetentionReadFallsBackOnCorruptValues(t *testing.T) {
	db := newLogRetentionTestDB(t)

	corrupt := map[string]string{
		LogRetentionMaxAgeDaysSettingKey:  "0",
		LogRetentionMaxTotalGiBSettingKey: "-3",
		LogRetentionMaxFileMiBSettingKey:  "not-a-number",
		LogRetentionEnabledSettingKey:     "maybe",
	}
	for key, value := range corrupt {
		if err := db.Create(&entity.SystemSetting{Key: key, Value: value}).Error; err != nil {
			t.Fatalf("seed corrupt value for %s: %v", key, err)
		}
	}

	settings := GetLogRetentionSettingsWithDB(db)
	if settings.MaxAgeDays != 30 || settings.MaxTotalGiB != 10 || settings.MaxFileMiB != 100 {
		t.Fatalf("corrupt values must fall back to defaults, got %+v", settings)
	}
	if !settings.Enabled {
		t.Fatal("unparseable enabled value must fall back to the default true")
	}
}

// 关闭状态：不调用任何 setter，也不因为「关闭」而拿到一个错误。
func TestLogRetentionDisabledSkipsApply(t *testing.T) {
	cfg := LogRetentionConfig{
		Enabled:         false,
		MaxAgeDays:      30,
		MaxTotalBytes:   10 << 30,
		MaxFileBytes:    100 << 20,
		CleanupInterval: time.Hour,
	}
	result, err := ApplyLogRetentionConfig(cfg)
	if err != nil {
		t.Fatalf("disabled config must not error: %v", err)
	}
	if result.Applied || result.AgeApplied || result.TotalSizeApplied {
		t.Fatalf("disabled config must apply nothing, got %+v", result)
	}
}

// 清理失败只返回错误，不 panic、不影响调用方（Q14⑦：失败不影响主服务）。
func TestApplyLogRetentionConfigWithoutLoggerReportsError(t *testing.T) {
	if err := logger.Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}
	cfg := LogRetentionConfig{
		Enabled:         true,
		MaxAgeDays:      30,
		MaxTotalBytes:   10 << 30,
		MaxFileBytes:    100 << 20,
		CleanupInterval: time.Hour,
	}
	result, err := ApplyLogRetentionConfig(cfg)
	if err == nil {
		t.Fatal("applying without an initialized logger must report an error")
	}
	if !strings.Contains(err.Error(), "初始化") && !strings.Contains(err.Error(), "initialized") {
		t.Fatalf("error should point at the uninitialized logger, got %v", err)
	}
	if result.Applied {
		t.Fatalf("nothing could be applied, got %+v", result)
	}
}

// 单文件上限当前**完全无法生效（重启也不行）**，必须如实报告而不是假装。
//
// 这条用例同时防两种退化：
//   - 有人把 FileSizeApplied 改成 true（假装生效）；
//   - 有人把原因文案清空（管理员看不到要去哪解决）。
//
// 事实依据：SetupGinLog 是 setupLoggerWithLimits(*LogDir, MaxLogFileBytes, ...)，
// maxBytes 吃常量；全仓没有把设置值传给 setupLoggerWithRetention 的代码。
func TestApplyLogRetentionConfigReportsFileSizeUnsupported(t *testing.T) {
	dir := setupLogRetentionTestLogger(t)
	_ = dir
	result, err := ApplyLogRetentionConfig(LogRetentionConfig{
		Enabled:         true,
		MaxAgeDays:      30,
		MaxTotalBytes:   10 << 30,
		MaxFileBytes:    100 << 20,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("apply config: %v", err)
	}
	if !result.AgeApplied || !result.TotalSizeApplied || !result.Applied {
		t.Fatalf("both runtime setters must succeed, got %+v", result)
	}
	if result.FileSizeApplied {
		t.Fatal("file size limit has no setter AND no startup path — it must never report applied")
	}
	if result.FileSizeUnsupportedReason == "" {
		t.Fatal("unsupported file size must carry a human-readable reason")
	}
	// 文案不能让管理员以为「重启就好」——重启不会生效。
	if strings.Contains(result.FileSizeUnsupportedReason, "重启") &&
		!strings.Contains(result.FileSizeUnsupportedReason, "重启也不会生效") {
		t.Fatalf("reason must not imply that restarting fixes it: %q", result.FileSizeUnsupportedReason)
	}
}

// DescribeLogRetentionCapability 必须**无副作用**：不删任何文件。
//
// GET 用它填 Apply 字段。若它内部去调 setter（setter 会顺手清理），
// 就变成「一个 GET 删日志文件」——浏览器的预取、监控探针都会触发。
func TestDescribeLogRetentionCapabilityHasNoSideEffects(t *testing.T) {
	dir := setupLogRetentionTestLogger(t)
	now := time.Now()
	// 造一份按配置「该被删」的超龄归档：40 天前，配置保留 30 天。
	expired := writeLogRetentionArchive(t, dir, "20260101_000000.zip", 128, now.Add(-40*24*time.Hour))

	settings := LogRetentionSettings{
		Enabled:                true,
		MaxAgeDays:             30,
		MaxTotalGiB:            1,
		MaxFileMiB:             100,
		CleanupIntervalMinutes: 60,
		MaxTotalBytes:          1 << 30,
		MaxFileBytes:           100 << 20,
	}
	result := DescribeLogRetentionCapability(settings)

	if _, err := os.Stat(expired); err != nil {
		t.Fatalf("describe must not delete anything, expired archive was removed: %v", err)
	}
	if !result.AgeApplied || !result.TotalSizeApplied || !result.Applied {
		t.Fatalf("enabled config should report both setters as capable, got %+v", result)
	}
	if result.FileSizeApplied || result.FileSizeUnsupportedReason == "" {
		t.Fatalf("capability report must carry the file size limitation, got %+v", result)
	}
	if result.MaxAgeDays != 30 || result.MaxTotalBytes != 1<<30 || result.MaxFileBytes != 100<<20 {
		t.Fatalf("capability report must echo the config, got %+v", result)
	}
}

// 关闭状态下能力报告不谎称「已生效」。
func TestDescribeLogRetentionCapabilityWhenDisabled(t *testing.T) {
	result := DescribeLogRetentionCapability(LogRetentionSettings{
		Enabled:                false,
		MaxAgeDays:             30,
		MaxTotalGiB:            10,
		MaxFileMiB:             100,
		CleanupIntervalMinutes: 60,
		MaxTotalBytes:          10 << 30,
		MaxFileBytes:           100 << 20,
	})
	if result.Enabled || result.Applied || result.AgeApplied || result.TotalSizeApplied {
		t.Fatalf("disabled config must report nothing applied, got %+v", result)
	}
	if result.FileSizeUnsupportedReason == "" {
		t.Fatal("the file size limitation exists regardless of the enabled switch")
	}
}

// ── 生效层（真实 logger + 真实目录）────────────────────────────────────

// 超过总体积上限时：**最旧的归档先被删，最新的保留，活动文件永不被删**。
//
// 数字选择（便于核对，单位字节）：
//
//	活动文件 Tiku.txt      1024
//	archive/20260101_...   1024  ← 最旧，应被删
//	archive/20260102_...   1024  ← 最新，应保留
//	合计 3072，上限 3000  ⇒ 删掉最旧那份后 2048 ≤ 3000 立即停止
//
// 上限刻意只比总量小一点：这样「删一份就够」与「该不该删」都能区分开，
// 不会出现「多删了也看不出来」的糊涂通过。
func TestLogRetentionSizePruneDeletesOldestArchiveAndKeepsNewest(t *testing.T) {
	dir := setupLogRetentionTestLogger(t)

	activePath := writeLogRetentionActiveFile(t, dir, "Tiku.txt", 1024)
	now := time.Now()
	oldest := writeLogRetentionArchive(t, dir, "20260101_000000.zip", 1024, now.Add(-48*time.Hour))
	newest := writeLogRetentionArchive(t, dir, "20260102_000000.zip", 1024, now.Add(-1*time.Hour))

	result, err := ApplyLogRetentionConfig(LogRetentionConfig{
		Enabled:         true,
		MaxAgeDays:      3650, // 放很大：本用例只测「按大小」，年龄闸门必须完全不参与
		MaxTotalBytes:   3000,
		MaxFileBytes:    1024,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("apply config: %v", err)
	}
	if !result.TotalSizeApplied {
		t.Fatalf("total size setter must be applied, got %+v", result)
	}

	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Fatalf("oldest archive must be removed by size pruning, err=%v", err)
	}
	if got, err := os.ReadFile(newest); err != nil || len(got) != 1024 {
		t.Fatalf("newest archive must be kept intact, err=%v len=%d", err, len(got))
	}
	// 活动文件永不被删 —— 这条是本任务点名要有的钉子，防将来有人把
	// pruneLogFiles 的候选集合从「归档」放宽成「所有 managed 文件」。
	// 删正在写入的文件会让写入直接失败，且日志本身就是排查手段。
	if got, err := os.ReadFile(activePath); err != nil || len(got) != 1024 {
		t.Fatalf("active log must never be removed by size pruning, err=%v len=%d", err, len(got))
	}
}

// 活动文件被计入总量统计，但**永不出现在删除候选里**。
//
// 这是「活动文件永不被删」的最强形态：活动文件本身就把总量撑破了，
// 而且一个归档都没有 —— 此时唯一能删的就是活动文件。
// 正确实现必须删无可删（总量仍然超限），绝不能删活动文件。
//
// 为什么这个场景必须单独测：
//   - 上一个用例里活动文件只有 1024 字节，删掉两份归档就够收敛，
//     实现即使把活动文件也算进候选，也可能**没轮到它**就停了；
//   - 本用例让它成为「唯一候选」，才能把「候选集合是否含活动文件」暴露出来。
func TestLogRetentionSizePruneNeverDeletesActiveFileEvenWhenItIsTheOnlyCandidate(t *testing.T) {
	dir := setupLogRetentionTestLogger(t)

	// 活动文件 5000 字节，上限 1000：总量必然超限，且没有归档可删。
	// 单文件上限取 500（= 总上限的一半，满足跨字段约束，且与本用例无关）。
	activePath := writeLogRetentionActiveFile(t, dir, "Tiku.txt", 5000)

	result, err := ApplyLogRetentionConfig(LogRetentionConfig{
		Enabled:         true,
		MaxAgeDays:      3650, // 年龄闸门不参与
		MaxTotalBytes:   1000,
		MaxFileBytes:    500,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("apply config: %v", err)
	}
	if !result.TotalSizeApplied {
		t.Fatalf("total size setter must be applied, got %+v", result)
	}

	if got, err := os.ReadFile(activePath); err != nil || len(got) != 5000 {
		t.Fatalf("active log must never be removed even when it is the only deletion candidate, err=%v len=%d", err, len(got))
	}
}

// 超过保留天数时按 **mtime** 删除超龄归档，且活动文件永不被删。
func TestLogRetentionAgePruneDeletesByModTimeAndKeepsActiveFile(t *testing.T) {
	dir := setupLogRetentionTestLogger(t)

	activePath := writeLogRetentionActiveFile(t, dir, "Tiku.txt", 64)
	now := time.Now()
	expired := writeLogRetentionArchive(t, dir, "20260101_000000.zip", 128, now.Add(-40*24*time.Hour))
	fresh := writeLogRetentionArchive(t, dir, "20260102_000000.zip", 128, now.Add(-24*time.Hour))
	// 活动文件也做旧：证明「旧」不是删除的充分条件，只有归档才受影响。
	if err := os.Chtimes(activePath, now.Add(-400*24*time.Hour), now.Add(-400*24*time.Hour)); err != nil {
		t.Fatalf("age active log: %v", err)
	}

	result, err := ApplyLogRetentionConfig(LogRetentionConfig{
		Enabled:         true,
		MaxAgeDays:      30,
		MaxTotalBytes:   1 << 20, // 放很大：本用例只测「按年龄」
		MaxFileBytes:    1024,
		CleanupInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("apply config: %v", err)
	}
	if !result.AgeApplied {
		t.Fatalf("age setter must be applied, got %+v", result)
	}

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("expired archive (40 days old, retention 30) must be removed, err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("archive within retention must be kept: %v", err)
	}
	if got, err := os.ReadFile(activePath); err != nil || len(got) != 64 {
		t.Fatalf("active log must never be removed by age pruning, err=%v len=%d", err, len(got))
	}
}

// 后台清理 goroutine：到点自动按库里的配置清理，停止函数能干净退出。
func TestStartLogRetentionCleanupAppliesPersistedConfig(t *testing.T) {
	dir := setupLogRetentionTestLogger(t)
	db := newLogRetentionTestDB(t)

	expired := writeLogRetentionArchive(t, dir, "20260101_000000.zip", 128, time.Now().Add(-40*24*time.Hour))
	age := 30
	total := int64(10)
	file := int64(1)
	interval := 30
	if _, err := UpdateLogRetentionSettingsWithDB(db, UpdateLogRetentionSettingsInput{
		MaxAgeDays:             &age,
		MaxTotalGiB:            &total,
		MaxFileMiB:             &file,
		CleanupIntervalMinutes: &interval,
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	stop := StartLogRetentionCleanup(db, 20*time.Millisecond)
	defer stop()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(expired); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background cleanup did not remove the expired archive within 5s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// interval <= 0 时必须从 system_settings 重新解析间隔（改配置即可生效，无需重启）。
func TestStartLogRetentionCleanupStopsCleanlyWithDynamicInterval(t *testing.T) {
	db := newLogRetentionTestDB(t)
	interval := 1
	if _, err := UpdateLogRetentionSettingsWithDB(db, UpdateLogRetentionSettingsInput{CleanupIntervalMinutes: &interval}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	stop := StartLogRetentionCleanup(db, 0)
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop function must return promptly")
	}
}
