package service

import (
	"testing"
	"time"
)

func TestServiceStatusDefaultProcessProbeReadsRealRuntime(t *testing.T) {
	src := NewDefaultServiceStatusSampler().readProcess()
	if src.Goroutines < 1 || src.Memory.HeapAllocBytes == 0 || src.Memory.SysBytes < src.Memory.HeapAllocBytes {
		t.Fatalf("runtime process probe was unavailable: %+v", src)
	}
	t.Logf("real process probe observed goroutines=%d and nonzero heap/runtime bytes", src.Goroutines)
}

func TestServiceStatusSnapshotRetainsOnlyBoundedObservedMinutes(t *testing.T) {
	base := time.Date(2026, 11, 27, 5, 4, 23, 789000000, time.UTC)
	now := base
	s := NewServiceStatusSampler(nil, func() time.Time { return now }, nil)
	for i := range ServiceStatusSlots + 17 {
		now = base.Add(time.Duration(i) * time.Minute)
		s.Sample()
	}
	summary := s.Snapshot()
	if len(summary.Points) != ServiceStatusSlots || summary.Points[0].Minute != now.Unix()/60-ServiceStatusSlots+1 ||
		summary.Points[ServiceStatusSlots-1].Minute != now.Unix()/60 || summary.Summary.PointsUsed != 5 ||
		summary.Summary.Status != ServiceStatusUnknown || !summary.LatestSampleAt.Equal(now) || len(summary.ProcessPoints()) != 0 {
		t.Fatalf("observed idle minutes must be bounded and not healthy: n=%d summary=%+v", len(summary.Points), summary.Summary)
	}
	now = now.Add(-time.Second) // last observation is before later wall calibration.
	paused := s.Snapshot()
	if len(paused.Points) >= len(summary.Points) || !paused.LatestSampleAt.Before(now) {
		t.Fatalf("future sample within the same wall minute escaped: count=%d latest=%v", len(paused.Points), paused.LatestSampleAt)
	}
	s.Sample() // backward wall time must not overwrite the later observation.
	now = now.Add(2 * time.Second)
	s.Sample()
	resumed := s.Snapshot()
	if len(resumed.Points) != ServiceStatusSlots || !resumed.LatestSampleAt.Equal(now) {
		t.Fatalf("sampling did not recover without duplicate: count=%d latest=%v", len(resumed.Points), resumed.LatestSampleAt)
	}

	now = now.Add(5 * time.Minute)
	gap := s.Snapshot()
	if len(gap.Points) != ServiceStatusSlots-5 || gap.Summary.PointsUsed != 0 || gap.Summary.EndMinute != 0 ||
		gap.Summary.Status != ServiceStatusUnknown {
		t.Fatalf("gap filled or summary ignored its clock: count=%d summary=%+v", len(gap.Points), gap.Summary)
	}
	now = now.Add(24 * time.Hour)
	empty := s.Snapshot()
	if empty.Points == nil || len(empty.Points) != 0 || !empty.LatestSampleAt.IsZero() || empty.Summary.TotalRequests != 0 {
		t.Fatalf("stale samples survived more than 24 hours: %+v", empty)
	}
}
