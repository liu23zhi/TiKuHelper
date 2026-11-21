package controller

import (
	"net/http"
	"runtime"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/diagnostics"
)

type systemMetricsResponse struct {
	diagnostics.Snapshot
	Tasks  TaskMetricsSnapshot   `json:"tasks"`
	Memory processMemorySnapshot `json:"memory"`
}

// processMemorySnapshot is a fixed, process-local Go runtime memory view.
// It is returned only by the admin-protected /api/system-metrics endpoint.
type processMemorySnapshot struct {
	AllocBytes        uint64 `json:"allocBytes"`
	TotalAllocBytes   uint64 `json:"totalAllocBytes"`
	SysBytes          uint64 `json:"sysBytes"`
	HeapAllocBytes    uint64 `json:"heapAllocBytes"`
	HeapSysBytes      uint64 `json:"heapSysBytes"`
	HeapInuseBytes    uint64 `json:"heapInuseBytes"`
	HeapIdleBytes     uint64 `json:"heapIdleBytes"`
	HeapReleasedBytes uint64 `json:"heapReleasedBytes"`
	StackInuseBytes   uint64 `json:"stackInuseBytes"`
	GCCycles          uint32 `json:"gcCycles"`
}

func snapshotProcessMemory() processMemorySnapshot {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return processMemorySnapshot{
		AllocBytes: stats.Alloc, TotalAllocBytes: stats.TotalAlloc, SysBytes: stats.Sys,
		HeapAllocBytes: stats.HeapAlloc, HeapSysBytes: stats.HeapSys, HeapInuseBytes: stats.HeapInuse,
		HeapIdleBytes: stats.HeapIdle, HeapReleasedBytes: stats.HeapReleased,
		StackInuseBytes: stats.StackInuse, GCCycles: stats.NumGC,
	}
}

// GetSystemMetrics exposes only fixed, process-local aggregates to the admin
// route. Query strings never select scope, labels, roles or task contents.
// Billing counters describe observed settlement attempts, not ledger or income.
func GetSystemMetrics(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, systemMetricsResponse{
		Snapshot: diagnostics.Default.Snapshot(),
		Tasks:    snapshotTaskMetrics(),
		Memory:   snapshotProcessMemory(),
	})
}
