package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
)

func TestServiceStatusProviderWindowPrivacyAndProcessHistory(t *testing.T) {
	fixed := time.Date(2026, 11, 27, 13, 4, 30, 123000000, time.FixedZone("fixture UTC+8", 8*3600))
	var mu sync.Mutex
	now := fixed.Add(-26 * time.Hour)
	var counts service.StatusCounters
	var endpoints service.ServiceStatusEndpointCounters
	processReads := 0
	newSampler := func() *service.ServiceStatusSampler {
		return service.NewServiceStatusSamplerWithSources(nil, func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		}, service.ServiceStatusSources{
			Requests:  func() service.StatusCounters { return counts },
			Endpoints: func() service.ServiceStatusEndpointCounters { return endpoints },
			Process: func() service.StatusProcessMetrics {
				processReads++
				return service.StatusProcessMetrics{
					UptimeSeconds: uint64(processReads * 60), Goroutines: 7,
					Memory: service.StatusProcessMemory{HeapAllocBytes: uint64(1000 + processReads)},
				}
			},
		})
	}
	sampler := newSampler()
	originalSampler := defaultServiceStatusSampler
	defaultServiceStatusSampler = func() *service.ServiceStatusSampler {
		mu.Lock()
		defer mu.Unlock()
		return sampler
	}
	t.Cleanup(func() { defaultServiceStatusSampler = originalSampler })
	at := func(offset int) time.Time { return fixed.Add(time.Duration(offset) * time.Minute) }
	minute := func(offset int) int64 { return at(offset).Unix() / 60 }
	setClock := func(at time.Time) { mu.Lock(); now = at; mu.Unlock() }
	sample := func(offset int, search, failed, searchMillis, other, otherMillis uint64) {
		setClock(at(offset))
		for i, delta := range []service.StatusCounters{
			{TotalRequests: search, FailedRequests: failed, LatencyCount: search, LatencyTotalMillis: searchMillis},
			{TotalRequests: other, LatencyCount: other, LatencyTotalMillis: otherMillis},
		} {
			bucket := []*service.StatusCounters{&endpoints.Search, &endpoints.Other}[i]
			bucket.TotalRequests += delta.TotalRequests
			bucket.FailedRequests += delta.FailedRequests
			bucket.LatencyCount += delta.LatencyCount
			bucket.LatencyTotalMillis += delta.LatencyTotalMillis
		}
		counts.TotalRequests += search + other
		counts.FailedRequests += failed
		counts.TimeoutFailures += failed
		counts.LatencyCount += search + other
		counts.LatencyTotalMillis += searchMillis + otherMillis
		sampler.Sample()
	}

	router := gin.New()
	// The owned loopback fixture substitutes identity only. HTTP handlers and
	// the authorization middleware are real; the production router contract
	// separately pins AuthOrAccessToken / Auth + RequireAdmin.
	fixtureIdentity := func(c *gin.Context) {
		switch role := c.GetHeader("X-Fixture-Role"); role {
		case "user", "admin":
			c.Set("user", &entity.User{ID: 12345, Role: role})
			c.Next()
		default:
			c.AbortWithStatus(http.StatusUnauthorized)
		}
	}
	const summaryURL, seriesURL = "/api/service-status/summary", "/api/service-status/series"
	const adminURL, metricsURL = "/api/admin/service-status", "/api/system-metrics"
	router.GET(summaryURL, fixtureIdentity, GetServiceStatusSummary)
	router.GET(seriesURL, fixtureIdentity, GetServiceStatusSeries)
	router.GET(adminURL, fixtureIdentity, middleware.RequireAdmin, GetServiceStatus)
	router.GET(metricsURL, fixtureIdentity, middleware.RequireAdmin, GetSystemMetrics)
	server := httptest.NewServer(router)
	defer server.Close()

	get := func(path, role string, want int, dst any) []byte {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Fixture-Role", role)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want || (want == 200 && resp.Header.Get("Cache-Control") != "no-store") {
			t.Fatalf("%s %s: got %d, wanted %d", role, path, resp.StatusCode, want)
		}
		if dst != nil {
			if err := json.Unmarshal(raw, dst); err != nil {
				t.Fatal(err)
			}
		}
		return raw
	}
	for _, path := range []string{summaryURL, seriesURL, adminURL, metricsURL} {
		get(path, "", 401, nil)
	}
	for _, path := range []string{adminURL, metricsURL} {
		get(path, "user", 403, nil)
	}
	var summary serviceStatusSummaryResponse
	var series serviceStatusSeriesResponse
	var admin serviceStatusResponse
	get(seriesURL, "user", 200, &series)
	if series.SearchPoints == nil || len(series.SearchPoints) != 0 || series.LatestSampleAt != nil {
		t.Fatalf("pre-first-tick response fabricated data: %+v", series)
	}

	sample(-1500, 1, 0, 33, 0, 0) // 25 hours, excluded.
	sample(-1440, 1, 0, 33, 0, 0) // exact minute before retained window.
	sample(-1439, 1, 0, 33, 0, 0) // oldest eligible minute.
	sample(-9, 6, 0, 60, 0, 0)
	sample(-5, 100, 0, 999, 0, 0) // still history, excluded from current five minutes.
	sample(-4, 2, 1, 1, 0, 0)     // count the 0ms observation and retain the fractional remainder.
	sample(-2, 0, 0, 0, 0, 0)     // genuinely sampled idle, not a gap.
	sample(-1, 1, 0, 2, 1, 100)
	sampler.Flush()
	sample(-1, 0, 0, 0, 0, 0) // repeated tick same minute neither duplicates nor recaptures process.
	setClock(fixed)

	publicSummary := get(summaryURL, "user", 200, &summary)
	publicSeries := get(seriesURL, "user", 200, &series)
	get(adminURL, "admin", 200, &admin)
	metrics := get(metricsURL, "admin", 200, nil)
	for _, raw := range [][]byte{publicSummary, publicSeries} {
		for _, key := range []string{"failedRequests", "upstreamFailures", "timeoutFailures", "authFailures", "quotaFailures", "processPoints", "memory", "heapAllocBytes", "goroutines", "uptimeSeconds", "sampledAt"} {
			if bytes.Contains(raw, []byte(`"`+key+`"`)) {
				t.Fatalf("admin-only key %q leaked in %s", key, raw)
			}
		}
	}
	for _, ts := range []time.Time{summary.AsOf, series.AsOf, admin.AsOf} {
		if !ts.Equal(fixed) || ts.Location() != time.UTC {
			t.Fatalf("asOf differs from the UTC clock snapshot: %s", ts)
		}
	}
	for _, ts := range []*time.Time{summary.LatestSampleAt, series.LatestSampleAt, admin.LatestSampleAt} {
		if ts == nil || !ts.Equal(at(-1)) {
			t.Fatalf("latestSampleAt must be actual observation, not asOf: %v", ts)
		}
	}
	wantMinutes := []int64{minute(-1439), minute(-9), minute(-5), minute(-4), minute(-2), minute(-1)}
	var searchMinutes, adminMinutes, processMinutes []int64
	for _, point := range series.SearchPoints {
		searchMinutes = append(searchMinutes, point.Minute)
		if point.Minute == minute(-2) && (point.TotalRequests != 0 || point.SuccessRequests != 0) {
			t.Fatalf("sampled idle must be a real zero: %+v", point)
		}
	}
	for _, point := range admin.Points {
		adminMinutes = append(adminMinutes, point.Minute)
	}
	for _, point := range admin.ProcessPoints {
		processMinutes = append(processMinutes, point.Minute)
		if point.SampledAt.Unix()/60 != point.Minute || point.Goroutines != 7 || point.Memory.HeapAllocBytes < 1003 {
			t.Fatalf("unbacked or time-misaligned runtime observation: %+v", point)
		}
	}
	if !slices.Equal(searchMinutes, wantMinutes) || !slices.Equal(adminMinutes, wantMinutes) || !slices.Equal(processMinutes, wantMinutes) {
		t.Fatalf("24h boundary or missing-minute behavior wrong: search=%v admin=%v process=%v want=%v", searchMinutes, adminMinutes, processMinutes, wantMinutes)
	}
	if len(series.Points) != 5 || series.Retained != 5 || summary.Retained != 5 || series.Endpoints.Other.Samples != 5 {
		t.Fatalf("compat requestful all-site coverage changed: summary=%+v series=%+v", summary, series)
	}
	if summary.WindowMinutes != 5 || summary.TotalRequests != 4 || summary.AvgLatencyMs != 25 ||
		summary.Endpoints.Search.TotalRequests != 3 || summary.Endpoints.Search.SuccessRequests != 2 ||
		summary.Endpoints.Search.AvgLatencyMs != 1 || summary.Endpoints.Search.Samples != 6 ||
		series.Endpoints.Search != summary.Endpoints.Search {
		t.Fatalf("natural window/latency mismatch: summary=%+v series=%+v", summary, series)
	}
	if admin.Summary.PointsUsed != 3 || admin.Summary.Breakdown.Timeout != 1 || admin.Points[3].TimeoutFailures != 1 ||
		admin.ProcessPoints[5].Memory.HeapAllocBytes != 1008 || !bytes.Contains(metrics, []byte("heapAllocBytes")) || processReads != 8 {
		t.Fatalf("admin classification/process series missing, duplicated, or sampled on GET: %+v (processReads=%d)", admin, processReads)
	}

	setClock(fixed.Add(time.Second))
	restarted := newSampler()
	mu.Lock()
	sampler = restarted
	mu.Unlock()
	get(summaryURL, "user", 200, &summary)
	get(seriesURL, "user", 200, &series)
	get(adminURL, "admin", 200, &admin)
	if !series.AsOf.Equal(fixed.Add(time.Second)) || series.RestartedAt != fixed.Add(time.Second).Format(time.RFC3339) ||
		len(series.SearchPoints) != 0 || len(admin.ProcessPoints) != 0 || admin.ProcessPoints == nil ||
		summary.SuccessRateAvailable || summary.Status != "unknown" || series.LatestSampleAt != nil {
		t.Fatalf("restart must clear observations without fabricating healthy state: %+v / %+v", series, admin)
	}
	t.Logf("loopback public=200, anonymous=401, ordinary admin paths=403, admin=200; 24h actual minutes=%v, five-minute search=3/2 avg=1ms, site avg=25ms, process snapshots=6 in range, new sampler empty", searchMinutes)
}

func TestPublicStatusSearchHasNoLegacyFallbackWithoutSource(t *testing.T) {
	now := time.Date(2026, 11, 27, 13, 4, 0, 0, time.UTC)
	sampler := service.NewServiceStatusSampler(nil, func() time.Time { return now }, func() service.StatusCounters { return service.StatusCounters{TotalRequests: 13} })
	sampler.Sample()
	response := buildServiceStatusSeriesResponse(sampler)
	if len(response.Points) != 1 || response.Points[0].TotalRequests != 13 ||
		response.SearchPoints == nil || len(response.SearchPoints) != 0 || response.LatestSampleAt != nil || response.Endpoints.Search.Samples != 0 {
		t.Fatalf("legacy all-site data became search coverage: %+v", response)
	}
}
