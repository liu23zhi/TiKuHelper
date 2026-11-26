package service

import (
	"net/http"
	"testing"
	"time"
)

func TestServiceStatusCompletionUsesActualMinuteAcrossMissingTicks(t *testing.T) {
	base := time.Date(2026, 11, 27, 10, 0, 15, 0, time.UTC)
	now := base
	recorder := &ServiceStatusEndpointRecorder{}
	probeReads := 0
	s := NewServiceStatusSamplerWithSources(nil, func() time.Time { return now }, ServiceStatusSources{
		Completions: recorder,
		Process: func() StatusProcessMetrics {
			probeReads++
			return StatusProcessMetrics{Goroutines: 3}
		},
	})
	s.Sample()
	recorder.ObserveCompletion("/api/search/tasks", 503, 123, StatusFailureNone, base.Add(time.Minute))
	recorder.ObserveCompletion("/api/non-search", 200, 100, StatusFailureNone, base.Add(9*time.Minute))
	recorder.ObserveCompletion("/api/search/tasks", 504, 21, StatusFailureTimeout, base.Add(10*time.Minute))
	recorder.ObserveCompletion("/api/search/tasks", 200, 0, StatusFailureNone, base.Add(10*time.Minute+5*time.Second))
	now = base.Add(12 * time.Minute)
	s.Sample()

	view := s.Snapshot()
	if len(view.Points) != 5 || len(view.ProcessPoints()) != 2 || probeReads != 2 {
		t.Fatalf("missing ticks must not fabricate points or process observations: %+v process=%+v", view.Points, view.ProcessPoints())
	}
	// old completion stays at t+1, the other-only evidence cannot prove search
	// coverage, and a real recent completion still counts despite no tick.
	old, other, recent, idle := view.Points[1], view.Points[2], view.Points[3], view.Points[4]
	if old.Minute != base.Add(time.Minute).Unix()/60 || old.AvgLatencyMs != 123 || old.FailedRequests != 1 ||
		other.Endpoints.Search.Samples != 0 || !other.searchSampledAt.IsZero() ||
		recent.Minute != base.Add(10*time.Minute).Unix()/60 || recent.Endpoints.Search.TotalRequests != 2 ||
		recent.TimeoutFailures != 1 || recent.PeriodicallyObserved() ||
		!idle.PeriodicallyObserved() || idle.TotalRequests != 0 || idle.Endpoints.Search.Samples != 1 {
		t.Fatalf("completion attribution/coverage wrong: %+v", view.Points)
	}
	if view.Summary.Endpoints.Search.TotalRequests != 2 || view.Summary.Endpoints.Search.FailedRequests != 1 ||
		view.Summary.Endpoints.Search.AvgLatencyMs != 10 || view.Summary.Breakdown.Timeout != 1 ||
		view.LegacyTrafficSummary().TotalRequests != 0 {
		t.Fatalf("outdated or unauthorized legacy traffic, or missing recent events: %+v", view.Summary)
	}
	t.Logf("11-minute-old failure remains at its actual minute; recent search=%d, failed=%d, avg=%dms; only two process ticks, legacy=%d",
		view.Summary.Endpoints.Search.TotalRequests, view.Summary.Endpoints.Search.FailedRequests,
		view.Summary.Endpoints.Search.AvgLatencyMs, view.LegacyTrafficSummary().TotalRequests)
}

func TestServiceStatusCumulativeGapKeepsUnknownAndLaterRecovers(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 15, 0, time.UTC)
	var count StatusCounters
	s := NewServiceStatusSamplerWithSources(nil, func() time.Time { return now }, ServiceStatusSources{
		Requests: func() StatusCounters { return count },
		Endpoints: func() ServiceStatusEndpointCounters {
			return ServiceStatusEndpointCounters{Search: count}
		},
		Process: func() StatusProcessMetrics { return StatusProcessMetrics{Goroutines: 1} },
	})
	s.Sample()
	now = now.Add(12 * time.Minute)
	count = StatusCounters{TotalRequests: 1, FailedRequests: 1, LatencyCount: 1, LatencyTotalMillis: 123}
	s.Sample()
	unknown := s.Snapshot()
	if len(unknown.Points) != 1 || len(unknown.ProcessPoints()) != 2 || unknown.Summary.PointsUsed != 0 ||
		unknown.Summary.Endpoints.Search.TotalRequests != 0 || unknown.LatestSearchSampleAt.Equal(now) {
		t.Fatalf("old cumulative delta became current traffic or manufactured idle: %+v", unknown)
	}
	now = now.Add(time.Second)
	count.TotalRequests++
	count.LatencyCount++
	count.LatencyTotalMillis += 9
	s.Sample()
	recovered := s.Snapshot()
	if count := recovered.Summary.Endpoints.Search; count.TotalRequests != 1 || count.SuccessRequests != 1 || count.AvgLatencyMs != 9 {
		t.Fatalf("known new traffic was lost while excluding unknowable delta: %+v", recovered.Summary)
	}
}

func TestServiceStatusCompletionOnlyClassifiesFailuresAndBoundsRing(t *testing.T) {
	clock := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	recorder := &ServiceStatusEndpointRecorder{}
	recorder.ObserveCompletion("/api/search/tasks", http.StatusOK, 4, StatusFailureAuth, clock)
	recorder.ObserveCompletion("/api/search/tasks", http.StatusServiceUnavailable, -1, StatusFailureNone, clock)
	recorder.ObserveCompletion("/api/search/tasks", http.StatusBadGateway, 6, StatusFailureUpstream, clock)
	initial := recorder.completedBetween(serviceStatusMinute(clock), serviceStatusMinute(clock))
	if len(initial) != 1 || initial[0].all.TotalRequests != 3 || initial[0].all.FailedRequests != 2 ||
		initial[0].all.UpstreamFailures != 1 || initial[0].all.AuthFailures != 0 || initial[0].all.LatencyTotalMillis != 10 {
		t.Fatalf("classified an unrelated or successful terminal response: %+v", initial)
	}
	for i := 1; i <= ServiceStatusSlots+2; i++ {
		recorder.ObserveCompletion("/api/search/tasks", http.StatusOK, 1, StatusFailureNone, clock.Add(time.Duration(i)*time.Minute))
	}
	recorder.ObserveCompletion("/api/search/tasks", 503, 123, StatusFailureNone, clock) // late, already overwritten.
	now := clock.Add(time.Duration(ServiceStatusSlots+2) * time.Minute)
	retained := recorder.completedBetween(0, serviceStatusMinute(now))
	if len(retained) != ServiceStatusSlots || retained[0].minute != serviceStatusMinute(clock)+3 ||
		retained[len(retained)-3].all.FailedRequests != 0 {
		t.Fatalf("outdated completion revived or displaced a current slot: n=%d first=%+v", len(retained), retained[0])
	}
}

func TestServiceStatusMixedCompletionMinuteStaysUnknownUntilClockCatchesUp(t *testing.T) {
	base := time.Date(2026, 11, 25, 15, 20, 0, 0, time.UTC)
	now := base
	recorder := &ServiceStatusEndpointRecorder{}
	sampler := NewServiceStatusSamplerWithSources(nil, func() time.Time { return now }, ServiceStatusSources{
		Completions: recorder, Process: func() StatusProcessMetrics { return StatusProcessMetrics{Goroutines: 7} },
	})
	sampler.Sample()
	now = base.Add(5 * time.Second)
	recorder.ObserveCompletion("/api/search/tasks", 503, 19, StatusFailureUpstream, now)
	if first := sampler.Snapshot(); first.Summary.Endpoints.Search.FailedRequests != 1 {
		t.Fatal("fixture did not first observe the failed search")
	}
	now = base.Add(30 * time.Second)
	recorder.ObserveCompletion("/api/other", 200, 4, StatusFailureNone, now)
	now = base.Add(20 * time.Second)
	if probe := sampler.Sample(); !probe.requestUnknown || probe.Endpoints.Search.Samples != 0 {
		t.Fatalf("direct sample exposed traffic from an indivisible minute: %+v", probe)
	}
	now = base.Add(25 * time.Second)
	sampler.Sample() // also put the most recent periodic tick past the next asOf.
	now = base.Add(20 * time.Second)
	unknown := sampler.Snapshot()
	if len(unknown.Points) != 0 || !unknown.LatestSearchSampleAt.IsZero() ||
		unknown.Summary.Status != ServiceStatusUnknown || unknown.Summary.Endpoints.Search.Samples != 0 ||
		unknown.Summary.Endpoints.Other.TotalRequests != 0 {
		t.Fatalf("indivisible minute became idle or leaked future traffic: %+v", unknown)
	}
	if points := unknown.ProcessPoints(); len(points) != 1 || !points[0].SampledAt.Equal(base) || points[0].Goroutines != 7 {
		t.Fatalf("valid earlier process observation lost: %+v", points)
	}
	now = base.Add(30 * time.Second)
	recovered := sampler.Snapshot()
	if len(recovered.Points) != 1 || recovered.Summary.TotalRequests != 2 ||
		recovered.Summary.Endpoints.Search.FailedRequests != 1 || recovered.Summary.Endpoints.Other.SuccessRequests != 1 ||
		!recovered.LatestSampleAt.Equal(now) || len(recovered.ProcessPoints()) != 1 {
		t.Fatalf("traffic did not recover after clock caught up: %+v", recovered)
	}
	t.Log("asOf 20s: traffic unknown, future completion hidden, 00s process observation retained; asOf 30s: search failure and other success recovered")
}
