package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/diagnostics"
)

func TestGetSystemMetricsReturnsDetachedReadOnlyAggregates(t *testing.T) {
	old := diagnostics.Default
	fixed := time.Date(2026, 10, 29, 8, 0, 0, 0, time.FixedZone("fixture", 8*60*60))
	collector := diagnostics.NewCollector(func() time.Time { return fixed })
	diagnostics.Default = collector
	t.Cleanup(func() { diagnostics.Default = old })
	collector.BeginHTTPRequest()(503)
	collector.ObserveCache("search", "hit")
	collector.ObserveCache("fallback", "shared")
	collector.ObserveBilling(false)
	before := collector.Snapshot()
	tasksBefore := snapshotTaskMetrics()
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/?role=user&owner=PRIVATE_B04_SENTINEL&requestId=PRIVATE_B04_SENTINEL", nil)
		GetSystemMetrics(c)
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status=%d headers=%v", w.Code, w.Header())
		}
		if strings.Contains(w.Body.String(), "PRIVATE_B04_SENTINEL") {
			t.Fatal("query input leaked into metrics")
		}
		var got systemMetricsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Snapshot, before) || got.Tasks != tasksBefore {
			t.Fatalf("snapshot differs: %#v", got)
		}
		// A caller can mutate a detached response without changing collector state.
		got.HTTP.Buckets[0].Count = 999
	}
	if !reflect.DeepEqual(collector.Snapshot(), before) || snapshotTaskMetrics() != tasksBefore {
		t.Fatal("GET mutated collector or task stores")
	}
}
