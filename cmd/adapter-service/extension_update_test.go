package main

import (
	"context"
	"testing"
	"time"
)

func TestExtensionUpdaterSharesShutdownContextAndJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := startExtensionUpdates(ctx, func(runContext context.Context, report func(string, error)) {
		if runContext != ctx {
			t.Error("updater does not share shutdown context")
		}
		close(entered)
		<-runContext.Done()
		<-release
	}, nil)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("updater not started")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("join completed before updater exited")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("updater did not join")
	}
}
