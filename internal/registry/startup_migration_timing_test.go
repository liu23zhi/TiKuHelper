package registry

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStartupMigrationSlowLog(t *testing.T) {
	threshold := 1 * time.Second

	if message, slow := startupMigrationSlowLog("m", 999*time.Millisecond, threshold); slow || message != "" {
		t.Fatalf("expected no warning below the threshold, got %q (slow=%v)", message, slow)
	}

	message, slow := startupMigrationSlowLog("ensureBillingClientIPSchema", 12*time.Second, threshold)
	if !slow {
		t.Fatal("expected a warning above the threshold")
	}
	if !strings.Contains(message, "ensureBillingClientIPSchema") || !strings.Contains(message, "12000ms") {
		t.Fatalf("unexpected message: %q", message)
	}

	// 阈值非正时不判断，避免把 0 误当成「永远告警」。
	if _, slow := startupMigrationSlowLog("m", time.Hour, 0); slow {
		t.Fatal("expected no warning with a non-positive threshold")
	}
}

func TestRunStartupMigrationPropagatesErrorAndCallsOnce(t *testing.T) {
	wantErr := errors.New("boom")
	if err := runStartupMigration("m", func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("expected the migration error to be returned unchanged, got %v", err)
	}

	calls := 0
	if err := runStartupMigration("m", func() error {
		calls++
		return nil
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected the migration to run exactly once, got %d", calls)
	}

	if err := runStartupMigration("m", nil); err != nil {
		t.Fatalf("expected a nil migration to be a no-op, got %v", err)
	}
}
