package controller

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/itihey/tikuAdapter/internal/entity"
	searchpkg "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
	"gorm.io/gorm"
)

func newBillingRetryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&entity.User{}, &entity.AccessToken{}, &entity.BillingRecord{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestPersistSearchBillingRecordsWithDBIsIdempotent(t *testing.T) {
	db := newBillingRetryTestDB(t)
	user := seedControllerUser(t, db, 9001, "billing-retry-user", "user")
	token := seedControllerTokenForUser(t, db, user, "billing-retry-token", 0)
	billingContext := searchBillingContext{UserID: user.ID, AccessTokenID: token.ID, RequestID: "retry-idempotency"}
	charges := []searchBillingCharge{{Provider: "icodef", Amount: 0.25}, {Provider: "ai", Amount: 0.75}}

	for attempt := 0; attempt < 2; attempt++ {
		if err := persistSearchBillingRecordsWithDB(db, billingContext, charges); err != nil {
			t.Fatalf("persist attempt %d: %v", attempt+1, err)
		}
	}
	if err := persistSearchBillingRecordsWithDB(db, billingContext, []searchBillingCharge{
		{Provider: "icodef", Amount: 0.5}, {Provider: "ai", Amount: 0.5},
	}); !errors.Is(err, errSearchBillingRetryConflict) {
		t.Fatalf("different batch with same request ID error = %v, want retry conflict", err)
	}

	var count int64
	if err := db.Model(&entity.BillingRecord{}).Where("request_id = ?", billingContext.RequestID).Count(&count).Error; err != nil {
		t.Fatalf("count billing rows: %v", err)
	}
	if count != int64(len(charges)) {
		t.Fatalf("billing row count = %d, want %d", count, len(charges))
	}
	var storedUser entity.User
	if err := db.First(&storedUser, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if storedUser.Balance != -1 {
		t.Fatalf("balance = %v, want -1", storedUser.Balance)
	}
	var storedToken entity.AccessToken
	if err := db.First(&storedToken, token.ID).Error; err != nil {
		t.Fatalf("load token: %v", err)
	}
	if storedToken.TotalAmountUsed != 1 {
		t.Fatalf("token usage = %v, want 1", storedToken.TotalAmountUsed)
	}
}

func TestSearchBillingPersistenceFailureQueuesNonBlockingRetry(t *testing.T) {
	db := newBillingRetryTestDB(t)
	user := seedControllerUser(t, db, 9002, "billing-retry-failure-user", "user")
	token := seedControllerTokenForUser(t, db, user, "billing-retry-failure-token", 0)
	var creates atomic.Int32
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail-first-billing-create", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameBillingRecord && creates.Add(1) == 1 {
			tx.AddError(&mysqldriver.MySQLError{Number: 1213, Message: "synthetic database deadlock"})
		}
	}); err != nil {
		t.Fatalf("register create callback: %v", err)
	}

	retryQueue := newSearchBillingRetryQueue()
	billingContext := searchBillingContext{UserID: user.ID, AccessTokenID: token.ID, RequestID: "retry-after-write-failure"}
	charges := []searchBillingCharge{{Provider: "ai", Amount: 0.5}}
	start := time.Now()
	queued, err := persistSearchBillingRecordsWithRetry(db, billingContext, charges, retryQueue)
	if err == nil {
		t.Fatal("expected the original persistence attempt to fail")
	}
	if !queued {
		t.Fatal("expected the failed billing batch to be accepted by the retry queue")
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("failed persistence waited for retry: elapsed %s", elapsed)
	}
	if got := retryQueue.pendingCount(); got != 1 {
		t.Fatalf("queued retries = %d, want 1", got)
	}
	var count int64
	db.Model(&entity.BillingRecord{}).Where("request_id = ?", billingContext.RequestID).Count(&count)
	if count != 0 {
		t.Fatalf("failed transaction left %d billing rows", count)
	}

	if !retryQueue.processDue(context.Background(), time.Now().Add(time.Minute)) {
		t.Fatal("retry worker did not process the queued billing batch")
	}
	if got := retryQueue.pendingCount(); got != 0 {
		t.Fatalf("queued retries after success = %d, want 0", got)
	}
	if err := db.Model(&entity.BillingRecord{}).Where("request_id = ?", billingContext.RequestID).Count(&count).Error; err != nil {
		t.Fatalf("count retried billing rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("retried billing row count = %d, want 1", count)
	}
	var storedUser entity.User
	if err := db.First(&storedUser, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if storedUser.Balance != -0.5 {
		t.Fatalf("balance after retry = %v, want -0.5", storedUser.Balance)
	}
	var storedToken entity.AccessToken
	if err := db.First(&storedToken, token.ID).Error; err != nil {
		t.Fatalf("load token: %v", err)
	}
	if storedToken.TotalAmountUsed != 0.5 {
		t.Fatalf("token usage after retry = %v, want 0.5", storedToken.TotalAmountUsed)
	}
}

func TestAIFallbackBillingRetryDoesNotReturnBillableResultAsFailure(t *testing.T) {
	db := newSearchControllerTestDB(t)
	user := seedControllerUser(t, db, 9004, "billing-retry-fallback-user", "user")
	if err := service.SetAISearchPricePerCallWithDB(db, 0.25); err != nil {
		t.Fatalf("set AI search price: %v", err)
	}
	var creates atomic.Int32
	if err := db.Callback().Create().Before("gorm:create").Register("test:fail-first-fallback-billing-create", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameBillingRecord && creates.Add(1) == 1 {
			tx.AddError(&mysqldriver.MySQLError{Number: 1213, Message: "synthetic database deadlock"})
		}
	}); err != nil {
		t.Fatalf("register create callback: %v", err)
	}

	previousQueue := searchBillingRetries
	searchBillingRetries = newSearchBillingRetryQueue()
	defer func() { searchBillingRetries = previousQueue }()
	result := service.AIFallbackResult{
		Success: true,
		Usage: searchpkg.AISearchUsage{SearchCalls: []searchpkg.AISearchCallUsage{{
			API: "https://search.example/api", Query: "test", Status: "success", ResultCount: 1,
		}}},
	}
	warning, billedResult, err := persistAIFallbackBillingWithWarning(
		searchBillingContext{UserID: user.ID, Billable: true}, "fallback-billing-retry", model.SearchRequest{Question: "test"}, result,
	)
	if err != nil {
		t.Fatalf("queued retry should not turn a successful fallback into a failed result: %v", err)
	}
	if !billedResult.Success {
		t.Fatal("successful fallback result was not preserved")
	}
	if !strings.Contains(warning, "后台自动重试") {
		t.Fatalf("warning = %q, want deferred billing notice", warning)
	}
	if got := searchBillingRetries.pendingCount(); got != 1 {
		t.Fatalf("queued retries = %d, want 1", got)
	}
}

func TestSearchBillingRetryWorkerDoesNotBlockQueueing(t *testing.T) {
	db := newBillingRetryTestDB(t)
	user := seedControllerUser(t, db, 9003, "billing-retry-worker-user", "user")
	var createStarted atomic.Int32
	var retryHasDeadline atomic.Bool
	createBlocked := make(chan struct{})
	releaseCreate := make(chan struct{})
	if err := db.Callback().Create().Before("gorm:create").Register("test:block-billing-retry-create", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameBillingRecord && createStarted.Add(1) == 1 {
			_, hasDeadline := tx.Statement.Context.Deadline()
			retryHasDeadline.Store(hasDeadline)
			close(createBlocked)
			<-releaseCreate
		}
	}); err != nil {
		t.Fatalf("register create callback: %v", err)
	}

	retryQueue := newSearchBillingRetryQueue()
	first := &searchBillingRetryJob{
		db:            db,
		ctx:           searchBillingContext{UserID: user.ID, RequestID: "retry-worker-running"},
		charges:       []searchBillingCharge{{Provider: "ai", Amount: 0.25}},
		attempts:      1,
		nextAttemptAt: time.Now().Add(-time.Second),
	}
	retryQueue.enqueue(first)
	workerContext, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_ = retryQueue.run(workerContext)
	}()
	defer func() {
		cancelWorker()
		select {
		case <-releaseCreate:
		default:
			close(releaseCreate)
		}
		<-workerDone
	}()

	select {
	case <-createBlocked:
	case <-time.After(time.Second):
		t.Fatal("worker did not reach the blocked database write")
	}
	if !retryHasDeadline.Load() {
		t.Fatal("database retry did not receive a bounded context")
	}
	second := &searchBillingRetryJob{
		db:            db,
		ctx:           searchBillingContext{UserID: user.ID, RequestID: "retry-worker-queued-during-write"},
		charges:       []searchBillingCharge{{Provider: "ai", Amount: 0.5}},
		attempts:      1,
		nextAttemptAt: time.Now().Add(time.Minute),
	}
	start := time.Now()
	retryQueue.enqueue(second)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("queueing while retry write was blocked took %s", elapsed)
	}
	if got := retryQueue.pendingCount(); got != 1 {
		t.Fatalf("pending retries while worker is blocked = %d, want 1", got)
	}
	cancelWorker()
	close(releaseCreate)
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("retry worker did not stop after cancellation")
	}
}

func TestSearchBillingRetryBackoffCapsAtOneMinute(t *testing.T) {
	for _, testCase := range []struct {
		attempt int
		want    time.Duration
	}{{1, time.Second}, {2, 2 * time.Second}, {6, 32 * time.Second}, {7, time.Minute}, {20, time.Minute}} {
		if got := searchBillingRetryBackoff(testCase.attempt); got != testCase.want {
			t.Errorf("backoff(%d) = %s, want %s", testCase.attempt, got, testCase.want)
		}
	}
}

// The original two injected generic errors above are now genuine driver-typed
// deadlocks. Unknown strings must not manufacture a promise to retry a debit.
type billingRetrySQLiteCodeError int

func (err billingRetrySQLiteCodeError) Error() string { return "synthetic SQLite result code" }
func (err billingRetrySQLiteCodeError) Code() int     { return int(err) }

func TestSearchBillingRetryClassifiesOnlyTransientPersistence(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{"sqlite-busy", billingRetrySQLiteCodeError(5), true},
		{"sqlite-extended-busy", billingRetrySQLiteCodeError(517), true},
		{"sqlite-locked", billingRetrySQLiteCodeError(6), true},
		{"mysql-lock-timeout", &mysqldriver.MySQLError{Number: 1205}, true},
		{"mysql-deadlock", fmt.Errorf("wrapped: %w", &mysqldriver.MySQLError{Number: 1213}), true},
		{"mysql-connection-lost", &mysqldriver.MySQLError{Number: 2013}, true},
		{"bad-connection", driver.ErrBadConn, true},
		{"statement-deadline", context.DeadlineExceeded, true},
		{"sqlite-constraint", billingRetrySQLiteCodeError(19), false},
		{"mysql-constraint", &mysqldriver.MySQLError{Number: 1062}, false},
		{"unknown-error", errors.New("temporary database failure"), false},
		{"canceled-owner", context.Canceled, false},
		{"quota", errAccessTokenQuotaExceeded, false},
		{"balance", service.ErrInsufficientSearchBalance, false},
		{"invalid-key", service.ErrInvalidAccessTokenAuthorization, false},
		{"cas", errSearchBillingCompareAndSwap, false},
		{"batch-conflict", errSearchBillingRetryConflict, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := retryableSearchBillingPersistenceError(test.err); got != test.want {
				t.Fatalf("retryable=%t want %t", got, test.want)
			}
		})
	}
}

func TestSearchBillingRetryPermanentFailuresDoNotQueueOrRequeue(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"quota", errAccessTokenQuotaExceeded}, {"balance", service.ErrInsufficientSearchBalance},
		{"invalid-key", service.ErrInvalidAccessTokenAuthorization}, {"cas", errSearchBillingCompareAndSwap},
		{"batch-conflict", errSearchBillingRetryConflict}, {"unknown", errors.New("synthetic permanent storage failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newBillingRetryTestDB(t)
			user := seedControllerUser(t, db, 9020, "retry-terminal-user", "user")
			token := seedControllerTokenForUser(t, db, user, "retry-terminal-token", 0)
			if err := db.Callback().Create().Before("gorm:create").Register("test:terminal-billing", func(tx *gorm.DB) {
				if tx.Statement.Table == entity.TableNameBillingRecord {
					tx.AddError(fmt.Errorf("wrapped: %w", test.err))
				}
			}); err != nil {
				t.Fatal(err)
			}
			queue := newSearchBillingRetryQueue()
			billingContext := searchBillingContext{UserID: user.ID, AccessTokenID: token.ID, RequestID: "permanent-" + test.name}
			charges := []searchBillingCharge{{Provider: "ai", Amount: 0.25}}
			queued, err := persistSearchBillingRecordsWithRetry(db, billingContext, charges, queue)
			if queued || !errors.Is(err, test.err) || queue.pendingCount() != 0 {
				t.Fatalf("permanent failure queued=%t pending=%d err=%v", queued, queue.pendingCount(), err)
			}
			// A formerly transient queued job can meet a permanent error later.
			queue.enqueue(&searchBillingRetryJob{db: db, ctx: billingContext, charges: charges, nextAttemptAt: time.Now().Add(-time.Second)})
			if !queue.processDue(context.Background(), time.Now()) || queue.pendingCount() != 0 {
				t.Fatal("permanent retry was retained")
			}
			waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := queue.waitIdle(waitCtx); err != nil {
				t.Fatal(err)
			}
			var bills int64
			if err := db.Model(&entity.BillingRecord{}).Count(&bills).Error; err != nil {
				t.Fatal(err)
			}
			var storedUser entity.User
			var storedToken entity.AccessToken
			if err := db.First(&storedUser, user.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.First(&storedToken, token.ID).Error; err != nil {
				t.Fatal(err)
			}
			if bills != 0 || storedUser.Balance != user.Balance || storedToken.TotalAmountUsed != token.TotalAmountUsed {
				t.Fatal("permanent failure changed committed billing/wallet/quota")
			}
		})
	}
}

func TestSearchBillingRetryDrainTracksInFlightAndNextWake(t *testing.T) {
	db := newBillingRetryTestDB(t)
	user := seedControllerUser(t, db, 9030, "retry-drain-user", "user")
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	var creates atomic.Int32
	if err := db.Callback().Create().Before("gorm:create").Register("test:drain-inflight", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameBillingRecord && creates.Add(1) == 1 {
			close(started)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	queue := newSearchBillingRetryQueue()
	previousQueue := searchBillingRetries
	searchBillingRetries = queue
	defer func() { searchBillingRetries = previousQueue }()
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		cancelWorker()
		select {
		case <-workerDone:
		case <-time.After(2 * time.Second):
			t.Error("retry worker cleanup did not complete")
		}
	})
	queue.enqueue(&searchBillingRetryJob{db: db, ctx: searchBillingContext{UserID: user.ID, RequestID: "drain-first"}, charges: []searchBillingCharge{{Provider: "ai", Amount: 0.25}}, nextAttemptAt: time.Now().Add(-time.Second)})
	go func() { defer close(workerDone); _ = queue.run(workerCtx) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not enter owned blocked SQL")
	}
	queue.mu.Lock()
	active, queued, idle := queue.active, len(queue.jobs), queue.idle
	queue.mu.Unlock()
	if active != 1 || queued != 0 {
		t.Fatalf("drain state active=%d queued=%d", active, queued)
	}
	select {
	case <-idle:
		t.Fatal("queue-empty incorrectly signaled idle during SQL")
	default:
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WaitForSearchBillingRetries(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("drain ignored in-flight work: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelDrain()
	if err := WaitForSearchBillingRetries(drainCtx); err != nil {
		t.Fatal(err)
	}
	// Enqueue after a completed generation must create a new signal and wake
	// the still-running worker, rather than passing on the old closed channel.
	queue.enqueue(&searchBillingRetryJob{db: db, ctx: searchBillingContext{UserID: user.ID, RequestID: "drain-second"}, charges: []searchBillingCharge{{Provider: "ai", Amount: 0.25}}, nextAttemptAt: time.Now().Add(-time.Second)})
	queue.mu.Lock()
	sameSignal := queue.idle == idle
	queue.mu.Unlock()
	if sameSignal {
		t.Fatal("new queued work reused a closed idle generation")
	}
	if err := WaitForSearchBillingRetries(drainCtx); err != nil {
		t.Fatal(err)
	}
	var bills int64
	if err := db.Model(&entity.BillingRecord{}).Count(&bills).Error; err != nil {
		t.Fatal(err)
	}
	if bills != 2 {
		t.Fatalf("wake/drain committed %d bills, want2", bills)
	}
}

func TestSearchBillingRetryShutdownRetainsInterruptedJob(t *testing.T) {
	db := newBillingRetryTestDB(t)
	// Cancellation can discard the transaction connection. Keep the shared
	// in-memory fixture alive independently, not by weakening rollback checks.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	lifeline, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lifeline.Close() })
	user := seedControllerUser(t, db, 9040, "retry-shutdown-user", "user")
	started := make(chan struct{})
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	if err := db.Callback().Create().Before("gorm:create").Register("test:shutdown-billing", func(tx *gorm.DB) {
		if tx.Statement.Table == entity.TableNameBillingRecord {
			close(started)
			<-workerCtx.Done()
			tx.AddError(workerCtx.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	queue := newSearchBillingRetryQueue()
	queue.enqueue(&searchBillingRetryJob{db: db, ctx: searchBillingContext{UserID: user.ID, RequestID: "shutdown-interrupted"}, charges: []searchBillingCharge{{Provider: "ai", Amount: 0.25}}, nextAttemptAt: time.Now().Add(-time.Second)})
	t.Cleanup(func() {
		cancelWorker()
		select {
		case <-workerDone:
		case <-time.After(2 * time.Second):
			t.Error("shutdown worker cleanup did not complete")
		}
	})
	go func() { defer close(workerDone); _ = queue.run(workerCtx) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not enter owned interrupted SQL")
	}
	cancelWorker()
	select {
	case <-workerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop")
	}
	queue.mu.Lock()
	pending, active, idle := len(queue.jobs), queue.active, queue.idle
	queue.mu.Unlock()
	if pending != 1 || active != 0 {
		t.Fatalf("shutdown lost pending work: queued=%d active=%d", pending, active)
	}
	select {
	case <-idle:
		t.Fatal("interrupted backlog falsely drained")
	default:
	}
	var bills int64
	if err := db.Model(&entity.BillingRecord{}).Count(&bills).Error; err != nil {
		t.Fatal(err)
	}
	if bills != 0 {
		t.Fatalf("interrupted transaction committed %d bills", bills)
	}
}
