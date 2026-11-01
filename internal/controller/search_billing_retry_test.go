package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
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
			tx.AddError(errors.New("temporary database failure"))
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
			tx.AddError(errors.New("temporary database failure"))
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
