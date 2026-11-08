// Package diagnostics collects bounded, process-local operational counters.
// It stores no user, request, route, question, token, or monetary data.
package diagnostics

import (
	"sync"
	"time"
)

// LatencyBucket is cumulative: Count includes completed requests whose elapsed
// whole milliseconds are <= UpperMillis. The final bucket covers time.Duration's
// maximum representable milliseconds, so it includes every recorded sample.
type LatencyBucket struct {
	UpperMillis uint64 `json:"upperMillis"`
	Count       uint64 `json:"count"`
}

// HTTPSnapshot counts starts separately from completed latency samples.
type HTTPSnapshot struct {
	Total              uint64          `json:"total"`
	InFlight           uint64          `json:"inFlight"`
	Failed             uint64          `json:"failed"`
	LatencyCount       uint64          `json:"latencyCount"`
	LatencyTotalMillis uint64          `json:"latencyTotalMillis"`
	LatencyMaxMillis   uint64          `json:"latencyMaxMillis"`
	Buckets            []LatencyBucket `json:"buckets"`
}

// CacheCounters has only the three permitted outcomes, never dynamic labels.
type CacheCounters struct {
	Hits   uint64 `json:"hits"`
	Misses uint64 `json:"misses"`
	Shared uint64 `json:"shared"`
}

// CacheSnapshot has only the two permitted cache kinds.
type CacheSnapshot struct {
	Search   CacheCounters `json:"search"`
	Fallback CacheCounters `json:"fallback"`
}

// BillingSnapshot counts observed settlement calls and their outcomes.
// These counters are neither accounting evidence nor revenue or amount totals.
type BillingSnapshot struct {
	Attempts  uint64 `json:"attempts"`
	Successes uint64 `json:"successes"`
	Failures  uint64 `json:"failures"`
}

// Snapshot is a detached, fixed-schema view of counters since process start.
// StartedAt preserves the injected clock's offset using RFC3339.
type Snapshot struct {
	SchemaVersion int             `json:"schemaVersion"`
	StartedAt     string          `json:"startedAt"`
	UptimeSeconds uint64          `json:"uptimeSeconds"`
	HTTP          HTTPSnapshot    `json:"http"`
	Cache         CacheSnapshot   `json:"cache"`
	Billing       BillingSnapshot `json:"billing"`
}

// Collector is concurrency-safe. Construct one with NewCollector and do not copy
// it after first use. Every update has fixed memory usage and no dynamic labels.
type Collector struct {
	mu        sync.Mutex
	now       func() time.Time
	startedAt time.Time
	http      HTTPSnapshot
	cache     CacheSnapshot
	billing   BillingSnapshot
}

// Default is the process-local collector for application instrumentation.
// Do not replace it while concurrent application goroutines are using it.
var Default = NewCollector(nil)

// NewCollector starts empty. A nil clock uses time.Now. An injected clock must
// not reenter this Collector; its calls after construction are serialized.
func NewCollector(now func() time.Time) *Collector {
	if now == nil {
		now = time.Now
	}
	c := &Collector{now: now, startedAt: now()}
	for _, bound := range [...]uint64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 9223372036854} {
		c.http.Buckets = append(c.http.Buckets, LatencyBucket{UpperMillis: bound})
	}
	return c
}

// BeginHTTPRequest records one start and returns an idempotent completion hook.
// The first completion status wins; statuses >= 400 count as failed. Callers must
// finish every started request, including panic/error paths. Elapsed durations
// are truncated to whole milliseconds and a regressed clock is clamped to zero.
func (c *Collector) BeginHTTPRequest() func(status int) {
	c.mu.Lock()
	started := c.now()
	c.http.Total = saturatingAdd(c.http.Total, 1)
	c.http.InFlight = saturatingAdd(c.http.InFlight, 1)
	c.mu.Unlock()

	var once sync.Once
	return func(status int) {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			elapsed := nonnegativeDuration(c.now().Sub(started))
			millis := uint64(elapsed / time.Millisecond)
			if c.http.InFlight > 0 {
				c.http.InFlight--
			}
			if status >= 400 {
				c.http.Failed = saturatingAdd(c.http.Failed, 1)
			}
			c.http.LatencyCount = saturatingAdd(c.http.LatencyCount, 1)
			c.http.LatencyTotalMillis = saturatingAdd(c.http.LatencyTotalMillis, millis)
			if millis > c.http.LatencyMaxMillis {
				c.http.LatencyMaxMillis = millis
			}
			for i := range c.http.Buckets {
				if millis <= c.http.Buckets[i].UpperMillis {
					c.http.Buckets[i].Count = saturatingAdd(c.http.Buckets[i].Count, 1)
				}
			}
		})
	}
}

// ObserveCache accepts only kind search/fallback and outcome hit/miss/shared.
// Matching is exact; unknown strings are ignored and never retained or echoed.
func (c *Collector) ObserveCache(kind, outcome string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var counters *CacheCounters
	switch kind {
	case "search":
		counters = &c.cache.Search
	case "fallback":
		counters = &c.cache.Fallback
	default:
		return
	}
	switch outcome {
	case "hit":
		counters.Hits = saturatingAdd(counters.Hits, 1)
	case "miss":
		counters.Misses = saturatingAdd(counters.Misses, 1)
	case "shared":
		counters.Shared = saturatingAdd(counters.Shared, 1)
	}
}

// ObserveBilling records exactly one observed settlement attempt and outcome.
// It must be called at the settlement boundary, not per accounting ledger row.
func (c *Collector) ObserveBilling(success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.billing.Attempts = saturatingAdd(c.billing.Attempts, 1)
	if success {
		c.billing.Successes = saturatingAdd(c.billing.Successes, 1)
	} else {
		c.billing.Failures = saturatingAdd(c.billing.Failures, 1)
	}
}

// Snapshot returns one coherent view. The bucket slice is copied, so callers may
// encode or mutate their response without racing or modifying collector state.
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.http
	h.Buckets = append([]LatencyBucket(nil), h.Buckets...)
	return Snapshot{
		SchemaVersion: 1,
		StartedAt:     c.startedAt.Format(time.RFC3339),
		UptimeSeconds: uint64(nonnegativeDuration(c.now().Sub(c.startedAt)) / time.Second),
		HTTP:          h,
		Cache:         c.cache,
		Billing:       c.billing,
	}
}

func saturatingAdd(current, delta uint64) uint64 {
	if ^uint64(0)-current < delta {
		return ^uint64(0)
	}
	return current + delta
}

func nonnegativeDuration(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}
