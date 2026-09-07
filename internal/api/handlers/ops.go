package handlers

// Operational endpoints: storage statistics, cache inspection and purge, and
// the readiness probe.
//
// These exist because the numbers behind them were already computed in Go and
// reachable from nowhere: GetStats and GetCacheStats had no HTTP route at all,
// so the only way to see a hit ratio was to scrape Prometheus, and there was no
// way to purge a cache entry short of deleting the image.

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/storage"
	"github.com/birdple/falco/internal/version"
)

// statsTimeout bounds a single backend's stats call.
//
// The dashboard used to ask every bucket in series with jay's own 5 s client
// timeout underneath, so N unreachable buckets cost N×5 s of a 30 s request
// budget. Here the calls run concurrently and each one is capped.
const statsTimeout = 5 * time.Second

// BucketStats is one bucket's entry in the stats response.
type BucketStats struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	IsDefault bool   `json:"is_default"`

	TotalImages int64 `json:"total_images"`
	TotalSize   int64 `json:"total_size_bytes"`

	// FreeSpace is a pointer because only a filesystem backend can answer it.
	// A plain 0 would be indistinguishable from "the disk is full", so absent
	// means "this backend does not report it".
	FreeSpace *int64 `json:"free_space_bytes"`

	// Error is set when the backend could not be asked. The other numbers are
	// then meaningless and must not be rendered as zeros.
	Error string `json:"error,omitempty"`
}

// StatsResponse is the body of GET /api/v1/stats.
type StatsResponse struct {
	Success bool          `json:"success"`
	Buckets []BucketStats `json:"buckets"`
}

// HandleStats reports per-bucket object counts and sizes.
//
// Only buckets the caller's scope can reach are included: a scoped key must not
// learn the size of a bucket it cannot read.
func (h *Handler) HandleStats(w http.ResponseWriter, r *http.Request) {
	if h.storageRegistry == nil {
		h.sendError(w, http.StatusInternalServerError, "NO_REGISTRY", "Storage registry is not configured")
		return
	}

	scope := apimw.GetScope(r.Context())
	names := h.accessibleBuckets(scope)
	out := make([]BucketStats, len(names))

	g, ctx := errgroup.WithContext(r.Context())
	// Bounded so that a deployment with many buckets does not open one
	// connection per bucket at once.
	g.SetLimit(8)

	defaultName := h.config.GetDefaultBucketName()
	for i, name := range names {
		g.Go(func() error {
			entry := BucketStats{Name: name, IsDefault: name == defaultName}
			if cfg, err := h.config.GetBucketConfig(name); err == nil {
				entry.Type = cfg.Type
			}

			backend, err := h.storageRegistry.Get(name)
			if err != nil {
				entry.Error = err.Error()
				out[i] = entry
				return nil
			}

			callCtx, cancel := context.WithTimeout(ctx, statsTimeout)
			defer cancel()

			stats, err := backend.GetStats(callCtx)
			if err != nil {
				// Reported, not swallowed: zeros here would read as "empty
				// bucket" when the truth is "we could not ask".
				entry.Error = err.Error()
				out[i] = entry
				return nil
			}

			entry.TotalImages = stats.TotalImages
			entry.TotalSize = stats.TotalSize
			if stats.FreeSpace > 0 {
				free := stats.FreeSpace
				entry.FreeSpace = &free
			}
			out[i] = entry
			return nil
		})
	}
	// No goroutine returns an error; they record failures per bucket instead,
	// so one unreachable backend does not blank the whole response.
	_ = g.Wait()

	writeJSON(w, http.StatusOK, StatsResponse{Success: true, Buckets: out})
}

// CacheResponse is the body of GET /api/v1/cache.
type CacheResponse struct {
	Success bool `json:"success"`
	// Enabled is false when caching is off entirely, in which case the
	// counters below are all zero for a reason worth stating.
	Enabled   bool    `json:"enabled"`
	Backend   string  `json:"backend"`
	Hits      int64   `json:"hits"`
	Misses    int64   `json:"misses"`
	Size      int64   `json:"size_bytes"`
	MaxSize   int64   `json:"max_size_bytes"`
	ItemCount int     `json:"item_count"`
	HitRatio  float64 `json:"hit_ratio"`
	// TTLHours and CleanupInterval are distinct knobs that get confused for
	// each other, so both are reported.
	TTLHours        int    `json:"ttl_hours"`
	CleanupInterval string `json:"cleanup_interval"`
	// Note explains what the numbers do NOT cover.
	Note string `json:"note"`
}

// HandleCacheStats reports the state of the transform cache.
func (h *Handler) HandleCacheStats(w http.ResponseWriter, r *http.Request) {
	stats := h.imageProcessor.GetCacheStats()

	writeJSON(w, http.StatusOK, CacheResponse{
		Success:         true,
		Enabled:         h.config.Cache.SizeMB > 0,
		Backend:         stats.Backend,
		Hits:            stats.Hits,
		Misses:          stats.Misses,
		Size:            stats.Size,
		MaxSize:         stats.MaxSize,
		ItemCount:       stats.ItemCount,
		HitRatio:        stats.HitRatio,
		TTLHours:        h.config.Cache.TTLHrs,
		CleanupInterval: h.config.Cache.CleanupInterval.String(),
		// Delivery with no transformations streams straight from storage and
		// is never cached, so this ratio describes transformed output only.
		// Without saying so, a low ratio looks like a cache problem.
		Note: "counts transformed variants and proxy fetches only; untransformed delivery streams from storage and is not cached",
	})
}

// PurgeResponse is the body of DELETE /api/v1/cache.
type PurgeResponse struct {
	Success bool `json:"success"`
	// Purged is how many entries were actually dropped. Answering without a
	// count is how a no-op passes for a purge.
	Purged int    `json:"purged"`
	Scope  string `json:"scope"`
	Key    string `json:"key,omitempty"`
}

// HandleCachePurge drops cached variants, either all of them or every variant
// of one storage key.
//
// Admin only: a scoped key purging the whole cache would degrade every other
// tenant's latency, and cache keys are hashes of storage keys, so there is no
// way to check a per-bucket scope against them.
func (h *Handler) HandleCachePurge(w http.ResponseWriter, r *http.Request) {
	scope := apimw.GetScope(r.Context())
	if scope != nil && !scope.IsAdmin {
		h.sendError(w, http.StatusForbidden, "ACCESS_DENIED", "Purging the cache requires an admin key")
		return
	}

	if key := strings.TrimSpace(r.URL.Query().Get("key")); key != "" {
		removed := h.imageProcessor.InvalidateCacheForKey(key)
		logger.Info().Str("key", key).Int("purged", removed).Msg("Cache purged for key")
		writeJSON(w, http.StatusOK, PurgeResponse{Success: true, Purged: removed, Scope: "key", Key: key})
		return
	}

	removed := h.imageProcessor.PurgeCache()
	logger.Info().Int("purged", removed).Msg("Cache purged")
	writeJSON(w, http.StatusOK, PurgeResponse{Success: true, Purged: removed, Scope: "all"})
}

// BackendHealth is one backend's entry in the readiness response.
type BackendHealth struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Breaker is the circuit breaker state: "closed", "open" or "half-open".
	// Empty when the backend is not wrapped by one.
	Breaker string `json:"breaker,omitempty"`
}

// ReadyResponse is the body of GET /health/ready.
type ReadyResponse struct {
	Status   string          `json:"status"`
	Version  string          `json:"version"`
	Uptime   string          `json:"uptime"`
	Backends []BackendHealth `json:"backends"`
	Cache    struct {
		Enabled   bool `json:"enabled"`
		ItemCount int  `json:"item_count"`
	} `json:"cache"`
	// Disabled names the features that are off because they are not
	// configured. A feature that is silently unavailable is worse than one
	// that says so.
	Disabled []string `json:"disabled,omitempty"`
}

// stateReporter is implemented by the circuit breaker wrapper. Declared here
// rather than importing the package so that handlers do not depend on the
// breaker just to name its state.
type stateReporter interface{ StateName() string }

// HandleReady is a live readiness probe: it checks every registered backend,
// not just the default one.
//
// /health answers the orchestrator's "is this process up" and only pings the
// default backend. This one answers "what exactly is wrong", which is the
// question asked when uploads fail while delivery still works.
func (h *Handler) HandleReady(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	resp := ReadyResponse{
		Status:  "ready",
		Version: version.Version,
		Uptime:  time.Since(h.startTime).String(),
	}

	cacheStats := h.imageProcessor.GetCacheStats()
	resp.Cache.Enabled = h.config.Cache.SizeMB > 0
	resp.Cache.ItemCount = cacheStats.ItemCount

	resp.Disabled = h.disabledFeatures()

	names := []string{}
	if h.storageRegistry != nil {
		for _, n := range h.storageRegistry.Names() {
			if n == registryDefaultAlias {
				continue
			}
			names = append(names, n)
		}
		sort.Strings(names)
	}

	healthy := true
	for _, name := range names {
		entry := BackendHealth{Name: name}
		if cfg, err := h.config.GetBucketConfig(name); err == nil {
			entry.Type = cfg.Type
		}

		backend, err := h.storageRegistry.Get(name)
		if err != nil {
			entry.Error = err.Error()
			healthy = false
			resp.Backends = append(resp.Backends, entry)
			continue
		}
		if sr, ok := backend.(stateReporter); ok {
			entry.Breaker = sr.StateName()
		}

		callCtx, cancel := context.WithTimeout(ctx, statsTimeout)
		err = backend.Health(callCtx)
		cancel()
		if err != nil {
			entry.Error = err.Error()
			healthy = false
		} else {
			entry.OK = true
		}
		resp.Backends = append(resp.Backends, entry)
	}

	status := http.StatusOK
	if !healthy {
		resp.Status = "degraded"
		status = http.StatusServiceUnavailable
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, resp)
}

// disabledFeatures lists what is switched off for lack of configuration.
//
// Each of these fails at the moment of use with an error that looks like a bug
// unless you already know the variable is missing; naming them here is what
// turns "the watermark 403s" into "WATERMARK_ALLOWED_HOSTS is unset".
func (h *Handler) disabledFeatures() []string {
	var off []string
	if h.config.Security.HMACKey == "" {
		off = append(off, "url_signing: HMAC_KEY is unset (POST /api/v1/sign answers 501)")
	}
	if watermarkAllowedHosts() == nil {
		off = append(off, "watermark_url: WATERMARK_ALLOWED_HOSTS is unset (?wm_url= answers 403)")
	}
	if !h.config.Development.EnableMetrics {
		off = append(off, "metrics: ENABLE_METRICS is off (/metrics is not mounted)")
	}
	if h.config.Cache.SizeMB <= 0 {
		off = append(off, "cache: CACHE_SIZE_MB is 0 (every transform is recomputed)")
	}
	return off
}

// accessibleBuckets returns the bucket names the scope may reach, sorted.
func (h *Handler) accessibleBuckets(scope *apimw.APIScope) []string {
	if h.storageRegistry == nil {
		return nil
	}
	var out []string
	for _, name := range h.storageRegistry.Names() {
		// The "default" entry is an alias for a bucket that is already listed
		// under its real name; including it would double every count.
		if name == registryDefaultAlias {
			continue
		}
		if scope == nil || scope.CanAccessBucket(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// registryDefaultAlias is the key the registry files its default backend under,
// in addition to the backend's real name.
const registryDefaultAlias = "default"

var _ = storage.ErrBackendNotFound // keep the storage import meaningful for future use
