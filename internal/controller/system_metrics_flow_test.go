package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/diagnostics"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
)

type metricsFlowProducerCase struct {
	Name    string      `json:"name"`
	Status  int         `json:"status"`
	Headers http.Header `json:"headers"`
	Body    any         `json:"body"`
}

type metricsFlowNoNetwork struct{ calls *atomic.Uint64 }

func (guard metricsFlowNoNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	guard.calls.Add(1)
	return nil, errors.New("system metrics flow fixture forbids real network")
}

// TestSystemMetricsFlowProducer always checks the actual middleware/handler.
// Only the optional output is conditional; it is never an unexecuted skip.
// Authentication is a synthetic context value, not a database/JWT/session claim.
func TestSystemMetricsFlowProducer(t *testing.T) {
	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(oldMode) })
	var networkCalls atomic.Uint64
	oldTransport := http.DefaultTransport
	http.DefaultTransport = metricsFlowNoNetwork{calls: &networkCalls}
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
		if networkCalls.Load() != 0 {
			t.Error("metrics fixture attempted real network")
		}
	})

	const private = "PRODUCER_PRIVATE_METRICS_SENTINEL"
	observed := make([]metricsFlowProducerCase, 0, 3)
	for _, name := range []string{"empty", "populated", "forbidden"} {
		t.Run(name, func(t *testing.T) {
			search, fallback := withB04TaskStores(t)
			now := time.Date(2026, 10, 31, 8, 0, 0, 0, time.FixedZone("synthetic+08", 8*60*60))
			origin := now
			collector := diagnostics.NewCollector(func() time.Time { return now })
			oldCollector := diagnostics.Default
			diagnostics.Default = collector
			t.Cleanup(func() { diagnostics.Default = oldCollector })
			if name != "empty" {
				first := collector.BeginHTTPRequest()
				now = now.Add(12 * time.Millisecond)
				first(http.StatusOK)
				second := collector.BeginHTTPRequest()
				now = now.Add(375 * time.Millisecond)
				second(http.StatusServiceUnavailable)
				now = origin.Add(3 * time.Second)
				unfinished := collector.BeginHTTPRequest()
				t.Cleanup(func() { unfinished(http.StatusOK) })
				for kind, outcomes := range map[string][]string{
					"search":   {"hit", "hit", "miss", "shared"},
					"fallback": {"hit", "miss", "miss", "shared", "shared", "shared"},
				} {
					for _, outcome := range outcomes {
						collector.ObserveCache(kind, outcome)
					}
				}
				collector.ObserveBilling(true)
				collector.ObserveBilling(true)
				collector.ObserveBilling(false)
				stale := origin.Add(-time.Hour)
				for _, status := range []string{"running", "succeeded", "failed", private + "_unknown"} {
					id := private + "_" + status
					search.tasks[id] = searchTask{ID: id, UserID: 77101, Status: status, UpdatedAt: stale, IdempotencyKey: private, RequestFingerprint: private}
					fallback.tasks[id] = aiFallbackTask{ID: id, UserID: 77102, Status: status, UpdatedAt: stale, IdempotencyKey: private, RequestFingerprint: private}
					search.terminalTasks[id] = struct{}{}
					fallback.terminalTasks[id] = struct{}{}
				}
				search.idempotency[searchTaskIdempotencyIndex{UserID: 77101, Key: private}] = private
				search.healthTasks[77101] = private
				fallback.idempotency[aiFallbackTaskIdempotencyIndex{UserID: 77102, Key: private}] = private
			}
			// Unknown labels must not be stored or echoed, even when an otherwise
			// valid cache kind is supplied. No new operational policy is invented.
			beforeUnknown := collector.Snapshot()
			collector.ObserveCache(private, "hit")
			collector.ObserveCache("search", private)
			if !reflect.DeepEqual(beforeUnknown, collector.Snapshot()) {
				t.Fatal("unknown metric labels changed bounded collector state")
			}
			before := collector.Snapshot()
			beforeTasks := snapshotTaskMetrics()
			searchRows, fallbackRows := maps.Clone(search.tasks), maps.Clone(fallback.tasks)
			searchTerminal, fallbackTerminal := maps.Clone(search.terminalTasks), maps.Clone(fallback.terminalTasks)
			searchKeys, fallbackKeys := maps.Clone(search.idempotency), maps.Clone(fallback.idempotency)
			healthKeys := maps.Clone(search.healthTasks)
			role := "admin"
			if name == "forbidden" {
				role = "user"
			}
			var handlerCalls int
			router := gin.New()
			router.Use(middleware.RequestCorrelation)
			router.Use(func(c *gin.Context) {
				c.Set("user", &entity.User{ID: 77103, Username: private, Role: role, Status: "enabled"})
				c.Next()
			})
			router.GET("/adapter-service/system-metrics", middleware.RequireAdmin, func(c *gin.Context) {
				handlerCalls++
				GetSystemMetrics(c)
			})
			request := httptest.NewRequest(http.MethodGet, "/adapter-service/system-metrics?role=admin&owner="+private+"&requestId="+private, nil)
			request.Header.Set("X-Request-ID", private)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(response.Header().Get("X-Request-ID")) {
				t.Fatal("actual correlation middleware did not emit a fresh validated transport ID")
			}
			for _, forbidden := range []string{private, "userId", "taskId", "idempotency", "fingerprint", "question", "owner", "password", "requestId"} {
				if strings.Contains(response.Body.String(), forbidden) {
					t.Fatal("metrics producer body leaked identity, input or private task contents")
				}
			}
			var body any
			decoder := json.NewDecoder(bytes.NewReader(response.Body.Bytes()))
			decoder.UseNumber()
			if err := decoder.Decode(&body); err != nil {
				t.Fatal(err)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				t.Fatal("actual handler emitted trailing non-JSON data")
			}
			after := collector.Snapshot()
			// The read-only handler does not record events itself. Its real outer
			// RequestCorrelation records one start, then one completion AFTER JSON.
			if after.HTTP.Total != before.HTTP.Total+1 || after.HTTP.InFlight != before.HTTP.InFlight || after.HTTP.LatencyCount != before.HTTP.LatencyCount+1 || after.HTTP.LatencyTotalMillis != before.HTTP.LatencyTotalMillis {
				t.Fatal("correlation GET accounting differs from its one start/zero-ms completion")
			}
			if after.Cache != before.Cache || after.Billing != before.Billing {
				t.Fatal("reading metrics altered cache or billing event counts")
			}
			if name == "forbidden" {
				if response.Code != http.StatusForbidden || handlerCalls != 0 || after.HTTP.Failed != before.HTTP.Failed+1 {
					t.Fatal("real RequireAdmin failed to reject synthetic ordinary-user context")
				}
				object, ok := body.(map[string]any)
				if !ok || len(object) != 1 || object["message"] != "无权限执行该操作" || response.Header().Get("Cache-Control") != "" {
					t.Fatal("403 output must be the actual middleware body, not fabricated metrics/no-store")
				}
			} else {
				if response.Code != http.StatusOK || handlerCalls != 1 || response.Header().Get("Cache-Control") != "no-store" || after.HTTP.Failed != before.HTTP.Failed {
					t.Fatal("admin handler status/header/read-only contract failed")
				}
				var got systemMetricsResponse
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				wantDuring := before
				wantDuring.HTTP.Total++
				wantDuring.HTTP.InFlight++
				if !reflect.DeepEqual(got.Snapshot, wantDuring) || got.Tasks != beforeTasks {
					t.Fatal("actual typed JSON differs from injected collector/task state at handler time")
				}
				if name == "empty" {
					if got.HTTP.Total != 1 || got.HTTP.InFlight != 1 || got.HTTP.LatencyCount != 0 || got.Tasks != (TaskMetricsSnapshot{}) {
						t.Fatal("empty sample must retain zero latency despite counting its own in-flight GET")
					}
				} else {
					counts := TaskCounts{Total: 4, Running: 1, Succeeded: 1, Failed: 1}
					if got.Tasks.Search != counts || got.Tasks.Fallback != counts || got.HTTP.Total != 4 || got.HTTP.InFlight != 2 || got.HTTP.Failed != 1 || got.HTTP.LatencyCount != 2 || got.HTTP.LatencyTotalMillis != 387 || got.HTTP.LatencyMaxMillis != 375 || got.UptimeSeconds != 3 {
						t.Fatal("finite populated counters or retained unknown-task semantics changed")
					}
				}
			}
			if snapshotTaskMetrics() != beforeTasks || !reflect.DeepEqual(search.tasks, searchRows) || !reflect.DeepEqual(fallback.tasks, fallbackRows) || !reflect.DeepEqual(search.terminalTasks, searchTerminal) || !reflect.DeepEqual(fallback.terminalTasks, fallbackTerminal) || !reflect.DeepEqual(search.idempotency, searchKeys) || !reflect.DeepEqual(fallback.idempotency, fallbackKeys) || !reflect.DeepEqual(search.healthTasks, healthKeys) {
				t.Fatal("GET or middleware expired/mutated retained tasks, private records or indexes")
			}
			// Output is decoded actual httptest JSON, never wantDuring/counts.
			observed = append(observed, metricsFlowProducerCase{Name: name, Status: response.Code, Headers: response.Header().Clone(), Body: body})
		})
	}
	if len(observed) != 3 || networkCalls.Load() != 0 {
		t.Fatal("all three actual cases and zero external requests are required")
	}
	envelope := struct {
		SchemaVersion int                       `json:"schemaVersion"`
		Cases         []metricsFlowProducerCase `json:"cases"`
	}{SchemaVersion: 1, Cases: observed}
	output, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if destination := os.Getenv("TIKU_METRICS_FLOW_OUTPUT"); destination != "" {
		if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination || filepath.Ext(destination) != ".json" {
			t.Fatal("opt-in metrics output requires a fresh absolute temporary JSON path")
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
		if err != nil {
			t.Fatal(err)
		}
		temporaryRoot, err := filepath.EvalSymlinks(os.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(temporaryRoot, parent)
		if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || parent == temporaryRoot {
			t.Fatal("output parent must be an independent existing directory beneath temporary root")
		}
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeError := file.Write(append(output, '\n'))
		closeError := file.Close()
		if writeError != nil || closeError != nil {
			t.Fatalf("write actual producer envelope: write=%v close=%v", writeError, closeError)
		}
	}
	t.Log("actual GetSystemMetrics JSON produced: empty/populated/admin and ordinary-user403; RequestCorrelation counts its own GET; authentication context synthetic; network/DB/listener0")
}
