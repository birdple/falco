package handlers

import (
	"encoding/json"
	"net/http"
	"time"
)

// HandleHealth handles health check requests with detailed metrics
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	services := map[string]interface{}{}

	storageHealth := map[string]interface{}{
		"status": "healthy",
	}

	overallStatus := "healthy"

	if err := h.storage.Health(ctx); err != nil {
		storageHealth["status"] = "unhealthy"
		storageHealth["error"] = err.Error()
		overallStatus = "degraded"
	}

	if stats, err := h.storage.GetStats(ctx); err == nil {
		storageHealth["total_images"] = stats.TotalImages
		storageHealth["total_size_bytes"] = stats.TotalSize
		storageHealth["total_size_mb"] = float64(stats.TotalSize) / (1024 * 1024)
		storageHealth["avg_image_size_bytes"] = stats.TotalSize / max(1, stats.TotalImages)
	} else {
		storageHealth["stats_error"] = err.Error()
	}

	services["storage"] = storageHealth
	services["processor"] = map[string]interface{}{
		"status":            "healthy",
		"supported_formats": []string{"webp", "jpeg", "png", "avif"},
	}

	health := map[string]interface{}{
		"status":    overallStatus,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"version":   "1.0.0",
		"uptime":    time.Since(h.startTime).String(),
		"services":  services,
	}

	health["system"] = map[string]interface{}{
		"go_version":       "1.21+",
		"max_file_size_mb": float64(h.config.GetMaxFileSizeBytes()) / (1024 * 1024),
		"max_dimensions": map[string]int{
			"width":  h.config.Processing.MaxDimensions.Width,
			"height": h.config.Processing.MaxDimensions.Height,
		},
	}

	statusCode := http.StatusOK
	if health["status"] == "degraded" {
		statusCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(health)
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
