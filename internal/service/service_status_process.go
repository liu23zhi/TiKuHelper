package service

import (
	"runtime"
	"time"

	"github.com/itihey/tikuAdapter/internal/diagnostics"
)

// StatusProcessMetrics is a fixed process-local view. No host, path, account,
// or request identifiers are recorded, and only the admin response uses it.
type StatusProcessMetrics struct {
	UptimeSeconds uint64              `json:"uptimeSeconds"`
	Goroutines    int                 `json:"goroutines"`
	Memory        StatusProcessMemory `json:"memory"`
}

type StatusProcessMemory struct {
	HeapAllocBytes  uint64 `json:"heapAllocBytes"`
	HeapSysBytes    uint64 `json:"heapSysBytes"`
	SysBytes        uint64 `json:"sysBytes"`
	StackInuseBytes uint64 `json:"stackInuseBytes"`
	GCCycles        uint32 `json:"gcCycles"`
}

type StatusProcessPoint struct {
	Minute    int64     `json:"minute"`
	SampledAt time.Time `json:"sampledAt"`
	StatusProcessMetrics
}

func readServiceStatusProcess() StatusProcessMetrics {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return StatusProcessMetrics{
		UptimeSeconds: diagnostics.Default.Snapshot().UptimeSeconds,
		Goroutines:    runtime.NumGoroutine(),
		Memory: StatusProcessMemory{
			HeapAllocBytes: mem.HeapAlloc, HeapSysBytes: mem.HeapSys, SysBytes: mem.Sys,
			StackInuseBytes: mem.StackInuse, GCCycles: mem.NumGC,
		},
	}
}

// ProcessPoints explicitly projects real, timestamped admin-only observations.
// Never copy today's runtime snapshot into empty slots of the retained history.
func (s ServiceStatusSnapshot) ProcessPoints() []StatusProcessPoint {
	points := make([]StatusProcessPoint, 0, len(s.Points))
	for _, point := range s.Points {
		if point.process != nil {
			points = append(points, *point.process)
		}
	}
	return points
}
