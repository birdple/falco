package handlers

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// watermarkAllowedHosts reads WATERMARK_ALLOWED_HOSTS.
//
// Unlike the proxy's allowlist there is no compiled-in fallback: an unset
// variable means external watermarks are refused outright. Falling back to
// "any host" would turn a signed image URL into an arbitrary outbound fetch,
// and falling back to the proxy's list would quietly grant a different feature
// the permissions of this one.
func watermarkAllowedHosts() map[string]struct{} {
	m := make(map[string]struct{})
	for h := range strings.SplitSeq(os.Getenv("WATERMARK_ALLOWED_HOSTS"), ",") {
		if t := strings.TrimSpace(h); t != "" {
			m[strings.ToLower(t)] = struct{}{}
		}
	}
	return m
}

var (
	watermarkAllowlistOnce  sync.Once
	watermarkAllowlistCache map[string]struct{}
)

func cachedWatermarkAllowedHosts() map[string]struct{} {
	watermarkAllowlistOnce.Do(func() {
		watermarkAllowlistCache = watermarkAllowedHosts()
	})
	return watermarkAllowlistCache
}

// Overlays are read once and kept: the same logo is asked for by every image
// that carries it, and re-reading it on each cache miss would make a watermark
// cost one extra storage round-trip per distinct transformation.
const (
	watermarkCacheTTL     = 10 * time.Minute
	watermarkCacheEntries = 16
)

type watermarkEntry struct {
	data     []byte
	storedAt time.Time
}

var (
	watermarkCacheMu sync.RWMutex
	watermarkCache   = make(map[string]watermarkEntry)
)

func watermarkFromCache(source string) ([]byte, bool) {
	watermarkCacheMu.RLock()
	entry, ok := watermarkCache[source]
	watermarkCacheMu.RUnlock()
	if !ok || time.Since(entry.storedAt) > watermarkCacheTTL {
		return nil, false
	}
	return entry.data, true
}

func storeWatermarkInCache(source string, data []byte) {
	watermarkCacheMu.Lock()
	defer watermarkCacheMu.Unlock()

	// A handful of overlays is the realistic working set, so eviction is by
	// age and only when the map is full. An LRU here would be more machinery
	// than the thing it manages.
	if len(watermarkCache) >= watermarkCacheEntries {
		oldest, oldestAt := "", time.Now()
		for key, entry := range watermarkCache {
			if entry.storedAt.Before(oldestAt) {
				oldest, oldestAt = key, entry.storedAt
			}
		}
		if oldest != "" {
			delete(watermarkCache, oldest)
		}
	}
	watermarkCache[source] = watermarkEntry{data: data, storedAt: time.Now()}
}

// resolveWatermark loads the overlay named by params.WatermarkSource into
// params.WatermarkImage.
//
// It runs on the cache-miss path only, inside the singleflight, because that is
// the only place the bytes are needed: a request answered from the transformed
// cache never touches the overlay at all.
//
// Every failure here is reported. A watermark that could not be loaded must not
// degrade into an unwatermarked image — the caller asked for one, the response
// would not have it, and nothing in the response would say so.
func (h *Handler) resolveWatermark(ctx context.Context, backend storage.StorageBackend, params *processor.ProcessingParams) *fetchError {
	source := params.WatermarkSource
	if source == "" {
		return nil
	}

	if data, ok := watermarkFromCache(source); ok {
		params.WatermarkImage = data
		return nil
	}

	var (
		data []byte
		err  *fetchError
	)
	if strings.HasPrefix(source, watermarkStoredPrefix) {
		data, err = h.watermarkFromStorage(ctx, backend, strings.TrimPrefix(source, watermarkStoredPrefix))
	} else {
		data, err = h.watermarkFromURL(ctx, source)
	}
	if err != nil {
		return err
	}

	storeWatermarkInCache(source, data)
	params.WatermarkImage = data
	return nil
}

// watermarkFromStorage reads an overlay that lives in Falco's own storage,
// through the same backend the image itself came from — so a scoped key cannot
// reach a bucket it is not allowed to read by asking for a watermark from it.
func (h *Handler) watermarkFromStorage(ctx context.Context, backend storage.StorageBackend, id string) ([]byte, *fetchError) {
	directory, imageID := utils.SplitDirectoryAndID(id)
	key := utils.BuildStorageKey(directory, imageID)

	reader, metadata, err := backend.Retrieve(ctx, key)
	if err != nil {
		if storage.IsNotFound(err) {
			return nil, &fetchError{http.StatusNotFound, "WATERMARK_NOT_FOUND", "Watermark image not found"}
		}
		logger.Error().Err(err).Str("watermark", key).Msg("Failed to retrieve watermark")
		return nil, &fetchError{http.StatusInternalServerError, "WATERMARK_ERROR", "Failed to read watermark"}
	}
	defer func() { _ = reader.Close() }()

	if !utils.IsImageContentType(metadata.ContentType) {
		return nil, &fetchError{http.StatusUnprocessableEntity, "WATERMARK_NOT_AN_IMAGE", "Watermark is not an image"}
	}

	data, readErr := io.ReadAll(io.LimitReader(reader, maxWatermarkBytes+1))
	if readErr != nil {
		logger.Error().Err(readErr).Str("watermark", key).Msg("Failed to read watermark body")
		return nil, &fetchError{http.StatusInternalServerError, "WATERMARK_ERROR", "Failed to read watermark"}
	}
	if len(data) > maxWatermarkBytes {
		return nil, &fetchError{http.StatusUnprocessableEntity, "WATERMARK_TOO_LARGE", "Watermark exceeds the size limit"}
	}
	return data, nil
}

// watermarkFromURL fetches an external overlay, behind its own allowlist.
//
// The host check runs before anything resolves the name: DNS resolution is
// itself an outbound request, and doing it first would let a caller make Falco
// look up arbitrary names. The client on top of that refuses to dial a private
// or reserved address, so an allowed host that points inward gets nowhere.
func (h *Handler) watermarkFromURL(ctx context.Context, raw string) ([]byte, *fetchError) {
	allowed := cachedWatermarkAllowedHosts()
	if len(allowed) == 0 {
		return nil, &fetchError{
			http.StatusForbidden, "WATERMARK_HOST_NOT_ALLOWED",
			"external watermarks are disabled: WATERMARK_ALLOWED_HOSTS is not set",
		}
	}

	if len(raw) > MaxURLLength {
		return nil, &fetchError{http.StatusBadRequest, "INVALID_WATERMARK", "wm_url exceeds the maximum allowed length"}
	}

	parsed, err := url.ParseRequestURI(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, &fetchError{http.StatusBadRequest, "INVALID_WATERMARK", "wm_url must be an absolute http/https URL"}
	}

	hostname := parsed.Hostname()
	if _, ok := allowed[strings.ToLower(hostname)]; !ok {
		logger.Warn().Str("host", hostname).Msg("Watermark request to disallowed host")
		return nil, &fetchError{http.StatusForbidden, "WATERMARK_HOST_NOT_ALLOWED", "host is not in the watermark allowlist"}
	}
	if isPrivateHost(hostname) {
		logger.Warn().Str("host", hostname).Msg("Watermark SSRF guard triggered")
		return nil, &fetchError{http.StatusForbidden, "WATERMARK_HOST_NOT_ALLOWED", "host is not allowed"}
	}

	fetchCtx, cancel := context.WithTimeout(ctx, watermarkFetchTimeout)
	defer cancel()

	data, contentType, dlErr := httputil.DownloadURL(fetchCtx, h.httpClient, raw, maxWatermarkBytes)
	if dlErr != nil {
		logger.Warn().Err(dlErr).Str("host", hostname).Msg("Watermark fetch failed")
		return nil, &fetchError{http.StatusBadGateway, "WATERMARK_FETCH_FAILED", "Failed to fetch the watermark"}
	}
	if !utils.IsImageContentType(contentType) {
		return nil, &fetchError{http.StatusUnprocessableEntity, "WATERMARK_NOT_AN_IMAGE", "Watermark is not an image"}
	}
	return data, nil
}
