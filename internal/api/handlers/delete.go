package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/birdple/imagine/internal/api/types"
	"github.com/birdple/imagine/internal/api/utils"
	"github.com/birdple/imagine/internal/storage"
)

// HandleDelete handles deleting files or entire directories
func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse request body
	var req types.DeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
		return
	}

	// Validate that either keys or prefix is provided
	if len(req.Keys) == 0 && req.Prefix == "" {
		h.sendError(w, http.StatusBadRequest, "MISSING_PARAMETERS", "Either 'keys' or 'prefix' must be provided")
		return
	}

	// Get bucket-aware storage instance
	storageBackend := h.getStorageForBucket(req.Bucket)

	var deletedKeys []string

	// If prefix is provided, list all files with that prefix and delete them
	if req.Prefix != "" {
		// Normalize and validate prefix path
		prefix := utils.NormalizeDirectoryPath(req.Prefix)
		if err := utils.ValidateDirectoryPath(prefix); err != nil {
			h.sendError(w, http.StatusBadRequest, "INVALID_PREFIX", fmt.Sprintf("Invalid prefix path: %v", err))
			return
		}

		// List all files with this prefix
		results, err := storageBackend.List(ctx, prefix)
		if err != nil {
			h.logger.WithError(err).Error("Failed to list files for deletion")
			h.sendError(w, http.StatusInternalServerError, "LIST_ERROR", "Failed to list files for deletion")
			return
		}

		// Delete each file
		for _, result := range results {
			if err := storageBackend.Delete(ctx, result.Key); err != nil {
				h.logger.WithError(err).WithField("key", result.Key).Warn("Failed to delete file")
				continue
			}
			deletedKeys = append(deletedKeys, result.Key)
		}
	}

	// Delete specific keys if provided
	if len(req.Keys) > 0 {
		for _, key := range req.Keys {
			if err := storageBackend.Delete(ctx, key); err != nil {
				if storage.IsNotFound(err) {
					h.logger.WithField("key", key).Warn("File not found for deletion")
					continue
				}
				h.logger.WithError(err).WithField("key", key).Warn("Failed to delete file")
				continue
			}
			deletedKeys = append(deletedKeys, key)
		}
	}

	response := types.DeleteResponse{
		Success: true,
		Deleted: deletedKeys,
		Count:   len(deletedKeys),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}
