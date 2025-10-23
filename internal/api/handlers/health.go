package handlers

import (
	"encoding/json"
	"net/http"
	"time"
)

// HandleHealth handles health check requests
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"version":   "1.0.0",
		"uptime":    time.Since(h.startTime).String(),
	}

	// Check storage health
	storageHealth := map[string]string{}
	if err := h.storage.Health(ctx); err != nil {
		storageHealth["status"] = "unhealthy"
		storageHealth["error"] = err.Error()
	} else {
		storageHealth["status"] = "healthy"
	}

	// Get storage stats
	if stats, err := h.storage.GetStats(ctx); err == nil {
		health["storage"] = map[string]interface{}{
			"status":       storageHealth["status"],
			"total_images": stats.TotalImages,
			"total_size":   stats.TotalSize,
		}
	} else {
		health["storage"] = storageHealth
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}
