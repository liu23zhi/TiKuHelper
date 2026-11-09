package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

// 敏感词打标的**旁路**实现。
//
// 设计红线（需求原文：绝不影响正常的答题流程）：
//  1. 不阻塞 —— 检测在独立 goroutine 里跑，答题响应先返回，主链路不等它；
//  2. 不失败 —— 检测任何错误都只在 goroutine 内部记日志，不冒泡到答题；
//  3. 不改结构 —— 只往 logs 表的四列独立字段写标记，返回给调用方的
//     SearchResponse 一个字段都不动。
//
// 所以这里刻意**不**把检测做进 Search 的返回路径：调用方拿到的是
// 「原样答案 + 原样响应体」，敏感词结论只在库里，由管理端/面板按需读取。
const (
	// sensitiveWordAttachLogIDTimeout 是旁路任务等审计行主键的上限。
	//
	// 审计行在答题返回前同步写入，正常是微秒级；给 30 秒是为了容忍
	// 计费重试排队（落账失败会走异步重试队列），超时则放弃打标 ——
	// 放弃一次打标远好过一个 goroutine 长期悬挂。
	sensitiveWordAttachLogIDTimeout = 30 * time.Second

	// sensitiveWordCheckTimeout 是单次检测（题目 + 答案两次外呼）的总预算。
	sensitiveWordCheckTimeout = 20 * time.Second
)

// sensitiveWordCheck 是一次请求的旁路打标任务句柄。
//
// 生命周期：答题主链路创建它（只做内存拷贝，不读库不发请求）→ 审计行写完后
// 把主键交给它 → 它在自己的 goroutine 里检测并回写。
type sensitiveWordCheck struct {
	logID   chan int32
	release func()
}

// defaultSensitiveWordDetectorBuilder 构造生产检测器。
//
// 抽成变量是为了让测试能把外呼指向 httptest（含「超时 5 秒」「返回 500」两种故障注入），
// 而不必改开关以外的生产逻辑。生产值就是 service.BuildSensitiveWordDetectorWithDB：
// 开关关闭或无可用密钥时返回 nil。
var defaultSensitiveWordDetectorBuilder = service.BuildSensitiveWordDetectorWithDB

// sensitiveWordClock 提供检测时间戳，测试可注入固定值。
var sensitiveWordClock = func() time.Time { return time.Now().In(logger.ShanghaiLocation()) }

// startSensitiveWordCheck 启动旁路打标。
//
// 关键：本函数只做内存操作（读 req/resp 的字段、起一个 goroutine），
// **不读数据库、不发网络请求**，因此对答题耗时的增量接近 0。
// 开关判断被特意放进 goroutine，主链路连一次 settings 查询都不增加。
func startSensitiveWordCheck(resolved resolvedSearchRequestContext, req model.SearchRequest, resp model.SearchResponse) *sensitiveWordCheck {
	task := &sensitiveWordCheck{
		logID:   make(chan int32, 1),
		release: sensitiveWordCheckOwners.start(),
	}
	question := strings.TrimSpace(resp.Question)
	if question == "" {
		question = strings.TrimSpace(req.Question)
	}
	answer := buildSensitiveWordAnswerText(resp)

	go func() {
		defer task.release()
		// 旁路任务自带的 recover：检测逻辑里的任何 panic 都不能带崩进程。
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.SysError(fmt.Sprintf("敏感词检测异常已忽略 requestId=%s error=%v", strings.TrimSpace(resolved.RequestID), recovered))
			}
		}()

		db := withDatabasePriority(resolveSearchPersistenceDB(), context.Background(), dbqueue.PriorityLow)
		if db == nil {
			return
		}
		// 开关在 goroutine 里读：关闭时**不构造客户端、不外呼、不写 sensitive 字段**。
		detector := defaultSensitiveWordDetectorBuilder(db)
		if detector == nil {
			return
		}
		if question == "" && answer == "" {
			return
		}

		checkContext, cancel := context.WithTimeout(context.Background(), sensitiveWordCheckTimeout)
		defer cancel()
		outcome := service.RunSensitiveWordCheck(checkContext, detector, question, answer)

		logID := task.awaitLogID()
		if logID <= 0 {
			logger.SysLog(fmt.Sprintf("敏感词检测跳过回写（审计行不可用） requestId=%s forbidden=%t", strings.TrimSpace(resolved.RequestID), outcome.Flag))
			return
		}
		if err := service.SaveSensitiveWordOutcomeWithDB(db, logID, outcome, sensitiveWordClock()); err != nil {
			// 打标失败只是「这次没标上」，答案早已返回，绝不能升级成搜索失败。
			logger.SysWarn(fmt.Sprintf("敏感词标记落库失败 requestId=%s logId=%d error=%s", strings.TrimSpace(resolved.RequestID), logID, logger.RedactSecrets(err.Error())))
			return
		}
		logger.SysLog(fmt.Sprintf("敏感词检测完成 requestId=%s logId=%d forbidden=%t error=%t", strings.TrimSpace(resolved.RequestID), logID, outcome.Flag, strings.TrimSpace(outcome.Error) != ""))
	}()
	return task
}

// attachLogID 把审计行主键交给旁路任务。
//
// 非阻塞：管道容量 1，重复调用安全。logID <= 0（审计行没写成功）也照常投递，
// 让旁路任务自己判断「没有可回写的行」并退出，而不是一直等到超时。
func (task *sensitiveWordCheck) attachLogID(logID int32) {
	if task == nil {
		return
	}
	select {
	case task.logID <- logID:
	default:
		// 已经投递过；保持第一次的值。
	}
}

// awaitLogID 等待审计行主键，超时返回 0。
func (task *sensitiveWordCheck) awaitLogID() int32 {
	timer := time.NewTimer(sensitiveWordAttachLogIDTimeout)
	defer timer.Stop()
	select {
	case logID := <-task.logID:
		return logID
	case <-timer.C:
		return 0
	}
}

// buildSensitiveWordAnswerText 汇总「返回给调用方的答案文本」用于检测。
//
// 只取真正会展示/返回的部分（需求：答题的答案也需要检测）：
// answerKeyText / answerText / bestAnswer。**不**把 allAnswer 全部摊开 ——
// 那里面含有被聚合淘汰的候选答案，检它们会把面板根本不显示的文本也算成命中。
func buildSensitiveWordAnswerText(resp model.SearchResponse) string {
	parts := make([]string, 0, 8)
	seen := make(map[string]struct{}, 8)
	appendPart := func(value string) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return
		}
		if _, exists := seen[trimmed]; exists {
			return
		}
		seen[trimmed] = struct{}{}
		parts = append(parts, trimmed)
	}
	appendPart(resp.Answer.AnswerKeyText)
	appendPart(resp.Answer.AnswerText)
	for _, item := range resp.Answer.BestAnswer {
		appendPart(item)
	}
	return strings.Join(parts, " ")
}

// sensitiveWordCheckOwnerTracker 跟踪进行中的旁路任务。
//
// 与 searchExecutionOwners 同一模式：进程退出与测试收尾都能等到它们结束，
// 避免「打标写到一半测试就结束」这类偶发失败。
type sensitiveWordCheckOwnerTracker struct {
	mu     sync.Mutex
	active int
	idle   chan struct{}
}

func newSensitiveWordCheckOwnerTracker() *sensitiveWordCheckOwnerTracker {
	idle := make(chan struct{})
	close(idle)
	return &sensitiveWordCheckOwnerTracker{idle: idle}
}

func (t *sensitiveWordCheckOwnerTracker) start() func() {
	t.mu.Lock()
	if t.active == 0 {
		t.idle = make(chan struct{})
	}
	t.active++
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.active--
			if t.active == 0 {
				close(t.idle)
			}
		})
	}
}

func (t *sensitiveWordCheckOwnerTracker) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	idle := t.idle
	t.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-idle:
		return nil
	}
}

var sensitiveWordCheckOwners = newSensitiveWordCheckOwnerTracker()

// WaitForSensitiveWordChecks 等待进行中的敏感词旁路任务结束。
//
// 供服务优雅关闭与测试收尾使用；不参与答题链路，调用与否都不影响答案。
func WaitForSensitiveWordChecks(ctx context.Context) error {
	return sensitiveWordCheckOwners.wait(ctx)
}

// sensitiveWordLogPreview 供测试构造断言用的日志行视图。
type sensitiveWordLogPreview struct {
	Flag      bool
	Words     string
	CheckedAt *time.Time
	Error     string
}

// loadSensitiveWordPreview 读取某条日志的敏感词标记，仅供测试与排障使用。
func loadSensitiveWordPreview(db *gorm.DB, logID int32) (sensitiveWordLogPreview, error) {
	var row entity.LogSensitive
	if db == nil {
		return sensitiveWordLogPreview{}, fmt.Errorf("数据库不可用")
	}
	if err := db.Session(&gorm.Session{NewDB: true}).Where("id = ?", logID).Take(&row).Error; err != nil {
		return sensitiveWordLogPreview{}, err
	}
	return sensitiveWordLogPreview{
		Flag:      row.Flag,
		Words:     row.Words,
		CheckedAt: row.CheckedAt,
		Error:     row.CheckError,
	}, nil
}
