package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/registry/dbqueue"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/logger"
	"github.com/itihey/tikuAdapter/pkg/util"
	"gorm.io/gorm"
)

const (
	searchBillingRetryInitialDelay = time.Second
	searchBillingRetryMaxDelay     = time.Minute
	searchBillingRetryAttemptLimit = 15 * time.Second
	searchBillingRetryIdleWait     = time.Hour
)

var (
	errSearchBillingRetryConflict = errors.New("同一请求编号已存在不同的账单批次")
	searchBillingRetries          = newSearchBillingRetryQueue()
)

type searchBillingRetryJob struct {
	db            *gorm.DB
	ctx           searchBillingContext
	charges       []searchBillingCharge
	attempts      int
	nextAttemptAt time.Time
}

type searchBillingRetryQueue struct {
	mu   sync.Mutex
	jobs []*searchBillingRetryJob
	wake chan struct{}
}

func newSearchBillingRetryQueue() *searchBillingRetryQueue {
	return &searchBillingRetryQueue{wake: make(chan struct{}, 1)}
}

func persistSearchBillingRecordsWithRetry(db *gorm.DB, ctx searchBillingContext, charges []searchBillingCharge, queue *searchBillingRetryQueue) (bool, error) {
	err := persistSearchBillingRecordsWithDB(db, ctx, charges)
	if err == nil {
		return false, nil
	}
	if queue == nil || db == nil || ctx.UserID <= 0 || strings.TrimSpace(ctx.RequestID) == "" {
		return false, err
	}
	job := &searchBillingRetryJob{
		db:            db.Session(&gorm.Session{NewDB: true}),
		ctx:           ctx,
		charges:       cloneSearchBillingCharges(charges),
		attempts:      1,
		nextAttemptAt: time.Now().Add(searchBillingRetryBackoff(1)),
	}
	queue.enqueue(job)
	logger.SysWarn(fmt.Sprintf("search_billing_retry_queued requestId=%s userId=%d attempts=%d pending=%d error=%s",
		strings.TrimSpace(ctx.RequestID), ctx.UserID, job.attempts, queue.pendingCount(), logger.RedactSecrets(err.Error())))
	return true, err
}

func appendSearchBillingRetryWarning(warning string) string {
	return strings.TrimSpace(strings.TrimSpace(warning) + " 计费暂未落库，系统将在后台自动重试。")
}

func cloneSearchBillingCharges(charges []searchBillingCharge) []searchBillingCharge {
	cloned := append([]searchBillingCharge(nil), charges...)
	for index := range cloned {
		if staged := cloned[index].AIStagedBilling; staged != nil {
			stagedCopy := *staged
			stagedCopy.Calls = append([]aiStagedTokenBillingCall(nil), staged.Calls...)
			cloned[index].AIStagedBilling = &stagedCopy
		}
		cloned[index].AIStageSettings.MultiplierPoints = append([]service.AIStagedTokenMultiplierPoint(nil), cloned[index].AIStageSettings.MultiplierPoints...)
	}
	return cloned
}

func (queue *searchBillingRetryQueue) enqueue(job *searchBillingRetryJob) {
	if queue == nil || job == nil {
		return
	}
	queue.mu.Lock()
	queue.jobs = append(queue.jobs, job)
	queue.mu.Unlock()
	select {
	case queue.wake <- struct{}{}:
	default:
	}
}

func (queue *searchBillingRetryQueue) pendingCount() int {
	if queue == nil {
		return 0
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return len(queue.jobs)
}

func (queue *searchBillingRetryQueue) takeDue(now time.Time) (*searchBillingRetryJob, time.Duration) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.jobs) == 0 {
		return nil, searchBillingRetryIdleWait
	}
	index := 0
	for candidate := 1; candidate < len(queue.jobs); candidate++ {
		if queue.jobs[candidate].nextAttemptAt.Before(queue.jobs[index].nextAttemptAt) {
			index = candidate
		}
	}
	job := queue.jobs[index]
	if wait := job.nextAttemptAt.Sub(now); wait > 0 {
		return nil, wait
	}
	queue.jobs = append(queue.jobs[:index], queue.jobs[index+1:]...)
	return job, 0
}

func (queue *searchBillingRetryQueue) processDue(ctx context.Context, now time.Time) bool {
	job, _ := queue.takeDue(now)
	if job == nil {
		return false
	}
	if ctx.Err() != nil {
		queue.enqueue(job)
		return true
	}
	attemptContext, cancel := context.WithTimeout(ctx, searchBillingRetryAttemptLimit)
	retryDB := job.db.Session(&gorm.Session{NewDB: true}).WithContext(dbqueue.WithPriority(attemptContext, dbqueue.PriorityLow))
	err := persistSearchBillingRecordsWithDB(retryDB, job.ctx, job.charges)
	cancel()
	if err == nil {
		logger.SysLog(fmt.Sprintf("search_billing_retry_succeeded requestId=%s userId=%d attempts=%d",
			strings.TrimSpace(job.ctx.RequestID), job.ctx.UserID, job.attempts+1))
		return true
	}
	if errors.Is(err, errSearchBillingRetryConflict) {
		logger.SysError(fmt.Sprintf("search_billing_retry_abandoned requestId=%s userId=%d error=%s",
			strings.TrimSpace(job.ctx.RequestID), job.ctx.UserID, logger.RedactSecrets(err.Error())))
		return true
	}
	job.attempts++
	job.nextAttemptAt = time.Now().Add(searchBillingRetryBackoff(job.attempts))
	queue.enqueue(job)
	logger.SysWarn(fmt.Sprintf("search_billing_retry_failed requestId=%s userId=%d attempts=%d nextAttemptAt=%s pending=%d error=%s",
		strings.TrimSpace(job.ctx.RequestID), job.ctx.UserID, job.attempts, job.nextAttemptAt.Format(time.RFC3339), queue.pendingCount(), logger.RedactSecrets(err.Error())))
	return true
}

func (queue *searchBillingRetryQueue) nextWait(now time.Time) time.Duration {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.jobs) == 0 {
		return searchBillingRetryIdleWait
	}
	next := queue.jobs[0].nextAttemptAt
	for _, job := range queue.jobs[1:] {
		if job.nextAttemptAt.Before(next) {
			next = job.nextAttemptAt
		}
	}
	if wait := next.Sub(now); wait > 0 {
		return wait
	}
	return 0
}

func searchBillingRetryBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return searchBillingRetryInitialDelay
	}
	if attempt >= 7 {
		return searchBillingRetryMaxDelay
	}
	delay := searchBillingRetryInitialDelay * time.Duration(1<<(attempt-1))
	if delay > searchBillingRetryMaxDelay || delay < 0 {
		return searchBillingRetryMaxDelay
	}
	return delay
}

func RunSearchBillingRetryWorker(ctx context.Context) error {
	return searchBillingRetries.run(ctx)
}

func (queue *searchBillingRetryQueue) run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if ctx.Err() != nil {
			pending := queue.pendingCount()
			if pending > 0 {
				logger.SysWarn(fmt.Sprintf("search_billing_retry_worker_stopped pending=%d; queued retries are held in memory and will not survive restart", pending))
			}
			return nil
		}
		if queue.processDue(ctx, time.Now()) {
			continue
		}
		delay := queue.nextWait(time.Now())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			stopSearchBillingRetryTimer(timer)
		case <-queue.wake:
			stopSearchBillingRetryTimer(timer)
		case <-timer.C:
		}
	}
}

func stopSearchBillingRetryTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func StartSearchBillingRetryWorker(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := RunSearchBillingRetryWorker(ctx); err != nil && ctx.Err() == nil {
			logger.SysWarn(fmt.Sprintf("search_billing_retry_worker_failed error=%s", logger.RedactSecrets(err.Error())))
		}
	}()
	return done
}

func searchBillingRetryBatchAlreadyPersisted(tx *gorm.DB, ctx searchBillingContext, charges []searchBillingCharge) (bool, error) {
	requestID := strings.TrimSpace(ctx.RequestID)
	if requestID == "" {
		return false, nil
	}
	var records []entity.BillingRecord
	if err := tx.Where("user_id = ? AND request_id = ? AND type = ?", ctx.UserID, requestID, searchBillingRecordTypeNormal).
		Order("id ASC").Find(&records).Error; err != nil {
		return false, err
	}
	if len(records) == 0 {
		return false, nil
	}
	if len(records) != len(charges) {
		return false, fmt.Errorf("%w: existing=%d expected=%d", errSearchBillingRetryConflict, len(records), len(charges))
	}
	matched := make([]bool, len(charges))
	for _, record := range records {
		found := false
		for index, charge := range charges {
			if matched[index] || !billingRecordMatchesSearchCharge(record, ctx, charge) {
				continue
			}
			matched[index] = true
			found = true
			break
		}
		if !found {
			return false, fmt.Errorf("%w: existing charge does not match retry payload", errSearchBillingRetryConflict)
		}
	}
	return true, nil
}

func billingRecordMatchesSearchCharge(record entity.BillingRecord, ctx searchBillingContext, charge searchBillingCharge) bool {
	return record.AccessTokenID == ctx.AccessTokenID && record.UsedDefaultAccount == ctx.UsedDefaultAccount &&
		record.Provider == charge.Provider && record.ModelName == charge.ModelName &&
		record.InputTokens == charge.InputTokens && record.CachedInputTokens == charge.CachedInputTokens &&
		record.OutputTokens == charge.OutputTokens && record.TotalTokens == charge.TotalTokens &&
		util.RoundMoney(record.Amount) == util.RoundMoney(charge.Amount) && record.UnitPriceSnapshot == charge.Snapshot &&
		record.QuestionText == service.ExtractBillingSnapshotField(charge.Snapshot, "question") &&
		record.ClientIP == strings.TrimSpace(ctx.ClientIP) && record.SearchMode == billingSearchMode(ctx.DeepThinking, ctx.AIRoute)
}
