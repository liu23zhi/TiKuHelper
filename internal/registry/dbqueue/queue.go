package dbqueue

import (
	"container/heap"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"gorm.io/gorm"
)

// Priority controls admission order for database work that is waiting to run.
// A running SQL statement or transaction is never preempted.
type Priority int

const (
	PriorityLow Priority = iota
	PriorityNormal
	PriorityHigh
)

const (
	defaultQueueCapacity = 1024
	maxConsecutiveUrgent = 8
)

type priorityContextKey struct{}

var priorityKey priorityContextKey

var (
	ErrSchedulerClosed = errors.New("database scheduler is closed")
	ErrQueueFull       = errors.New("database scheduler queue is full")
)

// WithPriority attaches a database admission priority to a request context.
// Contexts without an explicit priority use PriorityNormal.
func WithPriority(ctx context.Context, priority Priority) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, priorityKey, normalizePriority(priority))
}

func PriorityFromContext(ctx context.Context) Priority {
	if ctx == nil {
		return PriorityNormal
	}
	priority, ok := ctx.Value(priorityKey).(Priority)
	if !ok {
		return PriorityNormal
	}
	return normalizePriority(priority)
}

func normalizePriority(priority Priority) Priority {
	switch {
	case priority < PriorityLow:
		return PriorityLow
	case priority > PriorityHigh:
		return PriorityHigh
	default:
		return priority
	}
}

type waiter struct {
	priority Priority
	sequence uint64
	ready    chan struct{}
	index    int
	started  bool
	err      error
}

type waiterHeap []*waiter

func (w waiterHeap) Len() int { return len(w) }
func (w waiterHeap) Less(i, j int) bool {
	if w[i].priority != w[j].priority {
		return w[i].priority > w[j].priority
	}
	return w[i].sequence < w[j].sequence
}
func (w waiterHeap) Swap(i, j int) {
	w[i], w[j] = w[j], w[i]
	w[i].index = i
	w[j].index = j
}
func (w *waiterHeap) Push(value any) {
	item := value.(*waiter)
	item.index = len(*w)
	*w = append(*w, item)
}
func (w *waiterHeap) Pop() any {
	old := *w
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*w = old[:n-1]
	return item
}

// Scheduler is a bounded, priority-aware admission queue. It defaults to a
// single active task, which matches the SQLite deployment's single connection
// and keeps a transaction's reads and writes atomic with respect to the queue.
type Scheduler struct {
	mu                sync.Mutex
	waiters           waiterHeap
	active            int
	maxActive         int
	sequence          uint64
	consecutiveUrgent int
	closed            bool
	capacity          int
}

// NewScheduler creates a scheduler. The optional argument sets the maximum
// number of active database tasks; omitted or non-positive means one.
func NewScheduler(maxActive ...int) *Scheduler {
	limit := 1
	if len(maxActive) > 0 && maxActive[0] > 0 {
		limit = maxActive[0]
	}
	scheduler := &Scheduler{maxActive: limit, capacity: defaultQueueCapacity}
	heap.Init(&scheduler.waiters)
	return scheduler
}

// Acquire waits until this task is admitted and returns an idempotent release
// function. The context is only used while the task is waiting.
func (s *Scheduler) Acquire(ctx context.Context, priority Priority) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	priority = normalizePriority(priority)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrSchedulerClosed
	}
	if s.active < s.maxActive && s.waiters.Len() == 0 {
		s.active++
		s.noteStart(priority)
		s.mu.Unlock()
		return s.releaseFunc(), nil
	}
	if s.waiters.Len() >= s.capacity {
		s.mu.Unlock()
		return nil, ErrQueueFull
	}
	item := &waiter{priority: priority, sequence: s.sequence, ready: make(chan struct{})}
	s.sequence++
	heap.Push(&s.waiters, item)
	s.mu.Unlock()

	select {
	case <-item.ready:
		if item.err != nil {
			return nil, item.err
		}
		return s.releaseFunc(), nil
	case <-ctx.Done():
		s.mu.Lock()
		if !item.started && item.index >= 0 {
			heap.Remove(&s.waiters, item.index)
			s.mu.Unlock()
			return nil, ctx.Err()
		}
		s.mu.Unlock()
		<-item.ready
		if item.err != nil {
			return nil, item.err
		}
		return s.releaseFunc(), nil
	}
}

func (s *Scheduler) releaseFunc() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.active > 0 {
				s.active--
			}
			s.dispatchLocked()
			s.mu.Unlock()
		})
	}
}

func (s *Scheduler) noteStart(priority Priority) {
	if priority == PriorityHigh {
		s.consecutiveUrgent++
	} else {
		s.consecutiveUrgent = 0
	}
}

func (s *Scheduler) dispatchLocked() {
	for !s.closed && s.active < s.maxActive && s.waiters.Len() > 0 {
		index := 0
		if s.consecutiveUrgent >= maxConsecutiveUrgent {
			candidate := -1
			for i, item := range s.waiters {
				if item.priority == PriorityHigh {
					continue
				}
				if candidate < 0 || item.priority > s.waiters[candidate].priority ||
					(item.priority == s.waiters[candidate].priority && item.sequence < s.waiters[candidate].sequence) {
					candidate = i
				}
			}
			if candidate >= 0 {
				index = candidate
			}
		}
		item := heap.Remove(&s.waiters, index).(*waiter)
		item.started = true
		s.active++
		s.noteStart(item.priority)
		close(item.ready)
	}
}

// Close stops admission and releases all waiting tasks with ErrSchedulerClosed.
func (s *Scheduler) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for s.waiters.Len() > 0 {
		item := heap.Pop(&s.waiters).(*waiter)
		item.err = ErrSchedulerClosed
		close(item.ready)
	}
	s.mu.Unlock()
}

// Pool is a GORM ConnPool proxy. Every ordinary SQL operation acquires one
// scheduler lease. BeginTx holds its lease until Commit or Rollback.
type Pool struct {
	underlying gorm.ConnPool
	scheduler  *Scheduler
}

func Wrap(underlying gorm.ConnPool, scheduler *Scheduler) *Pool {
	if existing, ok := underlying.(*Pool); ok {
		return existing
	}
	if scheduler == nil {
		scheduler = NewScheduler()
	}
	return &Pool{underlying: underlying, scheduler: scheduler}
}

func (p *Pool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	release, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return p.underlying.PrepareContext(ctx, query)
}

func (p *Pool) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	release, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return p.underlying.ExecContext(ctx, query, args...)
}

func (p *Pool) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	release, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	// ConnPool's QueryContext API returns concrete *sql.Rows, so this lease can
	// cover query admission but cannot observe the caller's later Rows.Close.
	// GORM callbacks consume/close rows synchronously; the underlying database/sql
	// pool continues to own a connection until any explicitly returned Rows close.
	defer release()
	return p.underlying.QueryContext(ctx, query, args...)
}

func (p *Pool) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	release, err := p.acquire(ctx)
	if err != nil {
		// QueryRowContext cannot return an error separately. Use a cancelled
		// context so Scan reports a cancellation instead of misreporting no rows.
		failedCtx, cancel := context.WithCancel(ctx)
		cancel()
		return p.underlying.QueryRowContext(failedCtx, query, args...)
	}
	defer release()
	return p.underlying.QueryRowContext(ctx, query, args...)
}

func (p *Pool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	release, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	var tx gorm.ConnPool
	switch beginner := p.underlying.(type) {
	case gorm.ConnPoolBeginner:
		tx, err = beginner.BeginTx(ctx, opts)
	case gorm.TxBeginner:
		tx, err = beginner.BeginTx(ctx, opts)
	default:
		release()
		return nil, fmt.Errorf("database pool does not support transactions")
	}
	if err != nil {
		release()
		return nil, err
	}
	dbConn, _ := p.GetDBConn()
	return &transactionPool{underlying: tx, release: release, dbConn: dbConn}, nil
}

func (p *Pool) GetDBConn() (*sql.DB, error) {
	if connector, ok := p.underlying.(gorm.GetDBConnector); ok {
		return connector.GetDBConn()
	}
	if sqlDB, ok := p.underlying.(*sql.DB); ok {
		return sqlDB, nil
	}
	return nil, gorm.ErrInvalidDB
}

func (p *Pool) acquire(ctx context.Context) (func(), error) {
	return p.scheduler.Acquire(ctx, PriorityFromContext(ctx))
}

type transactionPool struct {
	underlying gorm.ConnPool
	release    func()
	dbConn     *sql.DB
	once       sync.Once
}

func (p *transactionPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.underlying.PrepareContext(ctx, query)
}
func (p *transactionPool) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return p.underlying.ExecContext(ctx, query, args...)
}
func (p *transactionPool) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return p.underlying.QueryContext(ctx, query, args...)
}
func (p *transactionPool) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return p.underlying.QueryRowContext(ctx, query, args...)
}
func (p *transactionPool) Commit() error {
	defer p.releaseOnce()
	committer, ok := p.underlying.(gorm.TxCommitter)
	if !ok {
		return gorm.ErrInvalidTransaction
	}
	return committer.Commit()
}
func (p *transactionPool) Rollback() error {
	defer p.releaseOnce()
	committer, ok := p.underlying.(gorm.TxCommitter)
	if !ok {
		return gorm.ErrInvalidTransaction
	}
	return committer.Rollback()
}
func (p *transactionPool) StmtContext(ctx context.Context, stmt *sql.Stmt) *sql.Stmt {
	if tx, ok := p.underlying.(interface {
		StmtContext(context.Context, *sql.Stmt) *sql.Stmt
	}); ok {
		return tx.StmtContext(ctx, stmt)
	}
	return stmt
}
func (p *transactionPool) releaseOnce() { p.once.Do(p.release) }

func (p *transactionPool) GetDBConn() (*sql.DB, error) {
	if p.dbConn != nil {
		return p.dbConn, nil
	}
	if connector, ok := p.underlying.(gorm.GetDBConnector); ok {
		return connector.GetDBConn()
	}
	return nil, gorm.ErrInvalidDB
}

var _ gorm.ConnPoolBeginner = (*Pool)(nil)
var _ gorm.TxCommitter = (*transactionPool)(nil)
var _ gorm.ConnPool = (*Pool)(nil)
var _ gorm.ConnPool = (*transactionPool)(nil)
var _ gorm.GetDBConnector = (*transactionPool)(nil)
