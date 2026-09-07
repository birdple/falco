package ui

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	views "github.com/birdple/falco/internal/api/views/templ"
	"github.com/birdple/falco/internal/storage"
	"github.com/birdple/falco/internal/version"
)

// detailTTL is how long the signed URL shown on an object page stays valid.
// Long enough to copy and try it, short enough that a screenshot is not a
// lasting credential.
const detailTTL = 1 * time.Hour

// Object renders one object with its real stored metadata.
func (h *Handler) Object(w http.ResponseWriter, r *http.Request) {
	sess := h.sessionFrom(r)
	query := r.URL.Query()

	bucket, err := h.resolveBucket(sess.Scope, query.Get("bucket"))
	if err != nil {
		h.deny(w, r, http.StatusForbidden, err.Error())
		return
	}

	key := strings.TrimPrefix(query.Get("key"), "/")
	name := key
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}

	data := views.ObjectDetail{
		Page:   h.pageData(r, sess, name, "explorer", nil),
		Bucket: bucket,
		Key:    key,
		Name:   name,
	}

	if key == "" {
		data.Error = "No object key was given."
		h.render(w, r, "object", views.ObjectPage(data))
		return
	}

	backend, err := h.registry.Get(bucket)
	if err != nil {
		data.Error = "Bucket not found: " + bucket
		h.render(w, r, "object", views.ObjectPage(data))
		return
	}

	// Retrieve is the only interface call that returns stored metadata. The
	// body is closed unread: backends stream on demand, so nothing beyond the
	// response header is transferred.
	reader, meta, err := backend.Retrieve(r.Context(), key)
	if err != nil {
		if storage.IsNotFound(err) {
			data.Error = "This object does not exist (any more) in " + bucket + "."
		} else {
			data.Error = err.Error()
		}
		h.render(w, r, "object", views.ObjectPage(data))
		return
	}
	_ = reader.Close()

	if meta != nil {
		data.Format = strings.ToUpper(meta.Format)
		data.ContentType = meta.ContentType
		data.SizeHuman = views.HumanizeBytes(meta.Size)
		data.Width = meta.Width
		data.Height = meta.Height
		data.OriginalName = meta.OriginalName
		data.OwnerID = meta.OwnerID
		data.ETag = meta.ETag
		data.CreatedAt = meta.CreatedAt
	}

	data.FullURL = h.signedThumb(bucket, key, 0)

	path := "/api/v1/images/" + key + "?" + url.Values{"b": {bucket}}.Encode()
	if signed, err := h.signPath(path, detailTTL); err == nil {
		data.CanSign = true
		data.SignedURL = signed
		data.ExpiresAt = signedExpiry(detailTTL)
	} else {
		data.SignNote = "Signing is unavailable: HMAC_KEY is not configured, so falco cannot mint signed URLs."
	}

	h.render(w, r, "object", views.ObjectPage(data))
}

// Playground renders the transformation workbench.
func (h *Handler) Playground(w http.ResponseWriter, r *http.Request) {
	sess := h.sessionFrom(r)
	query := r.URL.Query()

	bucket, err := h.resolveBucket(sess.Scope, query.Get("bucket"))
	if err != nil {
		h.deny(w, r, http.StatusForbidden, err.Error())
		return
	}

	key := strings.TrimPrefix(query.Get("key"), "/")
	names := h.accessibleBuckets(sess.Scope)

	data := views.PlaygroundData{
		Page:    h.pageData(r, sess, "Playground", "playground", nil),
		Bucket:  bucket,
		Key:     key,
		Buckets: names,
		Groups:  h.paramGroups(),
	}

	if key == "" {
		data.Error = "Pick an object from Storage, or paste a storage key above, to start transforming."
		h.render(w, r, "playground", views.PlaygroundPage(data))
		return
	}

	if backend, err := h.registry.Get(bucket); err == nil {
		if reader, meta, err := backend.Retrieve(r.Context(), key); err == nil {
			_ = reader.Close()
			if meta != nil {
				data.OriginalSizeHuman = views.HumanizeBytes(meta.Size)
				data.OriginalWidth = meta.Width
				data.OriginalHeight = meta.Height
				data.OriginalFormat = strings.ToUpper(meta.Format)
			}
		} else if storage.IsNotFound(err) {
			data.Error = "No object with that key in " + bucket + "."
		} else {
			data.Error = err.Error()
		}
	}

	h.render(w, r, "playground", views.PlaygroundPage(data))
}

// Signer renders the URL signer.
func (h *Handler) Signer(w http.ResponseWriter, r *http.Request) {
	sess := h.sessionFrom(r)

	data := views.SignerData{
		Page:       h.pageData(r, sess, "Sign URL", "signer", nil),
		Enabled:    h.signingEnabled(),
		Buckets:    h.accessibleBuckets(sess.Scope),
		DefaultTTL: 3600,
	}
	if !data.Enabled {
		data.DisabledReason = "HMAC_KEY and HMAC_SALT are not both configured, so falco cannot sign URLs. " +
			"POST /api/v1/sign answers 501 in this state."
	}
	data.RequireExpiry = requireExpiry()

	h.render(w, r, "signer", views.SignerPage(data))
}

// Ops renders the operations screen.
func (h *Handler) Ops(w http.ResponseWriter, r *http.Request) {
	sess := h.sessionFrom(r)

	data := views.OpsData{
		Page:           h.pageData(r, sess, "Operations", "ops", nil),
		Version:        version.Version,
		Uptime:         h.panelUptime(h.started),
		Status:         "ready",
		Sessions:       h.sessions.Count(),
		MetricsEnabled: h.cfg.Development.EnableMetrics,
		Features:       h.featureStates(),
		Config:         h.effectiveConfig(),
	}

	for _, name := range h.accessibleBuckets(sess.Scope) {
		entry := views.BackendStatus{Name: name, Type: h.bucketType(name)}
		if cfg, err := h.cfg.GetBucketConfig(name); err == nil {
			for _, bk := range cfg.Backups {
				entry.Backups = append(entry.Backups, views.BackupItem{Target: bk.Target, Mode: bk.Mode})
			}
		}

		backend, err := h.registry.Get(name)
		if err != nil {
			entry.Error = err.Error()
			data.Status = "degraded"
			data.Backends = append(data.Backends, entry)
			continue
		}

		_, entry.Paginated = backend.(storage.PagedLister)
		if sr, ok := backend.(interface{ StateName() string }); ok {
			entry.Breaker = sr.StateName()
		}

		ctx, cancel := contextWithTimeout(r, statsTimeout)
		if err := backend.Health(ctx); err != nil {
			entry.Error = err.Error()
			data.Status = "degraded"
		} else {
			entry.OK = true
		}
		if stats, err := backend.GetStats(ctx); err == nil {
			count := stats.TotalImages
			entry.Objects = &count
			entry.SizeHuman = views.HumanizeBytes(stats.TotalSize)
			if stats.FreeSpace > 0 {
				entry.HasFree = true
				entry.FreeHuman = views.HumanizeBytes(stats.FreeSpace)
			}
		}
		cancel()

		data.Backends = append(data.Backends, entry)
	}

	stats := h.processor.GetCacheStats()
	data.Cache = views.CacheView{
		Enabled:         h.cfg.Cache.SizeMB > 0,
		Backend:         stats.Backend,
		Hits:            stats.Hits,
		Misses:          stats.Misses,
		HitRatio:        stats.HitRatio,
		ItemCount:       stats.ItemCount,
		SizeHuman:       views.HumanizeBytes(stats.Size),
		MaxHuman:        views.HumanizeBytes(stats.MaxSize),
		TTLHours:        h.cfg.Cache.TTLHrs,
		CleanupInterval: h.cfg.Cache.CleanupInterval.String(),
		Note: "Counts transformed variants and proxy fetches only. Delivery without transformations " +
			"streams straight from storage and is never cached, so it never appears here.",
	}
	if stats.MaxSize > 0 {
		data.Cache.UsedPercent = float64(stats.Size) / float64(stats.MaxSize) * 100
	}

	h.render(w, r, "ops", views.OpsPage(data))
}
