package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"gorm.io/gorm"
)

// 异步任务框架（107 Q12 / 111 §4.7 / 121 §4.10，任务书 ①②⑤⑦）。
//
// # 三个不可协商的性质
//
//  1. **结果文件保留 1 小时**（111 §4.1「文件保留 1 小时」）。
//     清理是独立 goroutine（任务书 ⑤），间隔可配置，清理失败只记 SysWarn ——
//     清理是运维动作，绝不能因为删不掉一个文件就让主服务崩或让请求报错。
//
//  2. **任务可恢复**（任务书 ⑦）。进程重启后：
//     pending 行原样继续排队；
//     **崩溃遗留的 running 行会被翻回 pending 重跑** —— 这一条比 pending 更重要，
//     因为「进程被 kill 时正在跑的任务」是新进程里唯一没人记得的任务。
//     判据只能是「进程启动时不可能是 running」：这个进程还没开始执行任何任务，
//     表里所有 running 都是上一个进程的残骸。因此恢复不依赖 StartedAt 之类的启发式。
//
//  3. **只有终态才写结果过期时间**：排队中的任务没有文件可清，
//     提前写 ExpiresAt 会让清理逻辑与执行逻辑抢同一行。
//
// # 执行模型
//
// 提交 → 落库（pending）→ 唤醒 worker → worker 抢一行（pending → running，条件更新）
// → 执行执行器 → 写终态。并发上限默认 2（111 §4.7「同时运行的任务数 2 个，超过则排队」），
// 由信号量控制，不是靠数据库行锁 —— 行锁在 SQLite 单连接下会把自己锁死。
//
// 抢占用**条件 UPDATE**（`WHERE id = ? AND status = 'pending'`）而不是「先读后写」：
// 两个 worker 同时看到同一行 pending 时，只有一行的 RowsAffected=1，另一个自然放弃。
// 这与本仓 billing_records.request_id 的「先读后写在同一事务内」是同一思路。

// AsyncTaskDefaultRetention 是结果文件保留时长（111 §4.1：1 小时）。
const AsyncTaskDefaultRetention = time.Hour

// AsyncTaskDefaultTimeout 是单个任务的执行超时（111 §4.7：30 分钟，超时标记 failed）。
const AsyncTaskDefaultTimeout = 30 * time.Minute

// AsyncTaskDefaultConcurrency 是同时运行的任务数上限（111 §4.7：2 个，超过则排队）。
const AsyncTaskDefaultConcurrency = 2

// AsyncTaskDefaultCleanupInterval 是过期清理的默认间隔（任务书 ⑤：间隔可配置）。
const AsyncTaskDefaultCleanupInterval = 5 * time.Minute

// AsyncTaskTaskIDPrefix 是各类任务的对外 ID 前缀。
//
// 前缀让「拿到一个 ID 就知道是哪类任务」，也避免 exp-/imp- 撞号后误操作。
var AsyncTaskTaskIDPrefix = map[string]string{
	entity.AsyncTaskTypeImport:        "imp",
	entity.AsyncTaskTypeExport:        "exp",
	entity.AsyncTaskTypeBatchGrant:    "grn",
	entity.AsyncTaskTypeBatchRollback: "rbl",
}

// 异步任务相关错误。
var (
	// ErrAsyncTaskDBUnavailable 数据库未初始化 / 表结构未就绪。
	ErrAsyncTaskDBUnavailable = errors.New("异步任务数据库暂不可用")
	// ErrAsyncTaskNotFound 任务不存在。
	ErrAsyncTaskNotFound = errors.New("异步任务不存在")
	// ErrAsyncTaskFileExpired 结果文件已过期（HTTP 410）。
	ErrAsyncTaskFileExpired = errors.New("文件已过期，请重新导出")
	// ErrAsyncTaskNotReady 任务还没成功，没有可下载的文件。
	ErrAsyncTaskNotReady = errors.New("任务尚未完成，暂无可下载文件")
	// ErrAsyncTaskTypeUnsupported 任务类型没有注册执行器。
	ErrAsyncTaskTypeUnsupported = errors.New("该任务类型暂未启用（执行器未注册）")
)

// AsyncTaskExecutor 是一类任务的执行器。
//
// 入参 task 是**刚从库里抢到的行**（Status 已是 running）；执行器负责：
//   - 读 task.Params / SourcePath 干活；
//   - 把结果写进 FilePath / ErrorReportPath / 计数列；
//   - 返回 (结果摘要, 错误)。
//
// 执行器**不需要**自己写终态：状态机由框架统一提交（见 runAsyncTask），
// 否则每个执行器都要重复一遍「成功写 succeeded + ExpiresAt、失败写 failed + ErrorMessage」，
// 漏写一处就是一条永远 running 的行。
type AsyncTaskExecutor func(ctx context.Context, db *gorm.DB, task entity.AsyncTask) (AsyncTaskOutcome, error)

// AsyncTaskOutcome 是执行器的产出（框架据此写终态）。
type AsyncTaskOutcome struct {
	Total     int
	Processed int
	Succeeded int
	Skipped   int
	Failed    int
	// FilePath / ErrorReportPath 是执行器落盘后的路径（空 = 该任务不产出文件）。
	FilePath        string
	ErrorReportPath string
	// Message 是给用户看的一句话结论。
	Message string
	// ErrorMessage 是失败原因（框架也会往里写；执行器一般不用填）。
	ErrorMessage string
}

// AsyncTaskManager 是异步任务的进程内管理器。
//
// 一个进程只需要一个（它自己持有 worker 与清理 goroutine）。
// 用 NewAsyncTaskManager 构造，Start 才开始跑；不 Start 时 Submit 只会落库排队，
// 这让单测可以只验证「提交写了什么」而不引入并发。
type AsyncTaskManager struct {
	db *gorm.DB

	// BaseDir 是结果文件与源文件的根目录（111 Q12-2：服务器临时目录）。
	BaseDir string
	// Retention 是结果文件保留时长（默认 1 小时）。
	Retention time.Duration
	// Timeout 是单任务执行超时（默认 30 分钟）。
	Timeout time.Duration
	// Concurrency 是同时执行的任务数（默认 2）。
	Concurrency int
	// CleanupInterval 是过期清理间隔（默认 5 分钟）。
	CleanupInterval time.Duration

	mu        sync.Mutex
	executors map[string]AsyncTaskExecutor
	running   int
	closed    bool

	wake chan struct{}
	stop chan struct{}
	done chan struct{}
	// cleanupStop / cleanupDone 与 worker 分开：清理可以单独启停（测试里要单独跑一轮）。
	cleanupStop chan struct{}
	cleanupDone chan struct{}
	startOnce   sync.Once
	stopOnce    sync.Once
}

// NewAsyncTaskManager 构造管理器（未 Start，不会自己跑）。
func NewAsyncTaskManager(db *gorm.DB, baseDir string) *AsyncTaskManager {
	return &AsyncTaskManager{
		db:              db,
		BaseDir:         baseDir,
		Retention:       AsyncTaskDefaultRetention,
		Timeout:         AsyncTaskDefaultTimeout,
		Concurrency:     AsyncTaskDefaultConcurrency,
		CleanupInterval: AsyncTaskDefaultCleanupInterval,
		executors:       make(map[string]AsyncTaskExecutor, 4),
		wake:            make(chan struct{}, 1),
		stop:            make(chan struct{}),
		done:            make(chan struct{}),
		cleanupStop:     make(chan struct{}),
		cleanupDone:     make(chan struct{}),
	}
}

// RegisterExecutor 注册某类任务的执行器（重复注册后者覆盖前者）。
//
// 批量赠送 / 批量回滚由 u-grant 通过它接入（任务书 ①：那两个 type 只定义常量，
// 执行器由资金模块提供）。未注册时 Submit 不会失败（任务仍可排队），
// 但执行会以 failed + ErrAsyncTaskTypeUnsupported 结束 —— 这样即使接线顺序不对，
// 用户看到的是「任务失败：执行器未注册」，而不是任务永远 pending。
func (m *AsyncTaskManager) RegisterExecutor(taskType string, executor AsyncTaskExecutor) {
	if m == nil || executor == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executors[strings.TrimSpace(taskType)] = executor
}

// Start 启动 worker 与清理 goroutine，并执行一次崩溃恢复（任务书 ⑤⑦）。
//
// 幂等：重复调用只有第一次生效（startOnce）。恢复失败只记 SysError 并继续启动 ——
// 启动期一个任务捡不回来，不该让整个服务起不来。
func (m *AsyncTaskManager) Start() {
	if m == nil {
		return
	}
	m.startOnce.Do(func() {
		if err := m.RecoverInterruptedTasks(); err != nil {
			logger.SysError("异步任务恢复失败（不影响服务启动）：" + err.Error())
		}
		go m.workerLoop()
		go m.cleanupLoop()
	})
}

// Stop 停掉所有 goroutine（幂等）。
func (m *AsyncTaskManager) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		close(m.stop)
		select {
		case <-m.done:
		case <-time.After(5 * time.Second):
			// worker 卡在执行器里（执行器自己有 Timeout 兜底），这里不无限等。
		}
		close(m.cleanupStop)
		select {
		case <-m.cleanupDone:
		case <-time.After(5 * time.Second):
		}
	})
}

// RecoverInterruptedTasks 把崩溃遗留的 running 行翻回 pending（任务书 ⑦）。
//
// 判据不是 StartedAt，而是**进程启动时不可能有本进程的 running 任务**：
// 此刻本进程还没执行任何任务，表里所有 running 都是上一个进程的残骸。
// 因此这里无条件翻（回到 pending 后会被 worker 重新捡起）。
//
// 同时把仍在 pending 的任务数唤醒一次 worker —— 没有这一步，
// 「重启后 pending 任务要能重新捡起来」就要等下一次 Submit 才发生。
func (m *AsyncTaskManager) RecoverInterruptedTasks() error {
	if m == nil || m.db == nil {
		return ErrAsyncTaskDBUnavailable
	}
	if !m.db.Migrator().HasTable(entity.TableNameAsyncTask) {
		return ErrAsyncTaskDBUnavailable
	}
	// 条件更新用原始 SQL 语义：只翻 running，不碰终态。
	result := m.asyncDB().Model(&entity.AsyncTask{}).
		Where("status = ?", entity.AsyncTaskStatusRunning).
		Updates(map[string]any{
			"status":     entity.AsyncTaskStatusPending,
			"message":    "进程重启后重新排队",
			"updated_at": time.Now(),
		})
	if result.Error != nil {
		return fmt.Errorf("恢复中断任务失败: %w", result.Error)
	}
	// 仍有 pending → 主动唤醒一次 worker。
	var pending int64
	if err := m.asyncDB().Model(&entity.AsyncTask{}).
		Where("status = ?", entity.AsyncTaskStatusPending).Count(&pending).Error; err != nil {
		return fmt.Errorf("统计待执行任务失败: %w", err)
	}
	if pending > 0 {
		m.signalWake()
	}
	if result.RowsAffected > 0 {
		logger.SysLog(fmt.Sprintf("异步任务恢复：%d 个中断任务已重新排队", result.RowsAffected))
	}
	return nil
}

// asyncDB 返回一个「干净」的会话（跨 model 复用句柄必须先清 Statement，
// 否则 GORM 会用上一个 model 的 schema 去 reflect，见 adminLoginDB 的注释）。
//
// 同时套一层低优先级上下文：导入导出是运维动作，不该抢答题链路的数据库连接
// （与黑名单管理页同一口径，见 controller 的 requireBlacklistDB）。
func (m *AsyncTaskManager) asyncDB() *gorm.DB {
	if m == nil || m.db == nil {
		return nil
	}
	return asyncTaskDBPriority(m.db.Session(&gorm.Session{NewDB: true}))
}

// SubmitAsyncTask 提交一个任务（只落库，不执行）。
//
// 返回的任务 Status 是 pending。真正执行由 worker 抢单，调用方拿 TaskID 去轮询状态。
func (m *AsyncTaskManager) SubmitAsyncTask(taskType, resource, format string, masked bool, params string, sourcePath string, actor entity.User) (entity.AsyncTask, error) {
	if m == nil || m.db == nil {
		return entity.AsyncTask{}, ErrAsyncTaskDBUnavailable
	}
	if !m.db.Migrator().HasTable(entity.TableNameAsyncTask) {
		return entity.AsyncTask{}, ErrAsyncTaskDBUnavailable
	}
	prefix := AsyncTaskTaskIDPrefix[strings.TrimSpace(taskType)]
	if prefix == "" {
		return entity.AsyncTask{}, fmt.Errorf("%w: %q", ErrAsyncTaskTypeUnsupported, taskType)
	}
	taskID, err := newAsyncTaskID(prefix)
	if err != nil {
		return entity.AsyncTask{}, err
	}
	task := entity.AsyncTask{
		TaskID:        taskID,
		Type:          strings.TrimSpace(taskType),
		Resource:      strings.TrimSpace(resource),
		Format:        strings.TrimSpace(format),
		Status:        entity.AsyncTaskStatusPending,
		Masked:        masked,
		ActorUserID:   actor.ID,
		ActorUsername: actor.Username,
		Params:        params,
		SourcePath:    sourcePath,
		// ExpiresAt 在提交时也写一份（完成时会改写为「完成时刻 + 保留时长」）：
		// 卡死在 pending 的任务（执行器没注册 / 源文件丢了）没有兜底过期就永远清不掉。
		// 留一个执行超时的窗口，避免把还在排队的任务过早判过期。
		ExpiresAt: asyncTaskTimePtr(time.Now().Add(m.Timeout + m.Retention)),
	}
	if err := m.asyncDB().Create(&task).Error; err != nil {
		return entity.AsyncTask{}, fmt.Errorf("提交异步任务失败: %w", err)
	}
	// ⚠️ 这里**刻意不做**「Create 之后再 Update masked」的补写。
	//
	// 本仓有两次实测事故（entity/blacklist.go 的 Enabled、review_rebate.go 的 Enabled）：
	// 带 `default:true` 标签时 GORM 会把「等于默认值的字段」从 INSERT 里省略，
	// 于是显式传 false 会被静默写成 true。脱敏开关是安全边界，
	// 处置是**不依赖数据库默认值**：entity.AsyncTask.Masked 不带 `default:true` 标签，
	// 默认值只在 Go 侧表达（DefaultExportMasked / resolveExportMasked）。
	// 回归钉子是 TestAsyncTaskExportEndToEndUnmaskedKeepsRawValue ——
	// 它会同时断言「返回值 Masked=false」与「结果文件里是原文」。
	m.signalWake()
	return task, nil
}

// newAsyncTaskID 生成对外任务 ID：<prefix>-<16 位随机十六进制>。
//
// 不用自增 ID：自增可枚举，猜别人的导出文件会变得容易（下载接口只校验管理员，
// 但 ID 可枚举仍然放大了「误点别人结果」的风险）。随机源失败直接报错，
// 绝不退化成时间戳（时间戳可枚举，等于白做）。
func newAsyncTaskID(prefix string) (string, error) {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("生成任务 ID 失败: %w", err)
	}
	return prefix + "-" + hex.EncodeToString(buffer), nil
}

// asyncTaskTimePtr 返回时间指针（GORM 可空列）。
func asyncTaskTimePtr(value time.Time) *time.Time { return &value }

// signalWake 非阻塞唤醒 worker（channel 容量 1，重复唤醒无副作用）。
func (m *AsyncTaskManager) signalWake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// workerLoop 是执行循环。
//
// 循环结构：检查并发 → 抢一行 → 起 goroutine 执行。抢不到就阻塞等唤醒/停止，
// 不做 busy loop（空转会把 SQLite 单连接占满）。
func (m *AsyncTaskManager) workerLoop() {
	defer close(m.done)
	for {
		select {
		case <-m.stop:
			return
		default:
		}
		if !m.acquireSlot() {
			// 并发已满：等有任务结束再试（不占 CPU）。
			select {
			case <-m.stop:
				return
			case <-m.wake:
				continue
			case <-time.After(time.Second):
				continue
			}
		}
		task, claimed, err := m.claimNextTask()
		if err != nil {
			m.releaseSlot()
			logger.SysWarn("异步任务抢单失败：" + err.Error())
			select {
			case <-m.stop:
				return
			case <-time.After(time.Second):
				continue
			}
		}
		if !claimed {
			m.releaseSlot()
			// 没有待执行任务 → 等唤醒。
			select {
			case <-m.stop:
				return
			case <-m.wake:
			}
			continue
		}
		go func(claimedTask entity.AsyncTask) {
			defer m.releaseSlot()
			m.runAsyncTask(claimedTask)
		}(task)
	}
}

// acquireSlot 占一个并发名额（满则返回 false）。
func (m *AsyncTaskManager) acquireSlot() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.running >= m.Concurrency {
		return false
	}
	m.running++
	return true
}

// releaseSlot 释放并发名额。
func (m *AsyncTaskManager) releaseSlot() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running > 0 {
		m.running--
	}
}

// claimNextTask 抢一行 pending 任务。
//
// 「读一行 → 条件更新」两步，而不是 SELECT ... FOR UPDATE：
// SQLite 下没有行锁，且 glebarez/sqlite 的 maxOpenConns 常为 1，
// 长事务会把自己的写请求堵在门外。条件更新的 RowsAffected 同样是原子判据。
func (m *AsyncTaskManager) claimNextTask() (entity.AsyncTask, bool, error) {
	if m == nil || m.db == nil {
		return entity.AsyncTask{}, false, ErrAsyncTaskDBUnavailable
	}
	var candidate entity.AsyncTask
	err := m.asyncDB().
		Where("status = ?", entity.AsyncTaskStatusPending).
		Order("id asc").
		First(&candidate).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.AsyncTask{}, false, nil
		}
		return entity.AsyncTask{}, false, err
	}
	now := time.Now()
	result := m.asyncDB().Model(&entity.AsyncTask{}).
		Where("id = ? AND status = ?", candidate.ID, entity.AsyncTaskStatusPending).
		Updates(map[string]any{
			"status":     entity.AsyncTaskStatusRunning,
			"started_at": now,
			"updated_at": now,
		})
	if result.Error != nil {
		return entity.AsyncTask{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		// 被另一个 worker 抢走了。
		return entity.AsyncTask{}, false, nil
	}
	candidate.Status = entity.AsyncTaskStatusRunning
	candidate.StartedAt = &now
	return candidate, true, nil
}

// runAsyncTask 执行一个已抢占的任务并写终态。
//
// 三条纪律：
//  1. 执行器 panic 不能带崩进程（recover 包一层，写 failed）；
//  2. 执行超时（Timeout）写 failed，**不重试** —— 30 分钟还没跑完的任务重试只会再占 30 分钟；
//  3. 写终态失败只记 SysError（任务结果本身已经在库里/盘上了，状态写不进去是运维问题，
//     不该让 worker 崩溃）。
func (m *AsyncTaskManager) runAsyncTask(task entity.AsyncTask) {
	executor, ok := m.executorOf(task.Type)
	if !ok {
		m.finishAsyncTask(task.ID, entity.AsyncTaskStatusFailed, AsyncTaskOutcome{
			ErrorMessage: ErrAsyncTaskTypeUnsupported.Error(),
		})
		return
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = AsyncTaskDefaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	outcome, err := runAsyncExecutorSafely(ctx, m.asyncDB(), task, executor)
	if err != nil {
		message := "任务执行失败"
		if errors.Is(err, context.DeadlineExceeded) {
			message = fmt.Sprintf("任务执行超过 %s，已标记失败", timeout)
		}
		outcome.ErrorMessage = message + "：" + err.Error()
		m.finishAsyncTask(task.ID, entity.AsyncTaskStatusFailed, outcome)
		return
	}
	m.finishAsyncTask(task.ID, entity.AsyncTaskStatusSucceeded, outcome)
}

// runAsyncExecutorSafely 调用执行器并兜住 panic。
func runAsyncExecutorSafely(ctx context.Context, db *gorm.DB, task entity.AsyncTask, executor AsyncTaskExecutor) (outcome AsyncTaskOutcome, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("执行器 panic: %v", recovered)
		}
	}()
	return executor(ctx, db, task)
}

// executorOf 取执行器（带读锁）。
func (m *AsyncTaskManager) executorOf(taskType string) (AsyncTaskExecutor, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	executor, ok := m.executors[strings.TrimSpace(taskType)]
	return executor, ok
}

// finishAsyncTask 写终态。
//
// ExpiresAt 只在成功时设（失败任务没有结果文件可下载，但错误报告也要过期清理 ——
// 因此失败也设 ExpiresAt，让清理逻辑把错误报告一起回收）。
func (m *AsyncTaskManager) finishAsyncTask(id int64, status string, outcome AsyncTaskOutcome) {
	if m == nil || m.db == nil {
		return
	}
	now := time.Now()
	updates := map[string]any{
		"status":            status,
		"processed":         coalesceAsyncCount(outcome.Processed, outcome.Total),
		"total":             coalesceAsyncCount(outcome.Total, outcome.Processed),
		"succeeded":         outcome.Succeeded,
		"skipped":           outcome.Skipped,
		"failed":            outcome.Failed,
		"file_path":         outcome.FilePath,
		"error_report_path": outcome.ErrorReportPath,
		"message":           truncateAsyncText(outcome.Message, 512),
		"error_message":     truncateAsyncText(outcome.ErrorMessage, 1024),
		"finished_at":       now,
		"updated_at":        now,
		"expires_at":        now.Add(m.retentionOrDefault()),
	}
	if err := m.asyncDB().Model(&entity.AsyncTask{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		logger.SysError(fmt.Sprintf("写入异步任务 %d 终态失败（结果已产出）：%v", id, err))
	}
}

// coalesceAsyncCount 处理「执行器只填了 Total 或只填了 Processed」的情况。
func coalesceAsyncCount(primary, fallback int) int {
	if primary > 0 {
		return primary
	}
	return fallback
}

// truncateAsyncText 按字符截断（不按字节：中文会被切出半个字符）。
func truncateAsyncText(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// retentionOrDefault 返回保留时长（非法值回落默认 1 小时）。
func (m *AsyncTaskManager) retentionOrDefault() time.Duration {
	if m == nil || m.Retention <= 0 {
		return AsyncTaskDefaultRetention
	}
	return m.Retention
}

// cleanupLoop 是过期清理 goroutine（任务书 ⑤）。
//
// 间隔可配置（CleanupInterval），每次 tick 跑一轮 CleanupExpiredTasks。
// 清理失败只记 SysWarn —— 与幂等记录清理（idempotency.go）同一口径。
func (m *AsyncTaskManager) cleanupLoop() {
	defer close(m.cleanupDone)
	interval := m.CleanupInterval
	if interval <= 0 {
		interval = AsyncTaskDefaultCleanupInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.cleanupStop:
			return
		case now := <-ticker.C:
			if _, err := m.CleanupExpiredTasks(now); err != nil {
				logger.SysWarn("清理过期异步任务失败：" + err.Error())
			}
		}
	}
}

// CleanupExpiredTasks 删除超过 ExpiresAt 的任务行与它们的文件，返回删除条数。
//
// 三条判据（都会让「误删」或「永不删」的 bug 显现）：
//
//  1. **只删终态**（succeeded / failed）：running 任务可能正被执行器写文件，
//     把行删掉会让执行器最后写终态时打到 0 行、文件也没人清；
//     pending 任务的 ExpiresAt 是「最大的兜底时间」，用它在执行前就被删掉会让
//     「重启后重新捡起」失效 —— 但 pending 又有兜底过期的需求
//     （源文件丢了就永远 pending）。折中：pending 的兜底过期**远晚于**执行窗口
//     （提交时 ExpiresAt = now + Timeout + Retention），因此这里通过
//     「expires_at <= now」这一个条件既清得了卡死的 pending，也不会误删刚排队的。
//     为了让 running 不被误删，只排除 running 一种状态。
//  2. **未过期的一条都不动**（任务书必写测试：任务过期 1 小时后被清理，未过期的不会被删）；
//  3. **文件删失败不回滚行删除**：行是权威记录，文件残留只是磁盘占用；
//     反过来（先删文件后删行）一旦中间失败，就会留下「行说文件在、其实没了」的坏状态。
func (m *AsyncTaskManager) CleanupExpiredTasks(now time.Time) (int, error) {
	if m == nil || m.db == nil {
		return 0, ErrAsyncTaskDBUnavailable
	}
	if !m.db.Migrator().HasTable(entity.TableNameAsyncTask) {
		return 0, ErrAsyncTaskDBUnavailable
	}
	expired := make([]entity.AsyncTask, 0, 16)
	if err := m.asyncDB().
		Where("expires_at IS NOT NULL AND expires_at <= ? AND status <> ?",
			now, entity.AsyncTaskStatusRunning).
		Find(&expired).Error; err != nil {
		return 0, fmt.Errorf("查询过期任务失败: %w", err)
	}
	if len(expired) == 0 {
		return 0, nil
	}
	ids := make([]int64, 0, len(expired))
	for _, task := range expired {
		ids = append(ids, task.ID)
	}
	if err := m.asyncDB().Where("id IN ?", ids).Delete(&entity.AsyncTask{}).Error; err != nil {
		return 0, fmt.Errorf("删除过期任务失败: %w", err)
	}
	// 行删成功后清文件；失败只记日志（行是权威记录，文件残留只是占盘）。
	for _, task := range expired {
		m.removeTaskFiles(task)
	}
	return len(expired), nil
}

// removeTaskFiles 删除任务关联的源文件 / 结果文件 / 错误报告。
//
// 只删 BaseDir 之下的路径：Task.SourcePath 来自上传、FilePath 来自执行器，
// 两者都是「路径字符串」；一旦有人（或将来某个 bug）写进绝对路径到别处，
// 无脑 os.Remove 就成了任意文件删除。这里做一次前缀校验，路径不在 BaseDir 下就跳过。
func (m *AsyncTaskManager) removeTaskFiles(task entity.AsyncTask) {
	for _, path := range []string{task.SourcePath, task.FilePath, task.ErrorReportPath} {
		m.removeTaskFile(path)
	}
}

// removeTaskFile 删除单个文件（带 BaseDir 前缀校验）。
func (m *AsyncTaskManager) removeTaskFile(path string) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return
	}
	base := strings.TrimSpace(m.BaseDir)
	if base == "" {
		return
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return
	}
	absPath, err := filepath.Abs(trimmed)
	if err != nil {
		return
	}
	if absPath != absBase && !strings.HasPrefix(absPath, absBase+string(os.PathSeparator)) {
		logger.SysWarn("跳过清理 BaseDir 之外的文件：" + absPath)
		return
	}
	if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
		logger.SysWarn("删除任务文件失败（不阻断清理）：" + err.Error())
	}
}

// GetAsyncTaskWithDB 按 taskId 查任务（管理页与轮询接口用）。
func GetAsyncTaskWithDB(db *gorm.DB, taskID string) (entity.AsyncTask, error) {
	if db == nil {
		return entity.AsyncTask{}, ErrAsyncTaskDBUnavailable
	}
	trimmed := strings.TrimSpace(taskID)
	if trimmed == "" {
		return entity.AsyncTask{}, ErrAsyncTaskNotFound
	}
	var task entity.AsyncTask
	err := db.Session(&gorm.Session{NewDB: true}).
		Where("task_id = ?", trimmed).First(&task).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return entity.AsyncTask{}, ErrAsyncTaskNotFound
		}
		return entity.AsyncTask{}, err
	}
	return task, nil
}

// AsyncTaskListQuery 是任务列表的筛选条件。
type AsyncTaskListQuery struct {
	Type   string
	Status string
	// Resource 按资源筛（批量赠送类为空）。
	Resource string
}

// ListAsyncTasksWithDB 返回任务列表（id 倒序，新的在前）。
func ListAsyncTasksWithDB(db *gorm.DB, query AsyncTaskListQuery, page, pageSize int) ([]entity.AsyncTask, int64, error) {
	if db == nil {
		return nil, 0, ErrAsyncTaskDBUnavailable
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	session := func() *gorm.DB {
		tx := db.Session(&gorm.Session{NewDB: true}).Model(&entity.AsyncTask{})
		if value := strings.TrimSpace(query.Type); value != "" {
			tx = tx.Where("type = ?", value)
		}
		if value := strings.TrimSpace(query.Status); value != "" {
			tx = tx.Where("status = ?", value)
		}
		if value := strings.TrimSpace(query.Resource); value != "" {
			tx = tx.Where("resource = ?", value)
		}
		return tx
	}
	var total int64
	if err := session().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := make([]entity.AsyncTask, 0, pageSize)
	if err := session().Order("id desc").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// OpenAsyncTaskResultFile 打开任务的结果文件，供下载接口流式返回。
//
// 为什么不在 controller 里直接 os.Open：
// 过期判断（ExpiresAt）与「文件在不在」必须在一个地方，
// 否则「库里说没过期、盘上已经没了」会以 500 的形式暴露给用户。
// 这里把三种情况分别映射成三个错误：过期（410）/ 未完成（409）/ 文件缺失（500）。
func OpenAsyncTaskResultFile(task entity.AsyncTask, now time.Time) (*os.File, string, error) {
	if !entity.AsyncTaskIsTerminal(task.Status) {
		return nil, "", ErrAsyncTaskNotReady
	}
	if task.Status == entity.AsyncTaskStatusFailed {
		// 失败任务也可能有错误报告，但没有「结果文件」。
		return nil, "", ErrAsyncTaskNotReady
	}
	if task.ExpiresAt != nil && !now.Before(*task.ExpiresAt) {
		return nil, "", ErrAsyncTaskFileExpired
	}
	path := strings.TrimSpace(task.FilePath)
	if path == "" {
		return nil, "", ErrAsyncTaskNotReady
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 行还在但文件没了：只可能是过期清理删了文件却没删行，
			// 对外一律按「已过期」处理（让用户重新导出），不要暴露内部路径。
			return nil, "", ErrAsyncTaskFileExpired
		}
		return nil, "", err
	}
	return file, filepath.Base(path), nil
}

// asyncTaskDBPriority 给异步任务读写套一层低优先级上下文：
// 导入导出是运维动作，不该抢答题链路的数据库连接（与黑名单管理页同口径）。
func asyncTaskDBPriority(db *gorm.DB) *gorm.DB {
	if db == nil {
		return nil
	}
	return db.WithContext(dbqueue.WithPriority(context.Background(), dbqueue.PriorityLow))
}

// ─────────────────────── 内建执行器（export / import） ───────────────────────

// RegisterDefaultAsyncTaskExecutors 注册本模块自带的两类执行器（export / import）。
//
// 批量赠送 / 批量回滚**不在**这里：它们由资金模块通过 RegisterExecutor 注册
// （任务书 ①：那两个 type 只定义常量）。这样两个模块可以各自独立改动，
// 也不会让异步任务框架反向依赖钱包记账逻辑。
//
// 幂等：重复调用覆盖同名执行器（与 RegisterExecutor 语义一致）。
func (m *AsyncTaskManager) RegisterDefaultAsyncTaskExecutors() {
	if m == nil {
		return
	}
	m.RegisterExecutor(entity.AsyncTaskTypeExport, m.executeExportTask)
	m.RegisterExecutor(entity.AsyncTaskTypeImport, m.executeImportTask)
}

// asyncTaskExportParams 是导出任务的 Params 结构（与 controller 提交时写的 JSON 对齐）。
type asyncTaskExportParams struct {
	Resource string `json:"resource"`
	Format   string `json:"format"`
	Masked   *bool  `json:"masked"`
	Limit    int    `json:"limit"`
}

// asyncTaskImportParams 是导入任务的 Params 结构。
type asyncTaskImportParams struct {
	Resource            string `json:"resource"`
	DryRun              bool   `json:"dryRun"`
	ConfirmWeakPassword bool   `json:"confirmWeakPassword"`
	Filename            string `json:"filename"`
}

// executeExportTask 是导出执行器：生成 CSV → 落盘到 BaseDir/exports → 返回路径与计数。
//
// 为什么用任务行里的 Masked（而不是 params 里的）：任务行上的 Masked 是**提交时刻**
// 的审计字段，params 是执行参数 —— 两者应当一致，但若将来某处只改了一个，
// 以任务行（用户可见、审计可查）为准更安全。
func (m *AsyncTaskManager) executeExportTask(_ context.Context, db *gorm.DB, task entity.AsyncTask) (AsyncTaskOutcome, error) {
	var params asyncTaskExportParams
	if strings.TrimSpace(task.Params) != "" {
		if err := json.Unmarshal([]byte(task.Params), &params); err != nil {
			return AsyncTaskOutcome{}, fmt.Errorf("解析任务参数失败: %w", err)
		}
	}
	resource := strings.TrimSpace(params.Resource)
	if resource == "" {
		resource = task.Resource
	}
	masked := task.Masked
	result, err := ExportCSVWithDB(db, ExportDataOptions{
		Resource: resource,
		Masked:   &masked,
		Limit:    params.Limit,
	})
	if err != nil {
		return AsyncTaskOutcome{}, err
	}
	path, writeErr := m.writeAsyncResultFile(task.TaskID, "exports", resource, result.Content)
	if writeErr != nil {
		return AsyncTaskOutcome{}, writeErr
	}
	message := fmt.Sprintf("导出完成，共 %d 行", result.Rows)
	if result.Masked {
		message += "（已脱敏）"
	} else {
		// 明文导出必须在用户可见结论里点出来：这是唯一会让运营事后知道
		// 「这个文件里有明文手机号」的地方。
		message += "（未脱敏，含明文敏感字段）"
	}
	return AsyncTaskOutcome{
		Total:     result.Rows,
		Processed: result.Rows,
		Succeeded: result.Rows,
		FilePath:  path,
		Message:   message,
	}, nil
}

// executeImportTask 是导入执行器：读源文件 → ImportDataWithDB → 写错误报告 → 返回计数。
//
// 三个与导出不同的点：
//
//  1. 源文件必须存在（进程重启后要能重跑，所以文件是落盘的，不是内存里的）；
//  2. DryRun 由 params 决定（预览任务不落库，但仍产出同样的报告）；
//  3. 错误报告即使「全部成功」也要清空 ErrorReportPath 列 ——
//     否则重跑一个曾经失败的任务时会残留上一轮的报告路径。
func (m *AsyncTaskManager) executeImportTask(_ context.Context, db *gorm.DB, task entity.AsyncTask) (AsyncTaskOutcome, error) {
	var params asyncTaskImportParams
	if strings.TrimSpace(task.Params) != "" {
		if err := json.Unmarshal([]byte(task.Params), &params); err != nil {
			return AsyncTaskOutcome{}, fmt.Errorf("解析任务参数失败: %w", err)
		}
	}
	resource := strings.TrimSpace(params.Resource)
	if resource == "" {
		resource = task.Resource
	}
	sourcePath := strings.TrimSpace(task.SourcePath)
	if sourcePath == "" {
		return AsyncTaskOutcome{}, fmt.Errorf("导入任务缺少源文件路径，请重新上传")
	}
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return AsyncTaskOutcome{}, fmt.Errorf("源文件已被清理，请重新上传")
		}
		return AsyncTaskOutcome{}, fmt.Errorf("读取源文件失败: %w", err)
	}
	// Actor 从任务行还原：导入要写 created_by / register_source，
	// 不能因为进程重启就变成「系统导入」。
	actor := entity.User{ID: task.ActorUserID, Username: task.ActorUsername}
	result, err := ImportDataWithDB(db, ImportDataOptions{
		Resource:            resource,
		Format:              task.Format,
		Filename:            params.Filename,
		Content:             content,
		Actor:               actor,
		DryRun:              params.DryRun,
		ConfirmWeakPassword: params.ConfirmWeakPassword,
	})
	if err != nil {
		return AsyncTaskOutcome{}, err
	}
	outcome := AsyncTaskOutcome{
		Total:     result.Total,
		Processed: result.Total,
		Succeeded: result.Succeeded,
		Skipped:   result.Skipped,
		Failed:    result.Failed,
		Message: fmt.Sprintf("导入完成：成功 %d 行，跳过 %d 行，失败 %d 行",
			result.Succeeded, result.Skipped, result.Failed),
	}
	if params.DryRun {
		outcome.Message = "预览完成（未落库）：" + outcome.Message
	}
	if len(result.ErrorReport) > 0 {
		path, writeErr := m.writeAsyncResultFile(task.TaskID, "reports", "errors", result.ErrorReport)
		if writeErr != nil {
			// 报告写失败不算任务失败：数据已经导进去了，报告只是为了方便修文件。
			logger.SysWarn("写入导入错误报告失败（导入结果已生效）：" + writeErr.Error())
		} else {
			outcome.ErrorReportPath = path
		}
	}
	return outcome, nil
}

// writeAsyncResultFile 把结果写到 BaseDir/<subdir>/<taskId>-<name>.<ext> 并返回绝对路径。
//
// 目录权限 0o700、文件 0o600：导出文件含业务数据（不脱敏时是明文手机号），
// 同机上的其它用户可以读文件系统时，权限是最后一道防线。
func (m *AsyncTaskManager) writeAsyncResultFile(taskID, subdir, name string, content []byte) (string, error) {
	base := strings.TrimSpace(m.BaseDir)
	if base == "" {
		return "", fmt.Errorf("未配置异步任务目录")
	}
	dir := filepath.Join(base, subdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建结果目录失败: %w", err)
	}
	filename := fmt.Sprintf("%s-%s.csv", sanitizeAsyncTaskID(taskID), sanitizeAsyncTaskName(name))
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return "", fmt.Errorf("写入结果文件失败: %w", err)
	}
	return path, nil
}

// sanitizeAsyncTaskID 只保留字母数字与连字符（任务 ID 进文件名，防止路径穿越）。
func sanitizeAsyncTaskID(value string) string {
	var builder strings.Builder
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-', ch == '_':
			builder.WriteRune(ch)
		}
	}
	result := builder.String()
	if result == "" {
		return "task"
	}
	return result
}

// sanitizeAsyncTaskName 同上（资源名进文件名）。
func sanitizeAsyncTaskName(value string) string {
	sanitized := sanitizeAsyncTaskID(value)
	if sanitized == "task" && strings.TrimSpace(value) != "task" {
		return "result"
	}
	return sanitized
}
