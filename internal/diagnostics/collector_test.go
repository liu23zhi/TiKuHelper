package diagnostics

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func newClock() *fakeClock {
	return &fakeClock{at: time.Date(2026, 10, 28, 8, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))}
}
func (f *fakeClock) now() time.Time      { f.mu.Lock(); defer f.mu.Unlock(); return f.at }
func (f *fakeClock) add(d time.Duration) { f.mu.Lock(); defer f.mu.Unlock(); f.at = f.at.Add(d) }

// A request must be visible while running, then have exactly one completion sample.
func TestHTTPRequestLifecycle(t *testing.T) {
	clock := newClock()
	c := NewCollector(clock.now)
	done := c.BeginHTTPRequest()
	active := c.Snapshot()
	if active.HTTP.Total != 1 || active.HTTP.InFlight != 1 || active.HTTP.LatencyCount != 0 {
		t.Fatalf("begin: total/inFlight/count=%d/%d/%d, want 1/1/0", active.HTTP.Total, active.HTTP.InFlight, active.HTTP.LatencyCount)
	}
	clock.add(25 * time.Millisecond)
	done(200)
	s := c.Snapshot()
	if s.HTTP.Total != 1 || s.HTTP.InFlight != 0 || s.HTTP.Failed != 0 || s.HTTP.LatencyCount != 1 || s.HTTP.LatencyTotalMillis != 25 || s.HTTP.LatencyMaxMillis != 25 {
		t.Fatalf("completed HTTP=%+v, want total=1 inFlight=0 failed=0 count=1 totalMillis=25 maxMillis=25", s.HTTP)
	}
	counts := make([]uint64, len(s.HTTP.Buckets))
	for i, b := range s.HTTP.Buckets {
		counts[i] = b.Count
	}
	if !reflect.DeepEqual(counts, []uint64{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}) {
		t.Fatalf("25ms cumulative buckets=%v", counts)
	}
}

// Duplicate finish paths must neither release another request nor double count failures.
func TestHTTPCompletionIdempotentAndStatusBoundary(t *testing.T) {
	for _, tc := range []struct {
		status int
		failed uint64
	}{{0, 0}, {399, 0}, {400, 1}, {499, 1}, {500, 1}} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			clock := newClock()
			c := NewCollector(clock.now)
			done := c.BeginHTTPRequest()
			outstanding := c.BeginHTTPRequest()
			clock.add(15 * time.Millisecond)
			done(tc.status)
			done(599)
			s := c.Snapshot()
			if s.HTTP.Total != 2 || s.HTTP.InFlight != 1 || s.HTTP.Failed != tc.failed || s.HTTP.LatencyCount != 1 || s.HTTP.LatencyTotalMillis != 15 {
				t.Fatalf("status %d duplicate completion: %+v, want total=2 inFlight=1 failed=%d count=1 millis=15", tc.status, s.HTTP, tc.failed)
			}
			outstanding(200)
			if c.Snapshot().HTTP.InFlight != 0 {
				t.Fatal("last request did not release")
			}
		})
	}
}

// Both clock regressions and sub-millisecond durations must remain nonnegative integers.
func TestClockRegressionAndUptime(t *testing.T) {
	clock := newClock()
	c := NewCollector(clock.now)
	done := c.BeginHTTPRequest()
	clock.add(-time.Hour)
	done(200)
	s := c.Snapshot()
	if s.UptimeSeconds != 0 || s.HTTP.LatencyCount != 1 || s.HTTP.LatencyTotalMillis != 0 || s.HTTP.LatencyMaxMillis != 0 || s.HTTP.Buckets[0].Count != 1 {
		t.Fatalf("regressed clock=%+v", s)
	}
	clock.add(time.Hour + 3999*time.Millisecond)
	if got := c.Snapshot().UptimeSeconds; got != 3 {
		t.Fatalf("uptime=%d, want truncated 3", got)
	}
	done = c.BeginHTTPRequest()
	clock.add(999 * time.Microsecond)
	done(200)
	if got := c.Snapshot().HTTP.LatencyTotalMillis; got != 0 {
		t.Fatalf("sub-ms added %d milliseconds", got)
	}
}

// Long requests must still land in the final fixed bucket, without a dynamic label.
func TestLatencyOverflowBucket(t *testing.T) {
	clock := newClock()
	c := NewCollector(clock.now)
	done := c.BeginHTTPRequest()
	clock.add(24 * time.Hour)
	done(503)
	s := c.Snapshot()
	if s.HTTP.LatencyCount != 1 || s.HTTP.LatencyTotalMillis != 86400000 || s.HTTP.LatencyMaxMillis != 86400000 || s.HTTP.Failed != 1 {
		t.Fatalf("long request=%+v", s.HTTP)
	}
	for i, b := range s.HTTP.Buckets {
		want := uint64(0)
		if i == len(s.HTTP.Buckets)-1 {
			want = 1
		}
		if b.Count != want {
			t.Fatalf("bucket %d=%+v, want %d", i, b, want)
		}
	}
}

// Unknown kind/outcome strings must be discarded rather than retained as dimensions.
func TestCacheFixedDimensionsAndUnknownIgnored(t *testing.T) {
	c := NewCollector(newClock().now)
	for _, kind := range []string{"search", "fallback"} {
		for _, outcome := range []string{"hit", "miss", "shared"} {
			c.ObserveCache(kind, outcome)
		}
	}
	for _, tc := range [][2]string{{"private-user-token-question", "hit"}, {"search", "private-route-requestId"}, {"Search", "hit"}, {"fallback", "HIT"}, {"", ""}} {
		c.ObserveCache(tc[0], tc[1])
	}
	want := CacheSnapshot{Search: CacheCounters{Hits: 1, Misses: 1, Shared: 1}, Fallback: CacheCounters{Hits: 1, Misses: 1, Shared: 1}}
	if got := c.Snapshot().Cache; got != want {
		t.Fatalf("cache=%+v, want %+v", got, want)
	}
	payload, err := json.Marshal(c.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "private-") || strings.Contains(string(payload), "requestId") {
		t.Fatalf("input leaked: %s", payload)
	}
}

// A settlement call increments one attempt and exactly one success/failure, not revenue.
func TestBillingCountsSettlementAttempts(t *testing.T) {
	c := NewCollector(newClock().now)
	c.ObserveBilling(true)
	c.ObserveBilling(false)
	c.ObserveBilling(true)
	if got := c.Snapshot().Billing; got != (BillingSnapshot{Attempts: 3, Successes: 2, Failures: 1}) {
		t.Fatalf("billing=%+v, want attempts=3 successes=2 failures=1", got)
	}
}

// Snapshot output must remain the public fixed schema with RFC3339 offset preservation.
func TestSnapshotJSONContract(t *testing.T) {
	c := NewCollector(newClock().now)
	s := c.Snapshot()
	if s.SchemaVersion != 1 || s.StartedAt != "2026-10-28T08:00:00+08:00" {
		t.Fatalf("metadata=%+v", s)
	}
	if _, err := time.Parse(time.RFC3339, s.StartedAt); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err = json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]json.RawMessage) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	if got := keys(root); !reflect.DeepEqual(got, []string{"billing", "cache", "http", "schemaVersion", "startedAt", "uptimeSeconds"}) {
		t.Fatalf("root keys=%v", got)
	}
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"http", []string{"buckets", "failed", "inFlight", "latencyCount", "latencyMaxMillis", "latencyTotalMillis", "total"}},
		{"cache", []string{"fallback", "search"}},
		{"billing", []string{"attempts", "failures", "successes"}},
	} {
		var obj map[string]json.RawMessage
		if err = json.Unmarshal(root[tc.name], &obj); err != nil {
			t.Fatal(err)
		}
		if got := keys(obj); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s keys=%v, want %v", tc.name, got, tc.want)
		}
	}
	if Default == nil || Default.Snapshot().SchemaVersion != 1 {
		t.Fatal("nil/default collector unusable")
	}
	if NewCollector(nil).Snapshot().StartedAt == "" {
		t.Fatal("nil clock did not default to time.Now")
	}
}

// Mutating a response must never mutate internal buckets or another response.
func TestSnapshotBucketsDetached(t *testing.T) {
	c := NewCollector(newClock().now)
	done := c.BeginHTTPRequest()
	done(200)
	s := c.Snapshot()
	s.HTTP.Buckets[0].Count = 999
	s.HTTP.Buckets[0].UpperMillis = 999
	fresh := c.Snapshot()
	if fresh.HTTP.Buckets[0].Count != 1 || fresh.HTTP.Buckets[0].UpperMillis != 5 {
		t.Fatalf("snapshot aliases collector: %+v", fresh.HTTP.Buckets[0])
	}
}

// Concurrent observers, snapshots and duplicate completions must preserve exact totals.
func TestConcurrentObserversAndCompletion(t *testing.T) {
	const workers = 32
	const loops = 40
	clock := newClock()
	c := NewCollector(clock.now)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < loops; j++ {
				done := c.BeginHTTPRequest()
				c.ObserveCache("search", "hit")
				c.ObserveCache("fallback", "shared")
				c.ObserveBilling(j%2 == 0)
				_ = c.Snapshot()
				done(500)
				done(500)
			}
		}()
	}
	wg.Wait()
	s := c.Snapshot()
	if s.HTTP.Total != 1280 || s.HTTP.InFlight != 0 || s.HTTP.Failed != 1280 || s.HTTP.LatencyCount != 1280 || s.HTTP.LatencyTotalMillis != 0 || s.HTTP.LatencyMaxMillis != 0 {
		t.Fatalf("concurrent HTTP=%+v", s.HTTP)
	}
	if s.Cache.Search.Hits != 1280 || s.Cache.Fallback.Shared != 1280 || s.Billing != (BillingSnapshot{Attempts: 1280, Successes: 640, Failures: 640}) {
		t.Fatalf("concurrent cache/billing=%+v/%+v", s.Cache, s.Billing)
	}
	for _, b := range s.HTTP.Buckets {
		if b.Count != 1280 {
			t.Fatalf("concurrent bucket=%+v", b)
		}
	}
}

// Saturation is observable through API updates: counters must never wrap to zero.
func TestCountersSaturateRatherThanWrap(t *testing.T) {
	const max = ^uint64(0)
	clock := newClock()
	c := NewCollector(clock.now)
	c.mu.Lock()
	c.http.Total = max - 1
	c.http.Failed = max - 1
	c.http.LatencyCount = max - 1
	c.http.LatencyTotalMillis = max - 1
	for i := range c.http.Buckets {
		c.http.Buckets[i].Count = max - 1
	}
	c.cache.Search = CacheCounters{Hits: max, Misses: max, Shared: max}
	c.billing = BillingSnapshot{Attempts: max, Successes: max, Failures: max}
	c.mu.Unlock()
	for i := 0; i < 2; i++ {
		done := c.BeginHTTPRequest()
		clock.add(2 * time.Millisecond)
		done(500)
	}
	for _, outcome := range []string{"hit", "miss", "shared"} {
		c.ObserveCache("search", outcome)
	}
	c.ObserveBilling(true)
	c.ObserveBilling(false)
	s := c.Snapshot()
	if s.HTTP.Total != max || s.HTTP.Failed != max || s.HTTP.LatencyCount != max || s.HTTP.LatencyTotalMillis != max || s.HTTP.InFlight != 0 {
		t.Fatalf("wrapped HTTP=%+v", s.HTTP)
	}
	for _, b := range s.HTTP.Buckets {
		if b.Count != max {
			t.Fatalf("wrapped bucket=%+v", b)
		}
	}
	if s.Cache.Search != (CacheCounters{Hits: max, Misses: max, Shared: max}) || s.Billing != (BillingSnapshot{Attempts: max, Successes: max, Failures: max}) {
		t.Fatalf("wrapped cache/billing=%+v/%+v", s.Cache, s.Billing)
	}
}

// Concurrent finish paths for the same request must not release a different request.
func TestConcurrentCompletionSingleRequest(t *testing.T) {
	clock := newClock()
	c := NewCollector(clock.now)
	done := c.BeginHTTPRequest()
	outstanding := c.BeginHTTPRequest()
	clock.add(7 * time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); done(503) }()
	}
	wg.Wait()
	s := c.Snapshot()
	if s.HTTP.Total != 2 || s.HTTP.InFlight != 1 || s.HTTP.Failed != 1 || s.HTTP.LatencyCount != 1 || s.HTTP.LatencyTotalMillis != 7 {
		t.Fatalf("concurrent duplicate completion=%+v", s.HTTP)
	}
	outstanding(200)
	if c.Snapshot().HTTP.InFlight != 0 {
		t.Fatal("unrelated request did not release")
	}
}

// Total duration is a sum of samples, while maximum is the longest sample only.
func TestHTTPLatencyAggregatesSamples(t *testing.T) {
	clock := newClock()
	c := NewCollector(clock.now)
	for _, ms := range []time.Duration{2, 9, 4} {
		done := c.BeginHTTPRequest()
		clock.add(ms * time.Millisecond)
		done(200)
	}
	s := c.Snapshot()
	if s.HTTP.LatencyCount != 3 || s.HTTP.LatencyTotalMillis != 15 || s.HTTP.LatencyMaxMillis != 9 || s.HTTP.Buckets[0].Count != 2 || s.HTTP.Buckets[1].Count != 3 {
		t.Fatalf("aggregated samples=%+v", s.HTTP)
	}
}
