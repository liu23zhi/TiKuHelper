package registry

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// SQLite 存储警告的回归钉子（task_doc/108:297、109:474/486）。
//
// 这一组用例回答两个问题，缺一不可：
//  1. **判定**对不对：SQLite → 警告，MySQL → 不警告；
//  2. **真的写进日志了**：断言 gin.DefaultWriter 的实际输出里出现 WARN 行，
//     而不是断言代码里存在 SysWarn 字样 —— 后者是结构断言，改了调用点也绿。
//
// 第 2 条是本任务的核心风险：用户诉求是「警告没有如期出现」，
// 只测判定不测输出，等于没测「出现」。

// openStorageWarningSQLiteDB 开一个真实 SQLite 连接（dialector 名 = "sqlite"）。
func openStorageWarningSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "storage-warning.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// captureSysLog 把 gin.DefaultWriter/DefaultErrorWriter 换成内存 buffer，
// 返回读取函数（logger.SysWarn 写的是 gin.DefaultWriter）。
//
// 这是本仓断言「真实日志输出」的既有范式（见 controller/ai_fallback_failure_log_test.go）。
func captureSysLog(t *testing.T) func() string {
	t.Helper()
	var output, errors bytes.Buffer
	originalOutput, originalErrors := gin.DefaultWriter, gin.DefaultErrorWriter
	gin.DefaultWriter, gin.DefaultErrorWriter = &output, &errors
	t.Cleanup(func() { gin.DefaultWriter, gin.DefaultErrorWriter = originalOutput, originalErrors })
	return func() string { return output.String() + errors.String() }
}

// ─────────────────────── 判定层 ───────────────────────

func TestStorageStatusReportsSQLiteWithWarning(t *testing.T) {
	db := openStorageWarningSQLiteDB(t)
	status := storageStatusForDB(db)
	if status.Driver != StorageDriverSQLite {
		t.Fatalf("driver = %q, want %q", status.Driver, StorageDriverSQLite)
	}
	if strings.TrimSpace(status.Warning) == "" {
		t.Fatal("SQLite 必须带非空警告文案")
	}
	// 文案要点逐条断言（措辞可改，这三件事必须都在）。
	for _, want := range []string{"SQLite", "测试", "MySQL"} {
		if !strings.Contains(status.Warning, want) {
			t.Fatalf("警告文案 %q 必须包含 %q（说明现状 / 定性 / 行动）", status.Warning, want)
		}
	}
}

func TestNormalizeStorageDriverNeverMislabelsUnknownAsSQLite(t *testing.T) {
	// 未知 dialector 一律回落 mysql —— 反方向会把所有实例报成 SQLite，
	// 于是每个用户都看到一条假警告。
	cases := map[string]string{
		"sqlite":   StorageDriverSQLite,
		"SQLite":   StorageDriverSQLite,
		" sqlite ": StorageDriverSQLite,
		"mysql":    StorageDriverMySQL,
		"":         StorageDriverMySQL,
		"postgres": StorageDriverMySQL,
	}
	for input, want := range cases {
		if got := normalizeStorageDriver(input); got != want {
			t.Fatalf("normalizeStorageDriver(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestStorageStatusNilDBDegradesToMySQLWithoutWarning(t *testing.T) {
	// nil db（单测 / 启动失败路径）不能报 SQLite —— 否则前端在每次
	// 启动失败时都闪一条假横幅。
	status := storageStatusForDB(nil)
	if status.Driver != StorageDriverMySQL || status.Warning != "" {
		t.Fatalf("nil db must degrade silently, got %+v", status)
	}
}

func TestStorageStatusProductionFlagFollowsAppMode(t *testing.T) {
	db := openStorageWarningSQLiteDB(t)
	t.Setenv("TIKU_APP_MODE", "development")
	if storageStatusForDB(db).Production {
		t.Fatal("development 模式下 Production 必须为 false")
	}
	t.Setenv("TIKU_APP_MODE", "production")
	if !storageStatusForDB(db).Production {
		t.Fatal("production 模式下 Production 必须为 true")
	}
	// 空值 = 默认 production（与 ResolveApplicationMode 一致）。
	t.Setenv("TIKU_APP_MODE", "")
	if !storageStatusForDB(db).Production {
		t.Fatal("未设置 TIKU_APP_MODE 时按 production 处理")
	}
}

// ─────────────────────── 日志层（本任务的核心诉求）───────────────────────

func TestWarnIfSQLiteStorageEmitsWarnLineToRealOutput(t *testing.T) {
	db := openStorageWarningSQLiteDB(t)
	read := captureSysLog(t)

	warned := WarnIfSQLiteStorage(db)
	if !warned {
		t.Fatal("WarnIfSQLiteStorage 对 SQLite 必须返回 true")
	}

	out := read()
	if !strings.Contains(out, "[WARN]") {
		t.Fatalf("启动日志里必须有一条 WARN 行，实际输出：%q", out)
	}
	// 文案必须与端点同源：日志里出现的正是那个常量。
	if !strings.Contains(out, sqliteStorageWarning) {
		t.Fatalf("日志文案必须等于 sqliteStorageWarning（日志与前端同源），实际输出：%q", out)
	}
}

func TestWarnIfSQLiteStorageSilentForNonSQLite(t *testing.T) {
	// 用一个非 sqlite 的真实连接不好造，直接验判定函数在非 sqlite 上的行为：
	// normalizeStorageDriver 已覆盖 dialector 映射，这里验「nil → 不警告不输出」。
	read := captureSysLog(t)
	if WarnIfSQLiteStorage(nil) {
		t.Fatal("nil db 不得判定为 SQLite")
	}
	if out := read(); strings.TrimSpace(out) != "" {
		t.Fatalf("非 SQLite 时不得输出任何警告，实际：%q", out)
	}
}

// ─────────────────────── 一致性（日志 ↔ 端点）───────────────────────

func TestStorageWarningTextIsSingleSourceOfTruth(t *testing.T) {
	// 同一常量的两个出口必须是同一份字符串：日志（WarnIfSQLiteStorage）
	// 与端点（storageStatusForDB）。任一处另写一份中文就会漂移。
	db := openStorageWarningSQLiteDB(t)
	read := captureSysLog(t)
	WarnIfSQLiteStorage(db)
	logText := read()
	statusText := storageStatusForDB(db).Warning

	if !strings.Contains(logText, statusText) {
		t.Fatalf("日志与端点文案不同源：log=%q endpoint=%q", logText, statusText)
	}
}
