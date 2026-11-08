package api

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func b04RequireKeys(t *testing.T, raw json.RawMessage, want ...string) map[string]json.RawMessage {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sort.Strings(want)
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("schema keys=%v want=%v", keys, want)
	}
	return got
}

func b04RequireCounts(t *testing.T, raw json.RawMessage, want ...string) {
	t.Helper()
	for name, value := range b04RequireKeys(t, raw, want...) {
		var n uint64
		if err := json.Unmarshal(value, &n); err != nil {
			t.Fatalf("%s must be an observed unsigned count: %s", name, value)
		}
	}
}

func TestB04SystemMetricsProductionRouteAdminAuthorizationAndSchema(t *testing.T) {
	if b04RunIsolatedManager(t) {
		return
	}
	observations := b04ObserveProductionRoute(t)
	expected := map[string]int{"anonymous": 401, "user-query-admin": 403, "admin": 200, "admin-cookie": 200, "disabled-admin": 401, "invalid-header-beats-cookie": 401}
	for _, o := range observations {
		if o.Status != expected[o.Case] {
			t.Fatalf("%s real route status=%d want=%d body=%s", o.Case, o.Status, expected[o.Case], o.Body)
		}
		if o.Status != 200 {
			denied := b04RequireKeys(t, o.Body, "message")
			if len(denied["message"]) == 0 || strings.Contains(string(o.Body), "schemaVersion") {
				t.Fatal("denied response leaked diagnostics")
			}
			continue
		}
		if o.CacheControl != "no-store" {
			t.Fatalf("admin metrics must not be cached across accounts: %q", o.CacheControl)
		}
		data := b04RequireKeys(t, o.Body, "schemaVersion", "startedAt", "uptimeSeconds", "http", "cache", "billing", "tasks")
		if string(data["schemaVersion"]) != "1" {
			t.Fatalf("schemaVersion=%s", data["schemaVersion"])
		}
		var started string
		if err := json.Unmarshal(data["startedAt"], &started); err != nil {
			t.Fatal(err)
		}
		if _, err := time.Parse(time.RFC3339, started); err != nil {
			t.Fatalf("startedAt=%q: %v", started, err)
		}
		var uptime uint64
		if err := json.Unmarshal(data["uptimeSeconds"], &uptime); err != nil {
			t.Fatal(err)
		}
		h := b04RequireKeys(t, data["http"], "total", "inFlight", "failed", "latencyCount", "latencyTotalMillis", "latencyMaxMillis", "buckets")
		for _, name := range []string{"total", "inFlight", "failed", "latencyCount", "latencyTotalMillis", "latencyMaxMillis"} {
			var n uint64
			if err := json.Unmarshal(h[name], &n); err != nil {
				t.Fatal(err)
			}
		}
		var buckets []json.RawMessage
		if err := json.Unmarshal(h["buckets"], &buckets); err != nil {
			t.Fatal(err)
		}
		if len(buckets) != 12 {
			t.Fatalf("fixed latency buckets=%d want=12", len(buckets))
		}
		for _, b := range buckets {
			b04RequireCounts(t, b, "upperMillis", "count")
		}
		cache := b04RequireKeys(t, data["cache"], "search", "fallback")
		for _, kind := range []string{"search", "fallback"} {
			b04RequireCounts(t, cache[kind], "hits", "misses", "shared")
		}
		b04RequireCounts(t, data["billing"], "attempts", "successes", "failures")
		tasks := b04RequireKeys(t, data["tasks"], "search", "fallback")
		for _, kind := range []string{"search", "fallback"} {
			b04RequireCounts(t, tasks[kind], "total", "running", "succeeded", "failed")
		}
		for _, private := range []string{"B04_QUERY_SENTINEL", "b04-admin", "b04-user", "B04_SYNTHETIC_CREDENTIAL_VERSION", "owner", "requestId", "route", "question", "token", "amount", "income", "revenue"} {
			if strings.Contains(string(o.Body), private) {
				t.Fatalf("private or unbounded field leaked: %s", private)
			}
		}
	}
}
