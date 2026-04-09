package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/storage"
)

const (
	// deleteWorkers is the number of concurrent goroutines used for prefix deletes.
	deleteWorkers = 10
	// listCap is the MaxKeys value used by storage.List; when the result set
	// equals this number the response is likely truncated.
	listCap = 1000
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
	var truncated bool

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

		if len(results) >= listCap {
			truncated = true
		}

		// Fan-out parallel deletes
		keys := make(chan string, len(results))
		for _, item := range results {
			keys <- item.Key
		}
		close(keys)

		var mu sync.Mutex
		var wg sync.WaitGroup

		for i := 0; i < deleteWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for key := range keys {
					if err := storageBackend.Delete(ctx, key); err != nil {
						logger.Warn().Err(err).Str("key", key).Msg("Failed to delete file")
						continue
					}
					mu.Lock()
					deletedKeys = append(deletedKeys, key)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
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
		Success:   true,
		Deleted:   deletedKeys,
		Count:     len(deletedKeys),
		Truncated: truncated,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}
