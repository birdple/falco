package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/storage"
)

// HandleDelete handles deleting files or entire directories
func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req types.DeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
		return
	}

	if len(req.Keys) == 0 && req.Prefix == "" {
		h.sendError(w, http.StatusBadRequest, "MISSING_PARAMETERS", "Either 'keys' or 'prefix' must be provided")
		return
	}

	storageBackend, sbErr := h.getStorageBackendScoped(r, req.Storage, req.Bucket)
	if sbErr != nil {
		h.sendError(w, http.StatusForbidden, "ACCESS_DENIED", sbErr.Error())
		return
	}

	var deletedKeys []string

	if req.Prefix != "" {
		prefix := utils.NormalizeDirectoryPath(req.Prefix)
		if err := utils.ValidateDirectoryPath(prefix); err != nil {
			h.sendError(w, http.StatusBadRequest, "INVALID_PREFIX", fmt.Sprintf("Invalid prefix path: %v", err))
			return
		}

		results, err := storageBackend.List(ctx, prefix)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to list files for deletion")
			h.sendError(w, http.StatusInternalServerError, "LIST_ERROR", "Failed to list files for deletion")
			return
		}

		for _, result := range results {
			if err := storageBackend.Delete(ctx, result.Key); err != nil {
				logger.Warn().Err(err).Str("key", result.Key).Msg("Failed to delete file")
				continue
			}
			deletedKeys = append(deletedKeys, result.Key)
		}
	}

	if len(req.Keys) > 0 {
		for _, key := range req.Keys {
			if err := storageBackend.Delete(ctx, key); err != nil {
				if storage.IsNotFound(err) {
					logger.Warn().Str("key", key).Msg("File not found for deletion")
					continue
				}
				logger.Warn().Err(err).Str("key", key).Msg("Failed to delete file")
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
