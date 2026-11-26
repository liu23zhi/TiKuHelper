package service

import "time"

type statusCompletionMinute struct {
	minute       int64
	latest       time.Time
	latestSearch time.Time
	all          StatusCounters
	endpoints    ServiceStatusEndpointCounters
}

func (r *ServiceStatusEndpointRecorder) completedBetween(start, end int64, asOf time.Time) []statusCompletionMinute {
	if r == nil || end < start {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.completedBetweenLocked(start, end, asOf)
}

// Read the authoritative clock under the same lock as terminal observations;
// a concurrent completion cannot make an already observed minute disappear
// from an earlier as-of snapshot merely by winning a copy race.
func (r *ServiceStatusEndpointRecorder) completionSnapshot(startedAt time.Time, clock func() time.Time) (time.Time, []statusCompletionMinute) {
	r.mu.Lock()
	defer r.mu.Unlock()
	asOf := clock().UTC()
	return asOf, r.completedBetweenLocked(serviceStatusMinute(startedAt), serviceStatusMinute(asOf), asOf)
}

func (r *ServiceStatusEndpointRecorder) completedBetweenLocked(start, end int64, asOf time.Time) []statusCompletionMinute {
	start = max(start, end-ServiceStatusSlots+1, 0)
	var observations []statusCompletionMinute
	for minute := start; minute <= end; minute++ {
		slot := r.completed[minute%ServiceStatusSlots]
		if slot.minute == minute && slot.all.TotalRequests > 0 && !slot.latest.After(asOf) {
			observations = append(observations, slot)
		}
	}
	return observations
}

func withStatusCompletions(point StatusPoint, event statusCompletionMinute) StatusPoint {
	applyStatusDelta(&point, event.all)
	applyEndpointBucket(&point.Endpoints.Search, event.endpoints.Search, event.endpoints.Search.TotalRequests > 0)
	applyEndpointBucket(&point.Endpoints.Other, event.endpoints.Other, event.endpoints.Other.TotalRequests > 0)
	point.latency.addDelta(event.all)
	point.AvgLatencyMs = point.latency.average()
	if event.latest.After(point.sampledAt) {
		point.sampledAt = event.latest
	}
	if event.latestSearch.After(point.searchSampledAt) {
		point.searchSampledAt = event.latestSearch
	}
	return point
}

// Merge only actual ticks or terminal evidence; a gap is never filled with zero.
// An other-only completion does not prove public search coverage.
func mergeStatusCompletions(periodic []StatusPoint, events []statusCompletionMinute) []StatusPoint {
	points := make([]StatusPoint, 0, max(len(periodic), len(events)))
	for _, event := range events {
		for len(periodic) > 0 && periodic[0].Minute < event.minute {
			points = append(points, periodic[0])
			periodic = periodic[1:]
		}
		point := StatusPoint{Minute: event.minute, eventOnly: true}
		if len(periodic) > 0 && periodic[0].Minute == event.minute {
			point, periodic = periodic[0], periodic[1:]
		}
		points = append(points, withStatusCompletions(point, event))
	}
	return append(points, periodic...)
}
