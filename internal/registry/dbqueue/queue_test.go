package dbqueue

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSchedulerRunsHigherPriorityWorkFirst(t *testing.T) {
	scheduler := NewScheduler()
	t.Cleanup(scheduler.Close)

	release, err := scheduler.Acquire(context.Background(), PriorityNormal)
	if err != nil {
		t.Fatalf("acquire blocker: %v", err)
	}

	var mu sync.Mutex
	order := make([]string, 0, 2)
	var group sync.WaitGroup
	for _, item := range []struct {
		name     string
		priority Priority
	}{
		{name: "low", priority: PriorityLow},
		{name: "high", priority: PriorityHigh},
	} {
		item := item
		group.Add(1)
		go func() {
			defer group.Done()
			lease, acquireErr := scheduler.Acquire(context.Background(), item.priority)
			if acquireErr != nil {
				t.Errorf("acquire %s: %v", item.name, acquireErr)
				return
			}
			mu.Lock()
			order = append(order, item.name)
			mu.Unlock()
			lease()
		}()
	}

	deadline := time.Now().Add(time.Second)
	for {
		scheduler.mu.Lock()
		queued := scheduler.waiters.Len()
		scheduler.mu.Unlock()
		if queued == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("priority work did not enter the queue")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	group.Wait()

	if len(order) != 2 || order[0] != "high" || order[1] != "low" {
		t.Fatalf("priority order = %v, want [high low]", order)
	}
}

func TestSchedulerCancellationRemovesWaitingWork(t *testing.T) {
	scheduler := NewScheduler()
	t.Cleanup(scheduler.Close)

	release, err := scheduler.Acquire(context.Background(), PriorityNormal)
	if err != nil {
		t.Fatalf("acquire blocker: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	acquireDone := make(chan error, 1)
	go func() {
		_, acquireErr := scheduler.Acquire(ctx, PriorityLow)
		acquireDone <- acquireErr
	}()
	deadline := time.Now().Add(time.Second)
	for {
		scheduler.mu.Lock()
		queued := scheduler.waiters.Len()
		scheduler.mu.Unlock()
		if queued == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled work did not enter the queue")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-acquireDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire error = %v, want context.Canceled", err)
	}
	scheduler.mu.Lock()
	queued := scheduler.waiters.Len()
	scheduler.mu.Unlock()
	if queued != 0 {
		t.Fatalf("cancelled waiter remained queued: %d", queued)
	}
	release()
}

func TestPoolKeepsTransactionLeaseUntilCommit(t *testing.T) {
	dsn := "file:dbqueue-transaction-lease-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	scheduler := NewScheduler()
	t.Cleanup(func() {
		scheduler.Close()
		_ = sqlDB.Close()
	})
	db.ConnPool = Wrap(db.ConnPool, scheduler)

	started := make(chan struct{})
	finish := make(chan struct{})
	transactionDone := make(chan error, 1)
	go func() {
		transactionDone <- db.Transaction(func(tx *gorm.DB) error {
			if _, err := tx.DB(); err != nil {
				return err
			}
			close(started)
			<-finish
			return tx.Exec("SELECT 1").Error
		})
	}()
	<-started

	outsideDone := make(chan error, 1)
	go func() {
		outsideDone <- db.WithContext(WithPriority(context.Background(), PriorityHigh)).Exec("SELECT 1").Error
	}()
	select {
	case err := <-outsideDone:
		t.Fatalf("outside operation completed before transaction commit: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(finish)
	if err := <-transactionDone; err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if err := <-outsideDone; err != nil {
		t.Fatalf("outside operation: %v", err)
	}
}

func TestPoolTransactionRollbackReleasesSchedulerLease(t *testing.T) {
	dsn := "file:dbqueue-transaction-rollback-" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	scheduler := NewScheduler()
	t.Cleanup(func() {
		scheduler.Close()
		_ = sqlDB.Close()
	})
	db.ConnPool = Wrap(db.ConnPool, scheduler)

	transactionErr := errors.New("rollback transaction")
	if err := db.Transaction(func(tx *gorm.DB) error {
		return transactionErr
	}); !errors.Is(err, transactionErr) {
		t.Fatalf("transaction error = %v, want %v", err, transactionErr)
	}
	done := make(chan error, 1)
	go func() {
		done <- db.WithContext(WithPriority(context.Background(), PriorityHigh)).Exec("SELECT 1").Error
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("operation after rollback: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("transaction rollback leaked its scheduler lease")
	}
}

var _ interface {
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
} = (*Pool)(nil)

func TestPoolPinnedConnectionKeepsLeaseUntilCleanupReturns(t *testing.T) {
	db, err := sql.Open("sqlite", "file:pinned-queue-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2) // A spare physical connection cannot bypass the scheduler.
	scheduler := NewScheduler()
	t.Cleanup(func() { scheduler.Close(); _ = db.Close() })
	pool := Wrap(db, scheduler)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	entered := make(chan struct{})
	finish := make(chan struct{})
	pinnedDone, outsideDone := make(chan error, 1), make(chan error, 1)
	go func() {
		pinnedDone <- pool.WithPinnedConnection(WithPriority(ctx, PriorityLow), func(conn *sql.Conn) error {
			close(entered)
			select {
			case <-finish:
			case <-ctx.Done():
				return ctx.Err()
			}
			_, err := conn.ExecContext(ctx, "SELECT 1")
			return err
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("did not get the pinned connection")
	}
	go func() {
		_, err := pool.ExecContext(WithPriority(ctx, PriorityHigh), "SELECT 1")
		outsideDone <- err
	}()
	for {
		scheduler.mu.Lock()
		queued := scheduler.waiters.Len()
		scheduler.mu.Unlock()
		if queued == 1 {
			break
		}
		select {
		case err := <-outsideDone:
			t.Fatalf("outside statement escaped the pinned lease: %v", err)
		case <-ctx.Done():
			t.Fatal("outside statement never entered the queue")
		case <-time.After(time.Millisecond):
		}
	}
	close(finish)
	if err := <-pinnedDone; err != nil {
		t.Fatal(err)
	}
	if err := <-outsideDone; err != nil {
		t.Fatal(err)
	}
}
