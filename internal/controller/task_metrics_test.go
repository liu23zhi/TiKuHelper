package controller

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func withB04TaskStores(t *testing.T) (*searchTaskStore, *aiFallbackTaskStore) {
	t.Helper()
	oldSearch, oldFallback := defaultSearchTaskStore, defaultAIFallbackTaskStore
	s := newSearchTaskStore(time.Minute, func() time.Time { panic("snapshot must not call clock or cleanup") })
	f := newAIFallbackTaskStore(time.Minute, func() time.Time { panic("snapshot must not call clock or cleanup") })
	defaultSearchTaskStore, defaultAIFallbackTaskStore = s, f
	t.Cleanup(func() { defaultSearchTaskStore, defaultAIFallbackTaskStore = oldSearch, oldFallback })
	return s, f
}

func TestB04TaskMetricsRetainedCountsReadOnlyAndNoIdentity(t *testing.T) {
	s, f := withB04TaskStores(t)
	old := time.Now().Add(-time.Hour)
	for _, status := range []string{"running", "succeeded", "failed", "unknown"} {
		id := "private-task-" + status
		s.tasks[id] = searchTask{ID: id, UserID: 991, Status: status, IdempotencyKey: "private-idempotency", RequestFingerprint: "private-question", UpdatedAt: old}
		f.tasks[id] = aiFallbackTask{ID: id, UserID: 992, Status: status, UpdatedAt: old}
		s.terminalTasks[id] = struct{}{}
		f.terminalTasks[id] = struct{}{}
	}
	s.healthTasks[991] = "private-task-succeeded"
	s.idempotency[searchTaskIdempotencyIndex{UserID: 991, Key: "private-idempotency"}] = "private-task-failed"
	f.idempotency[aiFallbackTaskIdempotencyIndex{UserID: 992, Key: "private-idempotency"}] = "private-task-failed"
	snapshot := snapshotTaskMetrics()
	want := TaskCounts{Total: 4, Running: 1, Succeeded: 1, Failed: 1}
	if snapshot.Search != want || snapshot.Fallback != want {
		t.Fatalf("counts include unknown only in total: %+v", snapshot)
	}
	if len(s.tasks) != 4 || len(f.tasks) != 4 || len(s.terminalTasks) != 4 || len(f.terminalTasks) != 4 || len(s.idempotency) != 1 || len(f.idempotency) != 1 || len(s.healthTasks) != 1 {
		t.Fatal("snapshot mutated retained tasks or indexes")
	}
	if s.tasks["private-task-failed"].UpdatedAt != old || f.tasks["private-task-failed"].UpdatedAt != old {
		t.Fatal("snapshot altered task timestamps")
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	expected := `{"search":{"total":4,"running":1,"succeeded":1,"failed":1},"fallback":{"total":4,"running":1,"succeeded":1,"failed":1}}`
	if string(data) != expected || strings.Contains(string(data), "private") {
		t.Fatalf("unexpected public fields: %s", data)
	}
	if !reflect.DeepEqual(snapshot, snapshotTaskMetrics()) {
		t.Fatal("read-only snapshot unstable")
	}
}

func TestB04TaskMetricsConcurrentStoreUpdates(t *testing.T) {
	s, f := withB04TaskStores(t)
	s.tasks["fixture"] = searchTask{Status: searchTaskStatusRunning}
	f.tasks["fixture"] = aiFallbackTask{Status: aiFallbackTaskStatusRunning}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			s.mu.Lock()
			s.tasks["fixture"] = searchTask{Status: searchTaskStatusSucceeded}
			s.mu.Unlock()
			f.mu.Lock()
			f.tasks["fixture"] = aiFallbackTask{Status: aiFallbackTaskStatusFailed}
			f.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			got := snapshotTaskMetrics()
			if got.Search.Total != 1 || got.Fallback.Total != 1 {
				t.Errorf("incoherent per-store read: %+v", got)
			}
		}
	}()
	wg.Wait()
	got := snapshotTaskMetrics()
	if got.Search != (TaskCounts{Total: 1, Succeeded: 1}) || got.Fallback != (TaskCounts{Total: 1, Failed: 1}) {
		t.Fatalf("final counts %+v", got)
	}
}
