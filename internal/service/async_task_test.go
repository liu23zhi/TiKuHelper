package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/itihey/tikuAdapter/internal/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newAsyncTaskTestDB 建一个隔离的异步任务测试库。
//
// 库名带 t.Name()：SQLite 的 file:...?mode=memory&cache=shared 在同一进程内
// 同名库是共享的，跨用例会互相污染（与 newBlacklistTestDB 同款）。
//
// ⚠️ AutoMigrate **必须逐个调用**：一次传多个模型时 GORM 会合并建表语句，
// 物理表可能只剩几列，插入报「no such column」（错误完全不提 AutoMigrate）。
// 这条是 captain 广播的已验证事故，本仓测试 helper 一律分次调用。
func newAsyncTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:async-task-"+name+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	for _, model := range []any{
		&entity.SystemSetting{}, &entity.AsyncTask{}, &entity.User{}, &entity.Tiku{},
	} {
		if err := db.AutoMigrate(model); err != nil {
			t.Fatalf("migrate %T: %v", model, err)
		}
	}
	return db
}

// newTestAsyncTaskManager 构造一个不 Start 的管理器（不引入并发与 goroutine）。
func newTestAsyncTaskManager(t *testing.T, db *gorm.DB, baseDir string) *AsyncTaskManager {
	t.Helper()
	manager := NewAsyncTaskManager(db, baseDir)
	manager.Retention = time.Hour
	manager.Timeout = time.Minute
	manager.Concurrency = 1
	manager.RegisterDefaultAsyncTaskExecutors()
	return manager
}

func seedAsyncTask(t *testing.T, db *gorm.DB, task entity.AsyncTask) entity.AsyncTask {
	t.Helper()
	if task.TaskID == "" {
		t.Fatalf("seedAsyncTask requires TaskID")
	}
	if task.Type == "" {
		task.Type = entity.AsyncTaskTypeExport
	}
	if task.Status == "" {
		task.Status = entity.AsyncTaskStatusPending
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("create async task: %v", err)
	}
	return task
}

func loadAsyncTask(t *testing.T, db *gorm.DB, taskID string) entity.AsyncTask {
	t.Helper()
	var task entity.AsyncTask
	if err := db.Where("task_id = ?", taskID).First(&task).Error; err != nil {
		t.Fatalf("load async task %q: %v", taskID, err)
	}
	return task
}

// ─────────────────────────── 过期清理（任务书必写测试） ───────────────────────────

// 任务过期 1 小时后被清理，未过期的不会被删（任务书 ⑤ + 必写测试）。
//
// 三态断言，缺一条这个用例就是空的：
//  1. 已过期 → 行被删、文件被删；
//  2. 未过期（还差 1 秒）→ 行在、文件在；
//  3. **running 即使过期也不删**（执行器可能正在写这个文件）。
func TestAsyncTaskCleanupRemovesExpiredKeepsUnexpired(t *testing.T) {
	db := newAsyncTaskTestDB(t)
	baseDir := t.TempDir()
	manager := newTestAsyncTaskManager(t, db, baseDir)
	now := time.Now()

	writeFile := func(name string) string {
		path := filepath.Join(baseDir, name)
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	expiredPath := writeFile("expired.csv")
	unexpiredPath := writeFile("unexpired.csv")
	runningPath := writeFile("running.csv")

	// 1) 已过期 1 小时（时间点精确：now-1h，且清理用 now）。
	expiredAt := now.Add(-time.Hour)
	seedAsyncTask(t, db, entity.AsyncTask{
		TaskID: "exp-expired00000001", Status: entity.AsyncTaskStatusSucceeded,
		FilePath: expiredPath, ExpiresAt: &expiredAt,
	})
	// 2) 未过期（1 秒后到期）。
	future := now.Add(time.Second)
	seedAsyncTask(t, db, entity.AsyncTask{
		TaskID: "exp-unexpired000001", Status: entity.AsyncTaskStatusSucceeded,
		FilePath: unexpiredPath, ExpiresAt: &future,
	})
	// 3) 已过期但仍在 running（执行中，不能被删）。
	seedAsyncTask(t, db, entity.AsyncTask{
		TaskID: "exp-running00000001", Status: entity.AsyncTaskStatusRunning,
		FilePath: runningPath, ExpiresAt: &expiredAt,
	})

	deleted, err := manager.CleanupExpiredTasks(now)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (only the expired, non-running task)", deleted)
	}

	// 行层面。
	var expiredCount, unexpiredCount, runningCount int64
	if err := db.Model(&entity.AsyncTask{}).Where("task_id = ?", "exp-expired00000001").Count(&expiredCount).Error; err != nil {
		t.Fatalf("count expired: %v", err)
	}
	if err := db.Model(&entity.AsyncTask{}).Where("task_id = ?", "exp-unexpired000001").Count(&unexpiredCount).Error; err != nil {
		t.Fatalf("count unexpired: %v", err)
	}
	if err := db.Model(&entity.AsyncTask{}).Where("task_id = ?", "exp-running00000001").Count(&runningCount).Error; err != nil {
		t.Fatalf("count running: %v", err)
	}
	if expiredCount != 0 {
		t.Fatalf("expired task must be deleted, got %d rows", expiredCount)
	}
	if unexpiredCount != 1 {
		t.Fatalf("unexpired task must be kept, got %d rows", unexpiredCount)
	}
	if runningCount != 1 {
		t.Fatalf("running task must be kept even if expired, got %d rows", runningCount)
	}

	// 文件层面（这是「过期清理」真正的交付物，只看行会漏掉「行删了文件还在」）。
	if _, err := os.Stat(expiredPath); !os.IsNotExist(err) {
		t.Fatalf("expired task file must be removed, stat err = %v", err)
	}
	if _, err := os.Stat(unexpiredPath); err != nil {
		t.Fatalf("unexpired task file must be kept: %v", err)
	}
	if _, err := os.Stat(runningPath); err != nil {
		t.Fatalf("running task file must be kept: %v", err)
	}

	// 重复清理：已删的不会报错、不会再算一次。
	deleted, err = manager.CleanupExpiredTasks(now)
	if err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("second cleanup deleted = %d, want 0 (idempotent)", deleted)
	}
}

// 清理绝不删除 BaseDir 之外的文件（SourcePath/FilePath 来自外部输入，必须做前缀校验）。
func TestAsyncTaskCleanupRefusesFilesOutsideBaseDir(t *testing.T) {
	db := newAsyncTaskTestDB(t)
	baseDir := t.TempDir()
	manager := newTestAsyncTaskManager(t, db, baseDir)

	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "must-keep.csv")
	if err := os.WriteFile(outsidePath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	expiredAt := time.Now().Add(-time.Hour)
	seedAsyncTask(t, db, entity.AsyncTask{
		TaskID: "exp-outside0000001", Status: entity.AsyncTaskStatusSucceeded,
		FilePath: outsidePath, ExpiresAt: &expiredAt,
	})
	if _, err := manager.CleanupExpiredTasks(time.Now()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	// 行被删（它确实过期了），但**文件必须留着** —— 我们没有权限/理由删 BaseDir 外的文件。
	if _, err := os.Stat(outsidePath); err != nil {
		t.Fatalf("file outside BaseDir must never be removed: %v", err)
	}
}

// 清理失败不影响主服务：库不可用时返回错误但不 panic，调用方（cleanupLoop）只记日志。
func TestAsyncTaskCleanupWithoutTableReturnsError(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:async-task-notables?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	manager := newTestAsyncTaskManager(t, db, t.TempDir())
	if _, err := manager.CleanupExpiredTasks(time.Now()); !errors.Is(err, ErrAsyncTaskDBUnavailable) {
		t.Fatalf("cleanup without table err = %v, want ErrAsyncTaskDBUnavailable", err)
	}
}

// ─────────────────────────── 重启恢复（任务书必写测试） ───────────────────────────

// 进程重启后 pending 任务被重新捡起，且崩溃遗留的 running 行被翻回 pending。
//
// 这个用例断言的是「恢复之后真的能被执行完」，不是「状态字段被改过」——
// 后者是假钉子：改了状态但 worker 不捡起来，用户看到的仍是永远排队。
func TestAsyncTaskRecoverPicksUpPendingAfterRestart(t *testing.T) {
	db := newAsyncTaskTestDB(t)
	baseDir := t.TempDir()
	manager := newTestAsyncTaskManager(t, db, baseDir)

	// 模拟上一个进程留下的三条：pending（从没跑）、running（跑到一半进程被 kill）、终态。
	seedAsyncTask(t, db, entity.AsyncTask{
		TaskID: "exp-pending00000001", Status: entity.AsyncTaskStatusPending,
		Resource: ExportResourceQuestions, Format: "csv", Masked: true,
	})
	seedAsyncTask(t, db, entity.AsyncTask{
		TaskID: "exp-running00000002", Status: entity.AsyncTaskStatusRunning,
		Resource: ExportResourceQuestions, Format: "csv", Masked: true,
	})
	seedAsyncTask(t, db, entity.AsyncTask{
		TaskID: "exp-done0000000003", Status: entity.AsyncTaskStatusSucceeded,
		Resource: ExportResourceQuestions, Format: "csv", Masked: true,
	})

	if err := manager.RecoverInterruptedTasks(); err != nil {
		t.Fatalf("recover: %v", err)
	}
	// running → pending（崩溃残骸必须重跑）。
	if recovered := loadAsyncTask(t, db, "exp-running00000002"); recovered.Status != entity.AsyncTaskStatusPending {
		t.Fatalf("interrupted running task status = %q, want pending", recovered.Status)
	}
	if recovered := loadAsyncTask(t, db, "exp-running00000002"); !strings.Contains(recovered.Message, "重新排队") {
		t.Fatalf("recovered task message = %q, want it to mention 重新排队", recovered.Message)
	}
	// 终态不被动。
	if done := loadAsyncTask(t, db, "exp-done0000000003"); done.Status != entity.AsyncTaskStatusSucceeded {
		t.Fatalf("terminal task status = %q, want unchanged succeeded", done.Status)
	}

	// 关键断言：恢复之后这些任务真的会被执行完（不是只改状态）。
	manager.Start()
	defer manager.Stop()

	deadline := time.Now().Add(15 * time.Second)
	for {
		pendingCount := int64(0)
		if err := db.Model(&entity.AsyncTask{}).
			Where("status IN ?", []string{entity.AsyncTaskStatusPending, entity.AsyncTaskStatusRunning}).
			Count(&pendingCount).Error; err != nil {
			t.Fatalf("count pending: %v", err)
		}
		if pendingCount == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending tasks were not picked up after restart: still %d", pendingCount)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, taskID := range []string{"exp-pending00000001", "exp-running00000002"} {
		task := loadAsyncTask(t, db, taskID)
		if task.Status != entity.AsyncTaskStatusSucceeded {
			t.Fatalf("%s status = %q (%s), want succeeded", taskID, task.Status, task.ErrorMessage)
		}
		if strings.TrimSpace(task.FilePath) == "" {
			t.Fatalf("%s must produce a result file", taskID)
		}
		if _, err := os.Stat(task.FilePath); err != nil {
			t.Fatalf("%s result file missing: %v", taskID, err)
		}
		// 完成时 ExpiresAt 必须被改写成「完成时刻 + 保留时长」。
		if task.ExpiresAt == nil {
			t.Fatalf("%s must have ExpiresAt set on completion", taskID)
		}
	}
}

// 无执行器时任务以 failed + 明确原因结束，不会永远停在 pending。
func TestAsyncTaskWithoutExecutorFailsExplicitly(t *testing.T) {
	db := newAsyncTaskTestDB(t)
	manager := NewAsyncTaskManager(db, t.TempDir())
	manager.Timeout = time.Minute
	manager.Concurrency = 1

	task := seedAsyncTask(t, db, entity.AsyncTask{
		TaskID: "grn-noexecutor0001", Type: entity.AsyncTaskTypeBatchGrant,
		Status: entity.AsyncTaskStatusPending,
	})
	manager.Start()
	defer manager.Stop()

	deadline := time.Now().Add(10 * time.Second)
	for {
		current := loadAsyncTask(t, db, task.TaskID)
		if entity.AsyncTaskIsTerminal(current.Status) {
			if current.Status != entity.AsyncTaskStatusFailed {
				t.Fatalf("status = %q, want failed", current.Status)
			}
			if !strings.Contains(current.ErrorMessage, ErrAsyncTaskTypeUnsupported.Error()) {
				t.Fatalf("error message = %q, want it to mention 执行器未注册", current.ErrorMessage)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task stayed in %q, want failed", current.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 执行器 panic 不能带崩进程，且任务落 failed。
func TestAsyncTaskExecutorPanicIsRecovered(t *testing.T) {
	db := newAsyncTaskTestDB(t)
	manager := NewAsyncTaskManager(db, t.TempDir())
	manager.Timeout = time.Minute
	manager.RegisterExecutor(entity.AsyncTaskTypeBatchGrant, func(context.Context, *gorm.DB, entity.AsyncTask) (AsyncTaskOutcome, error) {
		panic("boom")
	})
	task := seedAsyncTask(t, db, entity.AsyncTask{
		TaskID: "grn-panic000000001", Type: entity.AsyncTaskTypeBatchGrant,
		Status: entity.AsyncTaskStatusPending,
	})
	manager.Start()
	defer manager.Stop()

	deadline := time.Now().Add(10 * time.Second)
	for {
		current := loadAsyncTask(t, db, task.TaskID)
		if entity.AsyncTaskIsTerminal(current.Status) {
			if current.Status != entity.AsyncTaskStatusFailed {
				t.Fatalf("status = %q, want failed", current.Status)
			}
			if !strings.Contains(current.ErrorMessage, "panic") {
				t.Fatalf("error message = %q, want it to mention panic", current.ErrorMessage)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("task stayed in %q, want failed", current.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ─────────────────────────── 提交与查询 ───────────────────────────

// 提交写出 pending + 对外 ID 前缀正确 + 任务 ID 不重复。
func TestAsyncTaskSubmitWritesPendingWithPrefixedUniqueID(t *testing.T) {
	db := newAsyncTaskTestDB(t)
	manager := newTestAsyncTaskManager(t, db, t.TempDir())
	actor := entity.User{ID: 7, Username: "admin"}

	first, err := manager.SubmitAsyncTask(entity.AsyncTaskTypeExport, ExportResourceUsers, "csv", true, "{}", "", actor)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if first.Status != entity.AsyncTaskStatusPending {
		t.Fatalf("status = %q, want pending", first.Status)
	}
	if !strings.HasPrefix(first.TaskID, "exp-") {
		t.Fatalf("taskId = %q, want exp- prefix", first.TaskID)
	}
	if first.ActorUserID != 7 || first.ActorUsername != "admin" {
		t.Fatalf("actor = %d/%q, want 7/admin", first.ActorUserID, first.ActorUsername)
	}
	if first.ExpiresAt == nil {
		t.Fatal("pending task must carry a fallback ExpiresAt (otherwise it can never be cleaned)")
	}

	second, err := manager.SubmitAsyncTask(entity.AsyncTaskTypeImport, ImportResourceBlacklist, "csv", false, "{}", "/tmp/x.csv", actor)
	if err != nil {
		t.Fatalf("submit second: %v", err)
	}
	if !strings.HasPrefix(second.TaskID, "imp-") {
		t.Fatalf("taskId = %q, want imp- prefix", second.TaskID)
	}
	if first.TaskID == second.TaskID {
		t.Fatal("task ids must be unique")
	}
	// 四类都有前缀（batch_grant / batch_rollback 由 u-grant 用）。
	for _, taskType := range []string{
		entity.AsyncTaskTypeImport, entity.AsyncTaskTypeExport,
		entity.AsyncTaskTypeBatchGrant, entity.AsyncTaskTypeBatchRollback,
	} {
		if AsyncTaskTaskIDPrefix[taskType] == "" {
			t.Fatalf("task type %q must have an id prefix", taskType)
		}
	}
}

// 在没有任何任务表时提交必须明确报错（不能假装成功）。
func TestAsyncTaskSubmitWithoutTableFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:async-task-submit-notables?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	manager := NewAsyncTaskManager(db, t.TempDir())
	if _, err := manager.SubmitAsyncTask(entity.AsyncTaskTypeExport, ExportResourceUsers, "csv", true, "{}", "", entity.User{}); !errors.Is(err, ErrAsyncTaskDBUnavailable) {
		t.Fatalf("submit err = %v, want ErrAsyncTaskDBUnavailable", err)
	}
}

// 下载判据三态：未完成 → 409、过期 → 410、成功 → 能打开文件。
func TestAsyncTaskOpenResultFileStates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.csv")
	if err := os.WriteFile(path, []byte("a,b\n1,2\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	now := time.Now()

	// 1) pending → ErrAsyncTaskNotReady
	if _, _, err := OpenAsyncTaskResultFile(entity.AsyncTask{Status: entity.AsyncTaskStatusPending, FilePath: path}, now); !errors.Is(err, ErrAsyncTaskNotReady) {
		t.Fatalf("pending err = %v, want ErrAsyncTaskNotReady", err)
	}
	// 2) failed → ErrAsyncTaskNotReady（失败任务没有结果文件）
	if _, _, err := OpenAsyncTaskResultFile(entity.AsyncTask{Status: entity.AsyncTaskStatusFailed, FilePath: path}, now); !errors.Is(err, ErrAsyncTaskNotReady) {
		t.Fatalf("failed err = %v, want ErrAsyncTaskNotReady", err)
	}
	// 3) 过期 → ErrAsyncTaskFileExpired
	past := now.Add(-time.Hour)
	if _, _, err := OpenAsyncTaskResultFile(entity.AsyncTask{
		Status: entity.AsyncTaskStatusSucceeded, FilePath: path, ExpiresAt: &past,
	}, now); !errors.Is(err, ErrAsyncTaskFileExpired) {
		t.Fatalf("expired err = %v, want ErrAsyncTaskFileExpired", err)
	}
	// 4) 成功且未过期 → 能打开
	future := now.Add(time.Hour)
	file, name, err := OpenAsyncTaskResultFile(entity.AsyncTask{
		Status: entity.AsyncTaskStatusSucceeded, FilePath: path, ExpiresAt: &future,
	}, now)
	if err != nil {
		t.Fatalf("succeeded err = %v, want nil", err)
	}
	defer func() { _ = file.Close() }()
	if name != "result.csv" {
		t.Fatalf("name = %q, want result.csv", name)
	}
	// 5) 行在但文件没了 → 也按过期处理（对外 410，不暴露内部路径）
	missing := filepath.Join(dir, "missing.csv")
	if _, _, err := OpenAsyncTaskResultFile(entity.AsyncTask{
		Status: entity.AsyncTaskStatusSucceeded, FilePath: missing, ExpiresAt: &future,
	}, now); !errors.Is(err, ErrAsyncTaskFileExpired) {
		t.Fatalf("missing file err = %v, want ErrAsyncTaskFileExpired", err)
	}
}

// 端到端：提交导出 → worker 执行 → 落盘 → 内容是脱敏后的 CSV。
func TestAsyncTaskExportEndToEndWritesMaskedCSV(t *testing.T) {
	db := newAsyncTaskTestDB(t)
	baseDir := t.TempDir()
	manager := newTestAsyncTaskManager(t, db, baseDir)

	// 造一条带手机号的用户（默认脱敏必须把它打码）。
	phone := "13812345678"
	if err := db.Create(&entity.User{
		Username: "alice", Phone: &phone, Balance: 1, GiftBalance: 2,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	task, err := manager.SubmitAsyncTask(entity.AsyncTaskTypeExport, ExportResourceUsers, "csv", true, `{"resource":"users"}`, "", entity.User{ID: 1, Username: "admin"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	manager.Start()
	defer manager.Stop()

	final := waitAsyncTaskTerminal(t, db, task.TaskID, 15*time.Second)
	if final.Status != entity.AsyncTaskStatusSucceeded {
		t.Fatalf("status = %q (%s), want succeeded", final.Status, final.ErrorMessage)
	}
	content, err := os.ReadFile(final.FilePath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "138****5678") {
		t.Fatalf("result CSV must contain masked phone 138****5678; got:\n%s", text)
	}
	if strings.Contains(text, "13812345678") {
		t.Fatalf("result CSV must not contain the raw phone; got:\n%s", text)
	}
	if !strings.Contains(final.Message, "已脱敏") {
		t.Fatalf("message = %q, want it to mention 已脱敏", final.Message)
	}
}

// 明文导出（显式 masked=false）里必须是原文，且结论要点出「未脱敏」。
func TestAsyncTaskExportEndToEndUnmaskedKeepsRawValue(t *testing.T) {
	db := newAsyncTaskTestDB(t)
	manager := newTestAsyncTaskManager(t, db, t.TempDir())
	phone := "13812345678"
	if err := db.Create(&entity.User{Username: "bob", Phone: &phone}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	task, err := manager.SubmitAsyncTask(entity.AsyncTaskTypeExport, ExportResourceUsers, "csv", false, `{"resource":"users"}`, "", entity.User{ID: 1, Username: "admin"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	manager.Start()
	defer manager.Stop()

	final := waitAsyncTaskTerminal(t, db, task.TaskID, 15*time.Second)
	if final.Status != entity.AsyncTaskStatusSucceeded {
		t.Fatalf("status = %q (%s), want succeeded", final.Status, final.ErrorMessage)
	}
	content, err := os.ReadFile(final.FilePath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if !strings.Contains(string(content), "13812345678") {
		t.Fatalf("unmasked export must contain the raw phone; got:\n%s", string(content))
	}
	if !strings.Contains(final.Message, "未脱敏") {
		t.Fatalf("message = %q, want it to mention 未脱敏", final.Message)
	}
}

// 默认脱敏必须在 **service 层**成立，不能只有 controller 层测。
//
// 这是变异验证逼出来的缺口：最初只有 controller 的
// TestAsyncTaskExportCreateDefaultsToMasked 覆盖「不传 masked → 脱敏」，
// 把 DefaultExportMasked 改成 false 时 service 层全绿、只红 controller 一条 ——
// 说明「导出脱敏」这条安全边界的行为断言是薄的。
//
// 因此这里用**不传 Masked（nil）**直接调 ExportCSVWithDB，断言：
//  1. 返回结果里 Masked 解析为 true；
//  2. 输出内容里手机号被打码（行为断言，不是只查字段）。
func TestAsyncTaskExportDefaultMaskedAtServiceLayer(t *testing.T) {
	db := newAsyncTaskTestDB(t)
	phone := "13812345678"
	if err := db.Create(&entity.User{Username: "carol", Phone: &phone}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	// 刻意**不传 Masked**（nil）——这就是「管理员没勾过」的真实形态。
	result, err := ExportCSVWithDB(db, ExportDataOptions{Resource: ExportResourceUsers})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !result.Masked {
		t.Fatal("Masked = false with nil option, want true (DefaultExportMasked)")
	}
	text := string(result.Content)
	if !strings.Contains(text, "138****5678") {
		t.Fatalf("default export must mask the phone; got:\n%s", text)
	}
	if strings.Contains(text, "13812345678") {
		t.Fatalf("default export must not leak the raw phone; got:\n%s", text)
	}
	// 显式取消脱敏 → 原文（证明开关两个方向都能动，不是「永远脱敏」的假实现）。
	unmasked := false
	result, err = ExportCSVWithDB(db, ExportDataOptions{Resource: ExportResourceUsers, Masked: &unmasked})
	if err != nil {
		t.Fatalf("unmasked export: %v", err)
	}
	if result.Masked {
		t.Fatal("explicit false must not be resolved to true")
	}
	if !strings.Contains(string(result.Content), "13812345678") {
		t.Fatalf("unmasked export must contain the raw phone; got:\n%s", string(result.Content))
	}
}

// GORM 脱敏开关的写入语义必须**逐层钉住**，不能只看「读回来对不对」。
//
// 背景（captain 提问）：`AsyncTask.Masked` 曾是 `default:true`，实测复现「传 false 入库变 true」。
// 改成 `default:false` 后要回答的关键问题是：
//
//	「传 false 时，INSERT 里到底带不带 masked 列？」
//	如果是「列被省略、靠 DB 默认值 false 兜住」，那正确就是**巧合**而非设计 ——
//	哪天 DB 默认值改了（或换到默认值不是 false 的方言），行为会静默翻转。
//
// 实测答案（DryRun 抓 SQL + 真库读回，两处独立）：
//   - `default:false` + 传 false → INSERT **带** masked 列，值为 false → 真库读回 false ✅
//   - `default:true`  + 传 false → INSERT **也带** masked 列，但**值被替换成 true** → 读回 true ❌
//
// 所以机制不是「省略列」，而是「GORM 用 tag 默认值替换了零值字段」。
// 这个用例把「列必须在 INSERT 里且值为 false」钉死，防止将来有人改回 default:true
// 或改成 `gorm:"-"` 之类导致写入语义变化。
func TestAsyncTaskMaskedColumnIsWrittenExplicitly(t *testing.T) {
	db := newAsyncTaskTestDB(t)

	// 用 DryRun 抓真实 SQL：不落库、不污染测试库，能直接看到列与绑定值。
	dryRun := db.Session(&gorm.Session{DryRun: true, NewDB: true})
	statement := dryRun.Create(&entity.AsyncTask{
		TaskID: "exp-dryrun00000001", Type: entity.AsyncTaskTypeExport,
		Status: entity.AsyncTaskStatusPending, Masked: false,
	}).Statement
	sql := statement.SQL.String()

	if !strings.Contains(sql, "`masked`") {
		t.Fatalf("INSERT must explicitly include the masked column (otherwise correctness would rely on the DB default): %s", sql)
	}
	// 绑定值里必须有一个 false —— 证明不是「列在但值被替换」。
	hasFalse := false
	for _, value := range statement.Vars {
		if asBool, ok := value.(bool); ok && !asBool {
			hasFalse = true
			break
		}
	}
	if !hasFalse {
		t.Fatalf("INSERT vars must contain the explicit false for masked, got %v", statement.Vars)
	}

	// 真库读回：黑盒层面的最终判据（上面是白盒，这里防「SQL 对但存储层改写」）。
	row := entity.AsyncTask{
		TaskID: "exp-dryrun-real0001", Type: entity.AsyncTaskTypeExport,
		Status: entity.AsyncTaskStatusPending, Masked: false,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var reloaded entity.AsyncTask
	if err := db.Where("task_id = ?", row.TaskID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Masked {
		t.Fatal("explicit Masked=false must survive a round-trip to the database")
	}

	// 反向：true 也必须如实写入（防止「反正都写 false」的坏实现）。
	rowTrue := entity.AsyncTask{
		TaskID: "exp-dryrun-real0002", Type: entity.AsyncTaskTypeExport,
		Status: entity.AsyncTaskStatusPending, Masked: true,
	}
	if err := db.Create(&rowTrue).Error; err != nil {
		t.Fatalf("create true: %v", err)
	}
	var reloadedTrue entity.AsyncTask
	if err := db.Where("task_id = ?", rowTrue.TaskID).First(&reloadedTrue).Error; err != nil {
		t.Fatalf("reload true: %v", err)
	}
	if !reloadedTrue.Masked {
		t.Fatal("explicit Masked=true must survive a round-trip to the database")
	}
}

// waitAsyncTaskTerminal 轮询到终态（超时即失败）。
func waitAsyncTaskTerminal(t *testing.T, db *gorm.DB, taskID string, timeout time.Duration) entity.AsyncTask {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		task := loadAsyncTask(t, db, taskID)
		if entity.AsyncTaskIsTerminal(task.Status) {
			return task
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %q stayed in %q past %s", taskID, task.Status, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
