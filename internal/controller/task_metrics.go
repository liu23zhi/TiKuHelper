package controller

// TaskCounts counts currently retained tasks, not queue length or capacity.
// Total includes unknown statuses, which never imply successful completion.
type TaskCounts struct {
	Total     uint64 `json:"total"`
	Running   uint64 `json:"running"`
	Succeeded uint64 `json:"succeeded"`
	Failed    uint64 `json:"failed"`
}

// TaskMetricsSnapshot exposes counts only, without IDs, owners, or request data.
// Each store is read coherently under its own lock; the two stores are not an
// atomic cross-store transaction. Reading never expires or changes any task.
type TaskMetricsSnapshot struct {
	Search   TaskCounts `json:"search"`
	Fallback TaskCounts `json:"fallback"`
}

func snapshotTaskMetrics() TaskMetricsSnapshot {
	var snapshot TaskMetricsSnapshot
	defaultSearchTaskStore.mu.RLock()
	for _, task := range defaultSearchTaskStore.tasks {
		snapshot.Search.Total++
		switch task.Status {
		case searchTaskStatusRunning:
			snapshot.Search.Running++
		case searchTaskStatusSucceeded:
			snapshot.Search.Succeeded++
		case searchTaskStatusFailed:
			snapshot.Search.Failed++
		}
	}
	defaultSearchTaskStore.mu.RUnlock()
	defaultAIFallbackTaskStore.mu.RLock()
	for _, task := range defaultAIFallbackTaskStore.tasks {
		snapshot.Fallback.Total++
		switch task.Status {
		case aiFallbackTaskStatusRunning:
			snapshot.Fallback.Running++
		case aiFallbackTaskStatusSucceeded:
			snapshot.Fallback.Succeeded++
		case aiFallbackTaskStatusFailed:
			snapshot.Fallback.Failed++
		}
	}
	defaultAIFallbackTaskStore.mu.RUnlock()
	return snapshot
}
