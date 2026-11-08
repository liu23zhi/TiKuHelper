package controller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

func producerTrackerActive(tracker *searchExecutionOwnerTracker) int {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.active
}

// This test-only tracker observes the same production wait API. The callback is
// only a blocked in-memory run, never a provider/HTTP/DB or cancellation policy.
func scopedProducerTracker(t *testing.T) *searchExecutionOwnerTracker {
	t.Helper()
	previous := searchExecutionOwners
	tracker := newSearchExecutionOwnerTracker()
	searchExecutionOwners = tracker
	t.Cleanup(func() { searchExecutionOwners = previous })
	return tracker
}

func waitForProducerDrainAssertion(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := WaitForSearchExecutions(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("admitted blocked task was reported idle, drain error=%v", err)
	}
}

func TestSearchTaskShutdownOwnerTracksNewAdmissionReplayAndFailure(t *testing.T) {
	tracker := scopedProducerTracker(t)
	store := newSearchTaskStore(searchTaskDefaultTTL, time.Now)
	var forbidden atomic.Bool
	rejected := errors.New("synthetic admission rejected")
	if _, _, err := store.createWithAdmission(17, "reject", "reject-fingerprint", func() error { return rejected }, func() (model.SearchResponse, error) {
		forbidden.Store(true)
		return model.SearchResponse{}, nil
	}); !errors.Is(err, rejected) {
		t.Fatalf("admission error changed: %v", err)
	}
	if got := producerTrackerActive(tracker); got != 0 || forbidden.Load() {
		t.Fatalf("rejected task launched an owner/run: active=%d ran=%v", got, forbidden.Load())
	}
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Error("synthetic search producer did not return during cleanup")
		}
	})
	id, reused, err := store.createWithAdmission(17, "key", "fingerprint", nil, func() (model.SearchResponse, error) {
		close(entered)
		<-release
		close(returned)
		return model.SearchResponse{}, nil
	})
	if err != nil || reused || id == "" {
		t.Fatalf("new task admission failed: id=%q reused=%v error=%v", id, reused, err)
	}
	<-entered
	beforeReplay := producerTrackerActive(tracker)
	replayed, reused, err := store.createWithAdmission(17, "key", "fingerprint", func() error {
		forbidden.Store(true)
		return nil
	}, func() (model.SearchResponse, error) {
		forbidden.Store(true)
		return model.SearchResponse{}, nil
	})
	if err != nil || !reused || replayed != id || forbidden.Load() || producerTrackerActive(tracker) != beforeReplay {
		t.Fatalf("replay introduced admission/owner/work: reused=%v id-match=%v error=%v ran=%v", reused, replayed == id, err, forbidden.Load())
	}
	waitForProducerDrainAssertion(t)
	select {
	case <-returned:
		t.Error("waiting for drain released/canceled the admitted synthetic run")
	default:
	}
	unblock()
	<-returned
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := WaitForSearchExecutions(ctx); err != nil || producerTrackerActive(tracker) != 0 {
		t.Fatalf("finished task owner did not drain: error=%v active=%d", err, producerTrackerActive(tracker))
	}
}

func TestAIFallbackTaskShutdownOwnerTracksNewAdmissionAndReplay(t *testing.T) {
	tracker := scopedProducerTracker(t)
	store := newAIFallbackTaskStore(aiFallbackTaskDefaultTTL, time.Now)
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	var forbidden atomic.Bool
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Error("synthetic fallback producer did not return during cleanup")
		}
	})
	id, reused, err := store.createIdempotent(17, "key", "fingerprint", func(string) service.AIFallbackResult {
		close(entered)
		<-release
		close(returned)
		return service.AIFallbackResult{Success: true}
	})
	if err != nil || reused || id == "" {
		t.Fatalf("new fallback admission failed: id=%q reused=%v error=%v", id, reused, err)
	}
	<-entered
	beforeReplay := producerTrackerActive(tracker)
	replayed, reused, err := store.createIdempotent(17, "key", "fingerprint", func(string) service.AIFallbackResult {
		forbidden.Store(true)
		return service.AIFallbackResult{}
	})
	if err != nil || !reused || replayed != id || forbidden.Load() || producerTrackerActive(tracker) != beforeReplay {
		t.Fatalf("fallback replay introduced owner/work: reused=%v id-match=%v error=%v ran=%v", reused, replayed == id, err, forbidden.Load())
	}
	if _, _, err := store.createIdempotent(17, "key", "different-fingerprint", func(string) service.AIFallbackResult {
		forbidden.Store(true)
		return service.AIFallbackResult{}
	}); !errors.Is(err, errAIFallbackTaskIdempotencyConflict) || forbidden.Load() || producerTrackerActive(tracker) != beforeReplay {
		t.Fatalf("fallback conflict introduced owner/work: error=%v ran=%v", err, forbidden.Load())
	}
	waitForProducerDrainAssertion(t)
	select {
	case <-returned:
		t.Error("waiting for drain released/canceled the admitted fallback run")
	default:
	}
	unblock()
	<-returned
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := WaitForSearchExecutions(ctx); err != nil || producerTrackerActive(tracker) != 0 {
		t.Fatalf("finished fallback owner did not drain: error=%v active=%d", err, producerTrackerActive(tracker))
	}
}
