package handlers

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"sync"

	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/jsonx"
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
	if err := jsonv2.UnmarshalRead(r.Body, &req, jsonx.Strict); err != nil {
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
	// Claves que se pidió borrar y no se borraron. Antes se contaban sólo en el
	// log y la respuesta salía `success: true` igual: con jay caído, el usuario
	// pedía "borrar mis fotos", recibía `{"success":true,"count":0}`, y
	// birdple-api lo asentaba como borrado. Las fotos seguían ahí y nadie
	// reintentaba.
	var failedKeys []string
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

		// Fan-out parallel deletes. Per-key ownership check runs INSIDE the
		// worker — a scoped caller must not be able to delete another user's
		// image by passing a shared prefix. Admin scope bypasses the check
		// inside checkOwnership.
		keys := make(chan string, len(results))
		for _, item := range results {
			keys <- item.Key
		}
		close(keys)

		var mu sync.Mutex
		var wg sync.WaitGroup

		for range deleteWorkers {
			wg.Go(func() {
				for key := range keys {
					if ownErr := h.checkOwnership(r, storageBackend, key); ownErr != nil {
						if storage.IsNotFound(ownErr) {
							// Ya no existe: el resultado que pedía el llamador.
							logger.Warn().Str("key", key).Msg("File not found for deletion")
							continue
						}
						logger.Warn().Err(ownErr).Str("key", key).Msg("Ownership check failed; skipping delete")
						mu.Lock()
						failedKeys = append(failedKeys, key)
						mu.Unlock()
						continue
					}
					if err := storageBackend.Delete(ctx, key); err != nil {
						logger.Warn().Err(err).Str("key", key).Msg("Failed to delete file")
						mu.Lock()
						failedKeys = append(failedKeys, key)
						mu.Unlock()
						continue
					}
					h.invalidateCache(key)
					mu.Lock()
					deletedKeys = append(deletedKeys, key)
					mu.Unlock()
				}
			})
		}
		wg.Wait()
	}

	if len(req.Keys) > 0 {
		for _, key := range req.Keys {
			if ownErr := h.checkOwnership(r, storageBackend, key); ownErr != nil {
				if storage.IsNotFound(ownErr) {
					logger.Warn().Str("key", key).Msg("File not found for deletion")
					continue
				}
				logger.Warn().Err(ownErr).Str("key", key).Msg("Ownership check failed; skipping delete")
				failedKeys = append(failedKeys, key)
				continue
			}
			if err := storageBackend.Delete(ctx, key); err != nil {
				if storage.IsNotFound(err) {
					logger.Warn().Str("key", key).Msg("File not found for deletion")
					continue
				}
				logger.Warn().Err(err).Str("key", key).Msg("Failed to delete file")
				failedKeys = append(failedKeys, key)
				continue
			}
			h.invalidateCache(key)
			deletedKeys = append(deletedKeys, key)
		}
	}

	// Un borrado que no borró todo lo que se le pidió NO es un éxito. El
	// llamador tiene que poder distinguirlo para reintentar.
	response := types.DeleteResponse{
		Success:   len(failedKeys) == 0,
		Deleted:   deletedKeys,
		Failed:    failedKeys,
		Count:     len(deletedKeys),
		Truncated: truncated,
	}

	status := http.StatusOK
	if len(failedKeys) > 0 {
		logger.Error().
			Int("deleted", len(deletedKeys)).
			Int("failed", len(failedKeys)).
			Msg("Delete completed with failures")
		status = http.StatusMultiStatus
	}

	writeJSON(w, status, response)
}

// invalidateCache drops every cached variant of a key that no longer exists (or
// whose bytes changed). Sin esto, `LRUCache.Delete` existía y no lo llamaba
// nadie: la imagen borrada se seguía sirviendo hasta 24 h desde RAM.
func (h *Handler) invalidateCache(key string) {
	if h.imageProcessor == nil {
		return
	}
	if n := h.imageProcessor.InvalidateCacheForKey(key); n > 0 {
		logger.Debug().Str("key", key).Int("variants", n).Msg("Invalidated cached variants")
	}
}
