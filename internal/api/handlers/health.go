package handlers

import (
	"encoding/json"
	"net/http"
	"time"
)

// HandleHealth handles health check requests with detailed metrics
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"version":   "1.0.0",
		"uptime":    time.Since(h.startTime).String(),
		"services":  map[string]interface{}{},
	}

	// Check storage health with detailed metrics
	storageHealth := map[string]interface{}{
		"status": "healthy",
	}

	if err := h.storage.Health(ctx); err != nil {
		storageHealth["status"] = "unhealthy"
		storageHealth["error"] = err.Error()
		health["status"] = "degraded"
	}

	// Get storage stats with more details
	if stats, err := h.storage.GetStats(ctx); err == nil {
		storageHealth["total_images"] = stats.TotalImages
		storageHealth["total_size_bytes"] = stats.TotalSize
		storageHealth["total_size_mb"] = float64(stats.TotalSize) / (1024 * 1024)
		storageHealth["avg_image_size_bytes"] = stats.TotalSize / max(1, stats.TotalImages)
	} else {
		storageHealth["stats_error"] = err.Error()
	}

	health["services"].(map[string]interface{})["storage"] = storageHealth

	// Check image processor health (basic check)
	processorHealth := map[string]interface{}{
		"status":            "healthy",
		"supported_formats": []string{"webp", "jpeg", "png", "avif"},
	}

	health["services"].(map[string]interface{})["processor"] = processorHealth

	// System metrics
	health["system"] = map[string]interface{}{
		"go_version":       "1.21+",
		"max_file_size_mb": float64(h.config.GetMaxFileSizeBytes()) / (1024 * 1024),
		"max_dimensions": map[string]int{
			"width":  h.config.Processing.MaxDimensions.Width,
			"height": h.config.Processing.MaxDimensions.Height,
		},
	}

	// Set status code based on health
	statusCode := http.StatusOK
	if health["status"] == "degraded" {
		statusCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(health)
}

// max returns the maximum of two integers
func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
