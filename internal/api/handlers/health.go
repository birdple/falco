package handlers

import (
	"encoding/json"
	"net/http"
	"time"
)

// HandleHealth handles health check requests for load balancers and monitoring.
// Returns only essential status information; verbose stats belong in /metrics.
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	overallStatus := "healthy"
	statusCode := http.StatusOK

	if err := h.storage.Health(ctx); err != nil {
		overallStatus = "unhealthy"
		statusCode = http.StatusServiceUnavailable
	}

	health := map[string]interface{}{
		"status":  overallStatus,
		"version": "1.0.0",
		"uptime":  time.Since(h.startTime).String(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(health)
}
