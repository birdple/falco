package handlers

import (
	"context"
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

	// Keys that were asked to be deleted and were not. These used to be counted
	// only in the log while the response still said `success: true`: with jay
	// pedía "borrar mis fotos", recibía `{"success":true,"count":0}`, y
	// down, a user asking to "delete my photos" got `{"success":true,"count":0}`
	// and birdple-api recorded it as deleted. The photos were still there and
	// reintentaba.
	var tally deleteTally
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
		truncated = len(results) >= listCap

		listedKeys := make([]string, 0, len(results))
		for _, item := range results {
			listedKeys = append(listedKeys, item.Key)
		}
		h.deleteKeysParallel(ctx, r, storageBackend, listedKeys, &tally)
	}

	for _, key := range req.Keys {
		h.deleteKey(ctx, r, storageBackend, key, &tally)
	}

	deletedKeys, failedKeys := tally.results()

	// A delete that did not delete everything it was asked to is NOT a success.
	// The caller has to be able to tell the difference in order to retry.
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
// whose bytes changed). Without this, LRUCache.Delete existed and nothing ever
// called it: a deleted image kept being served from RAM for up to 24 hours.
func (h *Handler) invalidateCache(key string) {
	if h.imageProcessor == nil {
		return
	}
	if n := h.imageProcessor.InvalidateCacheForKey(key); n > 0 {
		logger.Debug().Str("key", key).Int("variants", n).Msg("Invalidated cached variants")
	}
}

// deleteTally accumulates the outcome of every key a delete request touched.
// The mutex is there because the prefix path fans out across workers.
type deleteTally struct {
	mu      sync.Mutex
	deleted []string
	failed  []string
}

func (t *deleteTally) recordDeleted(key string) {
	t.mu.Lock()
	t.deleted = append(t.deleted, key)
	t.mu.Unlock()
}

func (t *deleteTally) recordFailed(key string) {
	t.mu.Lock()
	t.failed = append(t.failed, key)
	t.mu.Unlock()
}

func (t *deleteTally) results() (deleted, failed []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.deleted, t.failed
}

// deleteKeysParallel deletes a batch of keys across a bounded worker pool.
//
// The per-key ownership check runs INSIDE the worker, not before the fan-out: a
// scoped caller must not be able to delete somebody else's image by passing a
// prefix they share. Admin scope bypasses the check inside checkOwnership.
func (h *Handler) deleteKeysParallel(
	ctx context.Context, r *http.Request, backend storage.StorageBackend,
	keys []string, tally *deleteTally,
) {
	queue := make(chan string, len(keys))
	for _, key := range keys {
		queue <- key
	}
	close(queue)

	var wg sync.WaitGroup
	for range deleteWorkers {
		wg.Go(func() {
			for key := range queue {
				h.deleteKey(ctx, r, backend, key, tally)
			}
		})
	}
	wg.Wait()
}

// deleteKey deletes one key and records the outcome.
//
// A key that is already gone counts as neither deleted nor failed: the caller
// asked for it to not be there, and it is not there. Anything else — a failed
// ownership check, a backend error — goes to the failed list, because a delete
// that did not delete must not be reported as success.
func (h *Handler) deleteKey(
	ctx context.Context, r *http.Request, backend storage.StorageBackend,
	key string, tally *deleteTally,
) {
	if err := h.checkOwnership(r, backend, key); err != nil {
		if storage.IsNotFound(err) {
			logger.Warn().Str("key", key).Msg("File not found for deletion")
			return
		}
		logger.Warn().Err(err).Str("key", key).Msg("Ownership check failed; skipping delete")
		tally.recordFailed(key)
		return
	}

	if err := backend.Delete(ctx, key); err != nil {
		if storage.IsNotFound(err) {
			logger.Warn().Str("key", key).Msg("File not found for deletion")
			return
		}
		logger.Warn().Err(err).Str("key", key).Msg("Failed to delete file")
		tally.recordFailed(key)
		return
	}

	h.invalidateCache(key)
	tally.recordDeleted(key)
}
