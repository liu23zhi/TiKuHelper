package controller

import (
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/diagnostics"
	internalsearch "github.com/itihey/tikuAdapter/internal/search"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/pkg/model"
)

// Tests use before/after deltas, never reset or replace the process collector.
func TestB04SearchLookupMetrics(t *testing.T) {
	searchResultCache.clear()
	defer searchResultCache.clear()
	before := diagnostics.Default.Snapshot().Cache.Search
	if _, ok := loadCachedSearchResult("missing", 0); ok {
		t.Fatal("missing cache hit")
	}
	searchResultCache.put("hit", searchAnswerPayload{answers: [][]string{{"fixture"}}}, 0, nil)
	answers, ok := loadCachedSearchResult("hit", 0)
	if !ok {
		t.Fatal("valid cache miss")
	}
	answers[0][0] = "changed"
	cached, _ := searchResultCache.get("hit", 0)
	if cached.answers[0][0] != "fixture" {
		t.Fatal("cache no longer detached")
	}
	searchResultCache.put("empty", searchAnswerPayload{answers: [][]string{{}}}, 0, nil)
	if _, ok := loadCachedSearchResult("empty", 0); ok {
		t.Fatal("empty cache hit")
	}
	searchResultCache.mu.Lock()
	searchResultCache.entries["expired"] = resultCacheEntry[searchAnswerPayload]{value: searchAnswerPayload{answers: [][]string{{"fixture"}}}, expiresAt: time.Now().Add(-time.Hour)}
	searchResultCache.mu.Unlock()
	if _, ok := loadCachedSearchResult("expired", 0); ok {
		t.Fatal("expired cache hit")
	}
	if _, ok := loadCachedSearchResult("hit", 1); ok {
		t.Fatal("wrong epoch cache hit")
	}
	after := diagnostics.Default.Snapshot().Cache.Search
	if after.Hits-before.Hits != 1 || after.Misses-before.Misses != 4 || after.Shared != before.Shared {
		t.Fatalf("unexpected lookup delta before=%+v after=%+v", before, after)
	}
}

func TestB04FallbackLookupMetricsPreserveTypedUsage(t *testing.T) {
	fallbackResultCache.clear()
	defer fallbackResultCache.clear()
	before := diagnostics.Default.Snapshot().Cache.Fallback
	req := model.SearchRequest{Question: "fixture", Type: 2}
	if _, ok := loadCachedAIFallbackResult(17, req); ok {
		t.Fatal("missing cache hit")
	}
	source := service.AIFallbackResult{Success: true, Result: &service.AIFallbackAnswer{Answer: "fixture"}, Model: "fixture-model", UnpricedModel: true, Usage: internalsearch.AISearchUsage{InputTokens: 11, OutputTokens: 7, TotalTokens: 18, SearchCalls: []internalsearch.AISearchCallUsage{{Query: "fixture"}}}, Warnings: []string{"fixture"}}
	storeCachedAIFallbackResult(17, req, source)
	got, ok := loadCachedAIFallbackResult(17, req)
	if !ok || got.Model != source.Model || !got.UnpricedModel || !reflect.DeepEqual(got.Usage, source.Usage) {
		t.Fatalf("typed usage lost: %+v", got)
	}
	got.Result.Answer = "mutated"
	got.Warnings[0] = "mutated"
	got.Usage.SearchCalls[0].Query = "mutated"
	key, epoch, _ := buildFallbackResultCacheKey(17, req)
	entry, _ := fallbackResultCache.get(key, epoch)
	if entry.result.Result.Answer != "fixture" || entry.result.Warnings[0] != "fixture" || entry.result.Usage.SearchCalls[0].Query != "fixture" {
		t.Fatal("cached payload shares mutable fixture data")
	}
	fallbackResultCache.put(key, fallbackCachedValue{}, epoch, nil)
	if _, ok := loadCachedAIFallbackResult(17, req); ok {
		t.Fatal("unsuccessful cache hit")
	}
	fallbackResultCache.mu.Lock()
	fallbackResultCache.entries[key] = resultCacheEntry[fallbackCachedValue]{value: fallbackCachedValue{result: source}, expiresAt: time.Now().Add(-time.Hour)}
	fallbackResultCache.mu.Unlock()
	if _, ok := loadCachedAIFallbackResult(17, req); ok {
		t.Fatal("expired cache hit")
	}
	after := diagnostics.Default.Snapshot().Cache.Fallback
	if after.Hits-before.Hits != 1 || after.Misses-before.Misses != 3 || after.Shared != before.Shared {
		t.Fatalf("unexpected lookup delta before=%+v after=%+v", before, after)
	}
}

func TestB04SearchBypassDoesNotSample(t *testing.T) {
	gin.SetMode(gin.TestMode)
	withStubSearchDependencies(t, (&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{SearchMode: searchModeNormal, Tokens: map[string]string{"icodef": "fixture"}, UserID: 17}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) { return [][]string{{"fixture"}}, nil }}
		}, nil,
		func() {
			before := diagnostics.Default.Snapshot().Cache.Search
			performSearchRequest(t, "/api/search?use=icodef&noRecord=1", `{"question":"fixture","type":2,"skipCache":true}`, "Basic fixture")
			after := diagnostics.Default.Snapshot().Cache.Search
			if after != before {
				t.Fatalf("bypass fabricated cache sample before=%+v after=%+v", before, after)
			}
		})
}

func TestB04FlightCountsOnlyUsableBorrowedResults(t *testing.T) {
	before := diagnostics.Default.Snapshot().Cache
	// A real wait times out without counting joining or timeout as sharing.
	pendingSearch := &flightCall[searchFlightPayload]{done: make(chan struct{})}
	if got := waitSearchFlightResult("fixture-timeout", pendingSearch, time.Nanosecond); got != nil {
		t.Fatal("search timeout returned result")
	}
	pendingFallback := &flightCall[fallbackFlightPayload]{done: make(chan struct{})}
	if got := waitFallbackFlightResult("fixture-timeout", pendingFallback, time.Nanosecond); got != nil {
		t.Fatal("fallback timeout returned result")
	}
	for _, tc := range []struct {
		ok      bool
		answers [][]string
	}{{false, [][]string{{"fixture"}}}, {true, nil}, {true, [][]string{{}}}, {true, [][]string{{"fixture"}}}} {
		call := &flightCall[searchFlightPayload]{done: make(chan struct{}), ok: tc.ok, value: searchFlightPayload{answers: tc.answers}}
		call.once.Do(func() { close(call.done) })
		got := waitSearchFlightResult("fixture", call, time.Second)
		if (got != nil) != (tc.ok && hasUsableSearchAnswers(tc.answers)) {
			t.Fatal("search delivery changed")
		}
	}
	for _, tc := range []struct {
		ok     bool
		result *service.AIFallbackResult
	}{{false, &service.AIFallbackResult{Success: true}}, {true, nil}, {true, &service.AIFallbackResult{Success: false}}, {true, &service.AIFallbackResult{Success: true, Model: "fixture", Usage: internalsearch.AISearchUsage{TotalTokens: 18}}}} {
		call := &flightCall[fallbackFlightPayload]{done: make(chan struct{}), ok: tc.ok, value: fallbackFlightPayload{result: tc.result}}
		call.once.Do(func() { close(call.done) })
		got := waitFallbackFlightResult("fixture", call, time.Second)
		if tc.ok && tc.result != nil {
			if got != tc.result {
				t.Fatal("fallback delivery changed")
			}
		} else if got != nil {
			t.Fatal("fallback failed wait delivered result")
		}
	}
	after := diagnostics.Default.Snapshot().Cache
	if after.Search.Shared-before.Search.Shared != 1 || after.Fallback.Shared-before.Fallback.Shared != 1 {
		t.Fatalf("invalid shared samples before=%+v after=%+v", before, after)
	}
	if after.Search.Hits != before.Search.Hits || after.Search.Misses != before.Search.Misses || after.Fallback.Hits != before.Fallback.Hits || after.Fallback.Misses != before.Fallback.Misses {
		t.Fatal("singleflight double counted cache lookup")
	}
}

func TestB04ConcurrentCacheAndSharedMetrics(t *testing.T) {
	searchResultCache.clear()
	defer searchResultCache.clear()
	searchResultCache.put("fixture", searchAnswerPayload{answers: [][]string{{"fixture"}}}, 0, nil)
	ready := &flightCall[searchFlightPayload]{done: make(chan struct{}), ok: true, value: searchFlightPayload{answers: [][]string{{"fixture"}}}}
	ready.once.Do(func() { close(ready.done) })
	before := diagnostics.Default.Snapshot().Cache.Search
	const callers = 8
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loadCachedSearchResult("fixture", 0)
			loadCachedSearchResult("missing", 0)
			waitSearchFlightResult("fixture", ready, time.Second)
			_ = diagnostics.Default.Snapshot()
		}()
	}
	wg.Wait()
	after := diagnostics.Default.Snapshot().Cache.Search
	if after.Hits-before.Hits != callers || after.Misses-before.Misses != callers || after.Shared-before.Shared != callers {
		t.Fatalf("lost concurrent update before=%+v after=%+v", before, after)
	}
}

func TestB04SearchActualLookupMetricsAndOneBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream, bills := 0, 0
	original := searchBillingReporter
	searchBillingReporter = func(searchBillingContext) { bills++ }
	defer func() { searchBillingReporter = original }()
	withStubSearchDependencies(t, (&stubSearchClient{}).SearchAnswer,
		func(string, string, url.Values) (resolvedSearchRequestContext, error) {
			return resolvedSearchRequestContext{SearchMode: searchModeNormal, Tokens: map[string]string{"icodef": "fixture"}, UserID: 17}, nil
		},
		func(resolvedSearchRequestContext, string) []func(model.SearchRequest) ([][]string, error) {
			return []func(model.SearchRequest) ([][]string, error){func(model.SearchRequest) ([][]string, error) { upstream++; return [][]string{{"fixture"}}, nil }}
		}, nil,
		func() {
			before := diagnostics.Default.Snapshot().Cache.Search
			for i := 0; i < 2; i++ {
				performSearchRequest(t, "/api/search?use=icodef&noRecord=1", `{"question":"fixture","type":2}`, "Basic fixture")
			}
			after := diagnostics.Default.Snapshot().Cache.Search
			if after.Hits-before.Hits != 1 || after.Misses-before.Misses != 1 || after.Shared != before.Shared {
				t.Fatalf("real Search lookup not instrumented exactly once before=%+v after=%+v", before, after)
			}
		})
	if upstream != 1 || bills != 1 {
		t.Fatalf("duplicate execution or billing upstream=%d bills=%d", upstream, bills)
	}
}

func TestAIFallbackCacheDetachesModelCallsTraceAndOCR(t *testing.T) {
	fallbackResultCache.clear()
	defer fallbackResultCache.clear()
	req := model.SearchRequest{Question: "synthetic-clone-boundaries", Type: 2}
	source := service.AIFallbackResult{Success: true, Model: "synthetic-model", Usage: internalsearch.AISearchUsage{
		ModelCalls: []internalsearch.AIModelCallUsage{{Model: "synthetic-model", TotalTokens: 18}},
		Trace:      []internalsearch.AISearchTrace{{RequestBody: "private-synthetic-request", RawResponse: "private-synthetic-response", FullResponse: "private-synthetic-full", Model: "synthetic-model"}},
		OCRCalls:   []internalsearch.AIOCRCallUsage{{ImageURL: "https://example.invalid/image.png", Status: "success", Cached: true}},
	}}
	storeCachedAIFallbackResult(17, req, source)
	source.Usage.ModelCalls[0].TotalTokens = 999
	source.Usage.Trace[0].RequestBody = "mutated-source"
	source.Usage.Trace[0].RawResponse = "mutated-source"
	source.Usage.Trace[0].FullResponse = "mutated-source"
	source.Usage.OCRCalls[0].ImageURL = "mutated-source"
	assertOriginal := func(label string, got service.AIFallbackResult) {
		t.Helper()
		if len(got.Usage.ModelCalls) != 1 || got.Usage.ModelCalls[0].TotalTokens != 18 || len(got.Usage.Trace) != 1 || got.Usage.Trace[0].RequestBody != "private-synthetic-request" || got.Usage.Trace[0].RawResponse != "private-synthetic-response" || got.Usage.Trace[0].FullResponse != "private-synthetic-full" || len(got.Usage.OCRCalls) != 1 || got.Usage.OCRCalls[0].ImageURL != "https://example.invalid/image.png" || !got.Usage.OCRCalls[0].Cached {
			t.Errorf("%s shares mutable usage slices or lost private diagnostics: %+v", label, got.Usage)
		}
	}
	borrowed, ok := loadCachedAIFallbackResult(17, req)
	if !ok {
		t.Fatal("synthetic successful cache entry missing")
	}
	assertOriginal("source-to-cache boundary", borrowed)
	borrowed.Usage.ModelCalls[0].TotalTokens = 777
	borrowed.Usage.Trace[0].RequestBody = "mutated-borrower"
	borrowed.Usage.Trace[0].RawResponse = "mutated-borrower"
	borrowed.Usage.Trace[0].FullResponse = "mutated-borrower"
	borrowed.Usage.OCRCalls[0].ImageURL = "mutated-borrower"
	again, ok := loadCachedAIFallbackResult(17, req)
	if !ok {
		t.Fatal("cache entry lost after borrower mutation")
	}
	assertOriginal("cache-to-borrower boundary", again)
	if !t.Failed() {
		t.Log("FALLBACK_CLONE ModelCalls/Trace/OCR detached on store+load; private RequestBody/RawResponse/FullResponse retained")
	}
}
