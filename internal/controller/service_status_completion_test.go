package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/diagnostics"
	"github.com/itihey/tikuAdapter/internal/entity"
	"github.com/itihey/tikuAdapter/internal/middleware"
	"github.com/itihey/tikuAdapter/internal/service"
	"github.com/itihey/tikuAdapter/internal/uapi"
)

func TestServiceStatusRealUAPITerminalFailureReasons(t *testing.T) {
	newUAPISearchTestDB(t, "uapi-owned-status-fixture")
	upstreamStatus := atomic.Int32{}
	calls := atomic.Uint32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(upstreamStatus.Load()))
		fmt.Fprint(w, "{\"code\":\"OWNED_FIXTURE_FAILURE\"}")
	}))
	t.Cleanup(upstream.Close)
	prevClient := newUAPISearchClient
	newUAPISearchClient = func(key string) *uapi.Client {
		return uapi.NewClient(key).SetSearchEnginesURL(upstream.URL).SetMaxRetries(0)
	}
	t.Cleanup(func() { newUAPISearchClient = prevClient })

	sampler := service.NewDefaultServiceStatusSampler()
	g := gin.New()
	g.Use(middleware.RequestCorrelation)
	g.GET("/api/uapi/search/engines", func(c *gin.Context) {
		c.Set("user", &entity.User{ID: 1, Role: "admin", Status: "enabled"})
		c.Next()
	}, UAPISearchEngines)
	for _, tc := range []struct {
		upstream, http int
		want           service.ServiceStatusBreakdown
	}{
		{403, 502, service.ServiceStatusBreakdown{Auth: 1}},
		{504, 504, service.ServiceStatusBreakdown{Timeout: 1}},
		{429, 429, service.ServiceStatusBreakdown{Quota: 1}},
		{502, 502, service.ServiceStatusBreakdown{Upstream: 1}},
		{400, 400, service.ServiceStatusBreakdown{}}, // rejected input is not guessed.
	} {
		sampler.Sample()
		before := sampler.Snapshot().Summary
		previousCalls := calls.Load()
		upstreamStatus.Store(int32(tc.upstream))
		response := httptest.NewRecorder()
		g.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/uapi/search/engines", nil))
		sampler.Sample()
		after := sampler.Snapshot().Summary
		diff := service.ServiceStatusBreakdown{
			Auth:     after.Breakdown.Auth - before.Breakdown.Auth,
			Timeout:  after.Breakdown.Timeout - before.Breakdown.Timeout,
			Quota:    after.Breakdown.Quota - before.Breakdown.Quota,
			Upstream: after.Breakdown.Upstream - before.Breakdown.Upstream,
		}
		if response.Code != tc.http || calls.Load()-previousCalls != 1 || after.FailedRequests-before.FailedRequests != 1 ||
			after.TotalRequests-before.TotalRequests != 1 || diff != tc.want {
			t.Fatalf("upstream %d output %d calls=%d admin delta=%+v (want %+v)", tc.upstream, response.Code, calls.Load()-previousCalls, diff, tc.want)
		}
		t.Logf("upstream %d finished HTTP %d, one observed failure, terminal classes delta=%+v", tc.upstream, response.Code, diff)
	}
}

func TestServiceStatusCompletionDoesNotChangeDiagnosticStartSemantics(t *testing.T) {
	s := service.NewDefaultServiceStatusSampler()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	g := gin.New()
	g.Use(middleware.RequestCorrelation)
	g.POST("/api/search/tasks", func(c *gin.Context) { close(started); <-release; c.Status(503) })
	beforeDiag := diagnostics.Default.Snapshot().HTTP
	s.Sample()
	before := s.Snapshot().Summary
	w := httptest.NewRecorder()
	go func() {
		g.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/search/tasks", nil))
		close(done)
	}()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture did not enter")
	}
	s.Sample()
	inFlight := s.Snapshot().Summary
	during := diagnostics.Default.Snapshot().HTTP
	if during.Total != beforeDiag.Total+1 || during.InFlight != beforeDiag.InFlight+1 ||
		during.LatencyCount != beforeDiag.LatencyCount || inFlight.TotalRequests != before.TotalRequests {
		t.Fatalf("in-flight start changed completion counters: diagnostic=%+v status=%+v", during, inFlight)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fixture did not complete")
	}
	s.Sample()
	after := s.Snapshot().Summary
	diag := diagnostics.Default.Snapshot().HTTP
	if w.Code != 503 || diag.Total != beforeDiag.Total+1 || diag.InFlight != beforeDiag.InFlight ||
		diag.LatencyCount != beforeDiag.LatencyCount+1 || after.TotalRequests != before.TotalRequests+1 ||
		after.SuccessRequests != before.SuccessRequests || after.FailedRequests != before.FailedRequests+1 ||
		after.Endpoints.Search.FailedRequests != before.Endpoints.Search.FailedRequests+1 || after.Breakdown != before.Breakdown {
		t.Fatalf("503 must count once and generic 503 remain uncategorized: diagnostic=%+v status=%+v", diag, after)
	}
}

func TestServiceStatusEventOnlyPublicSearchDoesNotExpandLegacyTimeSeries(t *testing.T) {
	at := time.Date(2026, 11, 27, 10, 0, 15, 0, time.UTC)
	now := at
	recorder := &service.ServiceStatusEndpointRecorder{}
	s := service.NewServiceStatusSamplerWithSources(nil, func() time.Time { return now }, service.ServiceStatusSources{
		Completions: recorder, Process: func() service.StatusProcessMetrics { return service.StatusProcessMetrics{Goroutines: 2} },
	})
	s.Sample()
	searchAt, otherAt := at.Add(9*time.Minute), at.Add(10*time.Minute)
	recorder.ObserveCompletion("/api/search/tasks", 503, 12, service.StatusFailureNone, searchAt)
	recorder.ObserveCompletion("/api/owned-fixture-other", 200, 9, service.StatusFailureNone, otherAt)
	now = at.Add(11 * time.Minute) // no periodic ticks between t+0 and this read.
	series := buildServiceStatusSeriesResponse(s)
	summary := buildServiceStatusSummaryResponse(s)
	admin := buildServiceStatusResponse(s)
	if len(series.Points) != 0 || series.Retained != 0 || summary.TotalRequests != 0 || series.Endpoints.Other.TotalRequests != 0 ||
		len(series.SearchPoints) != 2 || series.SearchPoints[1].TotalRequests != 1 || series.SearchPoints[1].Minute != searchAt.Unix()/60 ||
		series.LatestSampleAt == nil || !series.LatestSampleAt.Equal(searchAt) || summary.Endpoints.Search.TotalRequests != 1 ||
		len(admin.Points) != 3 || len(admin.ProcessPoints) != 1 || admin.LatestSampleAt == nil || !admin.LatestSampleAt.Equal(otherAt) {
		t.Fatalf("completion-only observations crossed an exposure boundary: public=%+v summary=%+v admin=%+v", series, summary, admin)
	}
}
