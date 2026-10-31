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
	cancel()
	if _, err := scheduler.Acquire(ctx, PriorityLow); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire error = %v, want context.Canceled", err)
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

var _ interface {
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
} = (*Pool)(nil)
