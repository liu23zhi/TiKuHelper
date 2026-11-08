package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/itihey/tikuAdapter/internal/diagnostics"
)

type systemMetricsResponse struct {
	diagnostics.Snapshot
	Tasks TaskMetricsSnapshot `json:"tasks"`
}

// GetSystemMetrics exposes only fixed, process-local aggregates to the admin
// route. Query strings never select scope, labels, roles or task contents.
// Billing counters describe observed settlement attempts, not ledger or income.
func GetSystemMetrics(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, systemMetricsResponse{
		Snapshot: diagnostics.Default.Snapshot(),
		Tasks:    snapshotTaskMetrics(),
	})
}
