package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/pkg/metrics"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// defaultProxyAllowedHosts is the fallback allowlist when PROXY_ALLOWED_HOSTS is not set.
const defaultProxyAllowedHosts = "lh3.googleusercontent.com,lh4.googleusercontent.com,lh5.googleusercontent.com,lh6.googleusercontent.com,cf.geekdo-images.com,geekdo-images.com,boardgamegeek.com"

// proxyMaxBodyBytes caps the external image body to 10 MB.
const proxyMaxBodyBytes = 10 * 1024 * 1024

// proxyMaxAge / proxySMaxAge are the CDN TTLs applied to proxy responses.
// Used on both the cache-hit and cache-miss paths below so the same
// resource never emits a different Cache-Control depending on LRU state.
const (
	proxyMaxAge  = 86400
	proxySMaxAge = 2592000
)

// defaultProxyMaxWidth is the fallback max width applied when no w/h params
// are provided. Override with PROXY_MAX_WIDTH env var. Sized to trigger a
// downscale on BGG's __itemrep@2x variant (~984 px wide).
const defaultProxyMaxWidth = 600

// defaultProxyQuality is the fallback webp/jpeg quality applied when no q
// param is provided. Override with PROXY_DEFAULT_QUALITY env var. Lower than
// delivery's 85 because proxy images come from external CDNs and never need
// archival quality.
const defaultProxyQuality = 75

// proxyAllowedHosts parses PROXY_ALLOWED_HOSTS from the environment, falling
// back to defaultProxyAllowedHosts. Returns a map for O(1) lookup.
func proxyAllowedHosts() map[string]struct{} {
	raw := strings.TrimSpace(os.Getenv("PROXY_ALLOWED_HOSTS"))
	if raw == "" {
		raw = defaultProxyAllowedHosts
	}
	m := make(map[string]struct{})
	for h := range strings.SplitSeq(raw, ",") {
		if t := strings.TrimSpace(h); t != "" {
			m[strings.ToLower(t)] = struct{}{}
		}
	}
	return m
}

var (
	proxyAllowlistOnce  sync.Once
	proxyAllowlistCache map[string]struct{}
)

func cachedProxyAllowedHosts() map[string]struct{} {
	proxyAllowlistOnce.Do(func() {
		proxyAllowlistCache = proxyAllowedHosts()
	})
	return proxyAllowlistCache
}

// isPrivateHost is an early-rejection guard before the authoritative
// dial-time SSRF check in httputil.NewSafeHTTPClient. Both must pass —
// this function provides a fast path for obvious private hostnames and
// rejects all resolved IPs including unspecified (0.0.0.0/::) which the
// safe client also blocks, but having both ensures defence in depth.
func isPrivateHost(hostname string) bool {
	lower := strings.ToLower(strings.TrimSpace(hostname))
	if lower == "localhost" ||
		strings.HasSuffix(lower, ".local") ||
		strings.HasSuffix(lower, ".internal") ||
		strings.HasSuffix(lower, ".svc.cluster.local") {
		return true
	}
	ips, err := net.LookupHost(hostname)
	if err != nil {
		return true // fail-closed
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}

// proxyExtFromSegment extracts the known image extension from a path segment
// like "a1b2c3.webp" and returns (strippedSegment, mappedFormat).
// If the segment has no dot, returns (segment, defaultFormat).
// If the segment has an unknown extension, returns ("", "").
func proxyExtFromSegment(segment, defaultFormat string) (id, format string) {
	base, possibleExt, found := strings.CutLast(segment, ".")
	if !found {
		// No extension — use the default format.
		return segment, defaultFormat
	}
	mapped, ok := AllowedImageExtensions[strings.ToLower(possibleExt)]
	if !ok {
		return "", ""
	}
	return base, mapped
}

// proxyCacheKey generates a deterministic cache key for a proxy request.
func proxyCacheKey(rawURL, ext, w, h, q, fit string) string {
	h256 := sha256.New()
	_, _ = fmt.Fprintf(h256, "%s|%s|%s|%s|%s|%s", rawURL, ext, w, h, q, fit)
	return fmt.Sprintf("proxy:%x", h256.Sum(nil))
}

// HandleProxy fetches an image from an external URL, processes it with
// libvips, caches the result, and serves it with long-lived CDN headers.
//
// Route: GET /api/v1/proxy/*
// The wildcard captures "{hash}.{ext}" (e.g. "a1b2c3d4e5f6.webp").
func (h *Handler) HandleProxy(w http.ResponseWriter, r *http.Request) {
	// Parsed once for the whole handler (see HandleDelivery).
	query := r.URL.Query()

	target, targetErr := h.resolveProxyTarget(r, query)
	if targetErr != nil {
		h.sendError(w, targetErr.status, targetErr.code, targetErr.message)
		return
	}
	rawURL, extFormat := target.rawURL, target.extFormat

	params, paramErr := h.parseProxyParams(query, extFormat)
	if paramErr != nil {
		h.sendError(w, http.StatusBadRequest, paramErr.code, paramErr.message)
		return
	}

	// ── 6. Cache key + LRU hit ────────────────────────────────────────
	cacheKey := proxyCacheKey(
		rawURL,
		params.Format,
		strconv.Itoa(params.Width),
		strconv.Itoa(params.Height),
		strconv.Itoa(params.Quality),
		params.Fit,
	)

	if cachedData, found := h.imageProcessor.GetFromCache(cacheKey); found {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		h.serveImage(w, r, bytes.NewReader(cachedData), h.buildProxyCachedMetadata(cacheKey, params.Format, len(cachedData)))
		return
	}

	if fe, found := h.recallFailure(cacheKey); found {
		h.sendError(w, fe.status, fe.code, fe.message)
		return
	}

	// Same fallback chain as delivery: requested, then configured, then webp.
	params.Format = h.resolveOutputFormat(params.Format)

	// ── 7-8. Fetch + process, deduplicated by cacheKey ─────────────────
	// sf.Do collapses N concurrent requests for the same (url, format, w,
	// h, q, fit) into a single fetch+decode+encode — measured as the
	// dominant cost when a crawler requests the same cold image several
	// times within milliseconds. Deliberately built on context.Background()
	// rather than r.Context(): this work is shared across every caller
	// waiting on this key, so one client disconnecting must not cancel the
	// fetch/process that other, still-connected clients are waiting on.
	v, err, _ := h.sf.Do(cacheKey, func() (any, error) {
		return h.fetchAndProcessRemote(rawURL, cacheKey, params)
	})

	if err != nil {
		if fe, ok := errors.AsType[*fetchError](err); ok {
			h.sendError(w, fe.status, fe.code, fe.message)
		} else {
			logger.Error().Err(err).Str("url", rawURL).Msg("Unexpected proxy singleflight error")
			h.sendError(w, http.StatusBadGateway, "FETCH_FAILED", "Failed to fetch external image")
		}
		return
	}

	result := v.(*proxyResult)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	h.serveImage(w, r, bytes.NewReader(result.data), result.meta)
}

// proxyResult is the singleflight.Do payload shared across every request
// waiting on the same cacheKey — must be safe to read concurrently, hence
// plain bytes rather than the one-shot io.ReadCloser Process() returns.
type proxyResult struct {
	data []byte
	meta *storage.ImageMetadata
}

// proxyTarget is a proxy request whose destination has been validated.
type proxyTarget struct {
	rawURL    string
	hostname  string
	extFormat string
}

// proxyError is a rejected proxy request, with the HTTP status it maps to.
// Unlike paramError, the status varies: a disallowed host is a 403, a malformed
// URL a 400.
type proxyError struct {
	status  int
	code    string
	message string
}

// resolveProxyTarget validates everything about where the request points before
// a single byte is fetched.
//
// The order of the checks is a security property, not a style choice:
//
//  1. the path extension has to name a known image format;
//  2. the url parameter has to be an absolute http/https URL within the length
//     cap;
//  3. its host has to be on the allowlist; and only then
//  4. the SSRF guard resolves it.
//
// Step 4 performs DNS resolution, so it comes last on purpose — resolving a
// hostname is itself an outbound request, and doing it before the allowlist
// check would let anyone make falco resolve arbitrary names.
func (h *Handler) resolveProxyTarget(r *http.Request, query url.Values) (proxyTarget, *proxyError) {
	segment := chi.URLParam(r, "*")
	if segment == "" {
		return proxyTarget{}, &proxyError{http.StatusBadRequest, "MISSING_SEGMENT", "Missing path segment"}
	}

	_, extFormat := proxyExtFromSegment(segment, h.resolveOutputFormat(""))
	if extFormat == "" {
		return proxyTarget{}, &proxyError{http.StatusBadRequest, "INVALID_EXTENSION", "Unknown or missing image extension"}
	}

	rawURL := query.Get("url")
	switch {
	case rawURL == "":
		return proxyTarget{}, &proxyError{http.StatusBadRequest, "MISSING_URL", "url query parameter is required"}
	case len(rawURL) > MaxURLLength:
		return proxyTarget{}, &proxyError{http.StatusBadRequest, "URL_TOO_LONG", "url exceeds maximum allowed length"}
	}

	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return proxyTarget{}, &proxyError{http.StatusBadRequest, "INVALID_URL", "url must be an absolute http/https URL"}
	}
	hostname := parsed.Hostname()

	if _, ok := cachedProxyAllowedHosts()[strings.ToLower(hostname)]; !ok {
		logger.Warn().Str("host", hostname).Msg("Proxy request to disallowed host")
		return proxyTarget{}, &proxyError{http.StatusForbidden, "HOST_NOT_ALLOWED", "host is not in the proxy allowlist"}
	}

	if isPrivateHost(hostname) {
		logger.Warn().Str("host", hostname).Msg("Proxy SSRF guard triggered")
		return proxyTarget{}, &proxyError{http.StatusForbidden, "HOST_NOT_ALLOWED", "host is not allowed"}
	}

	return proxyTarget{rawURL: rawURL, hostname: hostname, extFormat: extFormat}, nil
}

// parseProxyParams turns the proxy query string into processing parameters.
//
// It accepts a narrower set than delivery does — no crop, rotation or colour
// adjustment — because the proxy exists to re-encode somebody else's image at a
// sane size, not to be a general-purpose editor pointed at third-party CDNs.
//
// Two defaults that delivery does not apply:
//
//   - with neither w nor h given, width is capped at PROXY_MAX_WIDTH so that a
//     cache miss on a full-resolution original does not push a huge file to the
//     browser. Height stays unset so libvips scales proportionally, never crops.
//   - quality defaults lower than delivery's, because these images come from
//     external CDNs and are not archival.
func (h *Handler) parseProxyParams(query url.Values, extFormat string) (*processor.ProcessingParams, *paramError) {
	params := &processor.ProcessingParams{}

	if raw := utils.QueryParam(query, "w", "width"); raw != "" {
		width, err := parseDimension(raw, h.config.Processing.MaxDimensions.Width)
		if err != nil {
			return nil, &paramError{"INVALID_WIDTH", err.Error()}
		}
		params.Width = width
	}

	if raw := utils.QueryParam(query, "h", "height"); raw != "" {
		height, err := parseDimension(raw, h.config.Processing.MaxDimensions.Height)
		if err != nil {
			return nil, &paramError{"INVALID_HEIGHT", err.Error()}
		}
		params.Height = height
	}

	if raw := utils.QueryParam(query, "q", "quality"); raw != "" {
		quality, err := strconv.Atoi(raw)
		if err != nil || quality <= 0 || quality > maxQuality {
			return nil, &paramError{"INVALID_QUALITY", "Invalid quality parameter"}
		}
		params.Quality = quality
	}

	if raw := query.Get("fit"); raw != "" {
		if raw != FitCover && raw != FitContain && raw != FitFill {
			return nil, &paramError{"INVALID_FIT", "Invalid fit parameter"}
		}
		params.Fit = raw
	}

	// An explicit ?f= wins over the path extension; the extension is the default.
	switch raw := utils.QueryParam(query, "f", "format"); {
	case raw != "" && h.imageProcessor.ValidateFormat(raw):
		params.Format = raw
	case raw != "":
		return nil, &paramError{"INVALID_FORMAT", "Unsupported format"}
	default:
		params.Format = extFormat
	}

	params.AutoOrient = query.Get("orient") != "0"
	params.StripMetadata = query.Get("meta") != "1"

	if params.Width == 0 && params.Height == 0 {
		params.Width = envPositiveInt("PROXY_MAX_WIDTH", defaultProxyMaxWidth, 0)
	}
	if params.Quality == 0 {
		params.Quality = envPositiveInt("PROXY_DEFAULT_QUALITY", defaultProxyQuality, maxQuality)
	}

	return params, nil
}

// envPositiveInt reads a positive integer override from the environment,
// falling back to fallback when it is unset, unparseable, or outside
// (0, maxValue]. A maxValue of 0 means "no upper bound".
func envPositiveInt(name string, fallback, maxValue int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 || (maxValue > 0 && v > maxValue) {
		return fallback
	}
	return v
}

// buildProxyCachedMetadata describes proxied bytes served from the LRU cache.
//
// CreatedAt is a fixed epoch rather than time.Now(): cacheKey is already a
// content hash of (url, format, w, h, q, fit), so any real timestamp would be
// arbitrary — and a moving one changes the ETag on every response (base.go
// folds CreatedAt.Unix() into it), which defeats conditional GETs at the CDN
// edge.
func (h *Handler) buildProxyCachedMetadata(cacheKey, format string, size int) *storage.ImageMetadata {
	return &storage.ImageMetadata{
		ID:          cacheKey,
		ContentType: h.imageProcessor.GetContentType(format),
		Format:      format,
		Size:        int64(size),
		CreatedAt:   time.Unix(0, 0),
		MaxAge:      proxyMaxAge,
		SMaxAge:     proxySMaxAge,
	}
}

// fetchAndProcessRemote downloads an external image and re-encodes it. It is
// the body shared by every request that collapsed onto the same cache key.
//
// Its contexts come from context.Background(), not the request's: the work is
// shared, so one client hanging up must not cancel the fetch the others are
// still waiting on.
//
// Which failures get negative-cached is a deliberate distinction. A dead link, a
// non-image response or an oversized body are stable properties of the URL and
// are remembered, so a crawler hammering the same bad URL does not cost a fetch
// every time. A processing failure is NOT remembered: it can be transient, and
// caching it would keep serving an error after the cause is gone.
func (h *Handler) fetchAndProcessRemote(rawURL, cacheKey string, params *processor.ProcessingParams) (*proxyResult, error) {
	// Re-check both caches: a sibling request may have just populated
	// them while this goroutine was queued behind sf.Do's internal lock.
	if cachedData, found := h.imageProcessor.GetFromCache(cacheKey); found {
		return &proxyResult{
			data: cachedData,
			meta: h.buildProxyCachedMetadata(cacheKey, params.Format, len(cachedData)),
		}, nil
	}
	if fe, found := h.recallFailure(cacheKey); found {
		return nil, fe
	}

	m := metrics.Default()

	fetchCtx, fetchCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer fetchCancel()
	fetchReq, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		logger.Error().Err(err).Str("url", rawURL).Msg("Failed to build proxy fetch request")
		return nil, &fetchError{http.StatusBadRequest, "INVALID_URL", "Could not build request for url"}
	}
	fetchReq.Header.Set("User-Agent", "falco-proxy/1.0")

	// "fetch" is observed once, below, only on a successful read of the
	// full body — mixing in fast failures (bad host, connection refused)
	// would skew the histogram toward looking artificially fast and
	// defeat its purpose: telling "the CDN is slow" apart from "there's
	// a queue" (see the semaphore_wait metric in vips_processor.go).
	fetchStart := time.Now()
	resp, err := h.httpClient.Do(fetchReq)
	if err != nil {
		logger.Warn().Err(err).Str("url", rawURL).Msg("Proxy fetch failed")
		fe := &fetchError{http.StatusBadGateway, "FETCH_FAILED", "Failed to fetch external image"}
		h.rememberFailure(cacheKey, fe)
		return nil, fe
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Warn().Int("status", resp.StatusCode).Str("url", rawURL).Msg("Proxy upstream non-2xx")
		fe := &fetchError{http.StatusBadGateway, "UPSTREAM_ERROR", "Upstream returned a non-2xx status"}
		h.rememberFailure(cacheKey, fe)
		return nil, fe
	}

	// Validate Content-Type is image/*
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(ct), "image/") {
		logger.Warn().Str("content_type", ct).Str("url", rawURL).Msg("Proxy upstream returned non-image content type")
		fe := &fetchError{http.StatusUnsupportedMediaType, "NOT_AN_IMAGE", "Upstream did not return an image"}
		h.rememberFailure(cacheKey, fe)
		return nil, fe
	}

	limitedBody := io.LimitReader(resp.Body, proxyMaxBodyBytes+1)
	bodyBytes, err := io.ReadAll(limitedBody)
	if err != nil {
		logger.Error().Err(err).Str("url", rawURL).Msg("Failed to read proxy response body")
		fe := &fetchError{http.StatusBadGateway, "FETCH_FAILED", "Failed to read external image"}
		h.rememberFailure(cacheKey, fe)
		return nil, fe
	}
	m.ImageProcessingDuration.WithLabelValues("fetch").Observe(time.Since(fetchStart).Seconds())
	if int64(len(bodyBytes)) > proxyMaxBodyBytes {
		fe := &fetchError{http.StatusRequestEntityTooLarge, "IMAGE_TOO_LARGE", "External image exceeds maximum allowed size"}
		h.rememberFailure(cacheKey, fe)
		return nil, fe
	}

	// Processing failures are not negative-cached: unlike a dead link or
	// a non-image response, they're not a stable property of the URL
	// (could be a transient libvips/memory hiccup), so retrying on the
	// next request is the right default. Duration (semaphore_wait +
	// transform) is recorded inside Process() itself — vips_processor.go.
	processCtx, processCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer processCancel()
	processedImage, err := h.imageProcessor.Process(processCtx, bytes.NewReader(bodyBytes), params, cacheKey)
	if err != nil {
		m.ImageProcessingTotal.WithLabelValues(ct, params.Format, "error").Inc()
		logger.Error().Err(err).Str("url", rawURL).Msg("Failed to process proxy image")
		return nil, &fetchError{http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image"}
	}
	m.ImageProcessingTotal.WithLabelValues(ct, params.Format, "success").Inc()
	defer func() { _ = processedImage.Data.Close() }()
	data, err := io.ReadAll(processedImage.Data)
	if err != nil {
		logger.Error().Err(err).Str("url", rawURL).Msg("Failed to read processed proxy image")
		return nil, &fetchError{http.StatusInternalServerError, "PROCESSING_FAILED", "Failed to read processed image"}
	}

	meta := &storage.ImageMetadata{
		ID:          cacheKey,
		Format:      processedImage.Metadata.Format,
		Size:        processedImage.Metadata.Size,
		Width:       processedImage.Metadata.Width,
		Height:      processedImage.Metadata.Height,
		ContentType: processedImage.Metadata.ContentType,
		// Stable epoch, not processedImage.Metadata.CreatedAt (time.Now()):
		// see the cache-hit branch above — the same cacheKey must always
		// produce the same ETag, whether served from cold processing or a
		// warm LRU hit.
		CreatedAt: time.Unix(0, 0),
		// Proxy responses get long CDN TTLs; no per-request override.
		MaxAge:  proxyMaxAge,
		SMaxAge: proxySMaxAge,
	}
	if meta.ContentType == "" {
		meta.ContentType = h.imageProcessor.GetContentType(meta.Format)
	}
	return &proxyResult{data: data, meta: meta}, nil
}
