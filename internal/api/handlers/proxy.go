package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
)

// defaultProxyAllowedHosts is the fallback allowlist when PROXY_ALLOWED_HOSTS is not set.
const defaultProxyAllowedHosts = "lh3.googleusercontent.com,lh4.googleusercontent.com,lh5.googleusercontent.com,lh6.googleusercontent.com,cf.geekdo-images.com,geekdo-images.com,boardgamegeek.com"

// proxyMaxBodyBytes caps the external image body to 10 MB.
const proxyMaxBodyBytes = 10 * 1024 * 1024

// defaultProxyMaxWidth is the fallback max width applied when no w/h params
// are provided. Override with PROXY_MAX_WIDTH env var.
const defaultProxyMaxWidth = 1200

// proxyAllowedHosts parses PROXY_ALLOWED_HOSTS from the environment, falling
// back to defaultProxyAllowedHosts. Returns a map for O(1) lookup.
func proxyAllowedHosts() map[string]struct{} {
	raw := strings.TrimSpace(os.Getenv("PROXY_ALLOWED_HOSTS"))
	if raw == "" {
		raw = defaultProxyAllowedHosts
	}
	m := make(map[string]struct{})
	for _, h := range strings.Split(raw, ",") {
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
	dot := strings.LastIndexByte(segment, '.')
	if dot < 0 {
		// No extension — use the default format.
		return segment, defaultFormat
	}
	possibleExt := strings.ToLower(segment[dot+1:])
	mapped, ok := AllowedImageExtensions[possibleExt]
	if !ok {
		return "", ""
	}
	return segment[:dot], mapped
}

// proxyCacheKey generates a deterministic cache key for a proxy request.
func proxyCacheKey(rawURL, ext, w, h, q, fit string) string {
	h256 := sha256.New()
	fmt.Fprintf(h256, "%s|%s|%s|%s|%s|%s", rawURL, ext, w, h, q, fit)
	return fmt.Sprintf("proxy:%x", h256.Sum(nil))
}

// HandleProxy fetches an image from an external URL, processes it with
// libvips, caches the result, and serves it with long-lived CDN headers.
//
// Route: GET /api/v1/proxy/*
// The wildcard captures "{hash}.{ext}" (e.g. "a1b2c3d4e5f6.webp").
func (h *Handler) HandleProxy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// ── 1. Extract and validate the path segment ──────────────────────
	segment := chi.URLParam(r, "*")
	if segment == "" {
		h.sendError(w, http.StatusBadRequest, "MISSING_SEGMENT", "Missing path segment")
		return
	}

	defaultFormat := h.config.Processing.DefaultFormat
	if defaultFormat == "" {
		defaultFormat = "webp"
	}

	_, extFormat := proxyExtFromSegment(segment, defaultFormat)
	if extFormat == "" {
		h.sendError(w, http.StatusBadRequest, "INVALID_EXTENSION", "Unknown or missing image extension")
		return
	}

	// ── 2. Obtain and validate the target URL ─────────────────────────
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		h.sendError(w, http.StatusBadRequest, "MISSING_URL", "url query parameter is required")
		return
	}

	if len(rawURL) > MaxURLLength {
		h.sendError(w, http.StatusBadRequest, "URL_TOO_LONG", "url exceeds maximum allowed length")
		return
	}

	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		h.sendError(w, http.StatusBadRequest, "INVALID_URL", "url must be an absolute http/https URL")
		return
	}

	hostname := parsed.Hostname()

	// ── 3. Allowlist check ────────────────────────────────────────────
	allowlist := cachedProxyAllowedHosts()
	if _, ok := allowlist[strings.ToLower(hostname)]; !ok {
		logger.Warn().Str("host", hostname).Msg("Proxy request to disallowed host")
		h.sendError(w, http.StatusForbidden, "HOST_NOT_ALLOWED", "host is not in the proxy allowlist")
		return
	}

	// ── 4. SSRF guard ─────────────────────────────────────────────────
	// isPrivateHost performs DNS resolution — only do this after the
	// allowlist check so we never resolve hostile/unexpected hostnames.
	if isPrivateHost(hostname) {
		logger.Warn().Str("host", hostname).Msg("Proxy SSRF guard triggered")
		h.sendError(w, http.StatusForbidden, "HOST_NOT_ALLOWED", "host is not allowed")
		return
	}

	// ── 5. Parse processing params from query string ──────────────────
	params := &processor.ProcessingParams{}

	if width := utils.GetQueryParam(r, "w", "width"); width != "" {
		if widthVal, err := strconv.Atoi(width); err == nil && widthVal > 0 && widthVal <= h.config.Processing.MaxDimensions.Width {
			if widthVal < MinDimensionPixels {
				h.sendError(w, http.StatusBadRequest, "INVALID_WIDTH", "Width must be at least 16 pixels")
				return
			}
			params.Width = widthVal
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_WIDTH", "Invalid width parameter")
			return
		}
	}

	if height := utils.GetQueryParam(r, "h", "height"); height != "" {
		if heightVal, err := strconv.Atoi(height); err == nil && heightVal > 0 && heightVal <= h.config.Processing.MaxDimensions.Height {
			if heightVal < MinDimensionPixels {
				h.sendError(w, http.StatusBadRequest, "INVALID_HEIGHT", "Height must be at least 16 pixels")
				return
			}
			params.Height = heightVal
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_HEIGHT", "Invalid height parameter")
			return
		}
	}

	if quality := utils.GetQueryParam(r, "q", "quality"); quality != "" {
		if q, err := strconv.Atoi(quality); err == nil && q > 0 && q <= 100 {
			params.Quality = q
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_QUALITY", "Invalid quality parameter")
			return
		}
	}

	if fit := r.URL.Query().Get("fit"); fit != "" {
		if fit == FitCover || fit == FitContain || fit == FitFill {
			params.Fit = fit
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_FIT", "Invalid fit parameter")
			return
		}
	}

	// Explicit ?f= overrides the path extension; extension is the default.
	if format := utils.GetQueryParam(r, "f", "format"); format != "" {
		if h.imageProcessor.ValidateFormat(format) {
			params.Format = format
		} else {
			h.sendError(w, http.StatusBadRequest, "INVALID_FORMAT", "Unsupported format")
			return
		}
	} else {
		params.Format = extFormat
	}

	params.AutoOrient = r.URL.Query().Get("orient") != "0"
	params.StripMetadata = r.URL.Query().Get("meta") != "1"

	// ── 5b. Safety-net max width ──────────────────────────────────────
	// When neither w nor h was supplied, cap width at PROXY_MAX_WIDTH (default
	// 1200) so cache misses on full-resolution originals don't propagate huge
	// files to the browser. Height is left unset so libvips scales
	// proportionally — no cropping.
	if params.Width == 0 && params.Height == 0 {
		maxW := defaultProxyMaxWidth
		if v := os.Getenv("PROXY_MAX_WIDTH"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				maxW = parsed
			}
		}
		params.Width = maxW
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
		contentType := h.imageProcessor.GetContentType(params.Format)
		meta := &storage.ImageMetadata{
			ID:          cacheKey,
			ContentType: contentType,
			Format:      params.Format,
			Size:        int64(len(cachedData)),
			CreatedAt:   time.Now(),
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		h.serveImage(w, r, bytes.NewReader(cachedData), meta)
		return
	}

	// ── 7. Fetch external image ───────────────────────────────────────
	fetchCtx, fetchCancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer fetchCancel()
	fetchReq, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		logger.Error().Err(err).Str("url", rawURL).Msg("Failed to build proxy fetch request")
		h.sendError(w, http.StatusBadRequest, "INVALID_URL", "Could not build request for url")
		return
	}
	fetchReq.Header.Set("User-Agent", "falco-proxy/1.0")

	resp, err := h.httpClient.Do(fetchReq)
	if err != nil {
		logger.Warn().Err(err).Str("url", rawURL).Msg("Proxy fetch failed")
		h.sendError(w, http.StatusBadGateway, "FETCH_FAILED", "Failed to fetch external image")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.Warn().Int("status", resp.StatusCode).Str("url", rawURL).Msg("Proxy upstream non-2xx")
		h.sendError(w, http.StatusBadGateway, "UPSTREAM_ERROR", "Upstream returned a non-2xx status")
		return
	}

	// Validate Content-Type is image/*
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(ct), "image/") {
		logger.Warn().Str("content_type", ct).Str("url", rawURL).Msg("Proxy upstream returned non-image content type")
		h.sendError(w, http.StatusUnsupportedMediaType, "NOT_AN_IMAGE", "Upstream did not return an image")
		return
	}

	limitedBody := io.LimitReader(resp.Body, proxyMaxBodyBytes+1)
	bodyBytes, err := io.ReadAll(limitedBody)
	if err != nil {
		logger.Error().Err(err).Str("url", rawURL).Msg("Failed to read proxy response body")
		h.sendError(w, http.StatusBadGateway, "FETCH_FAILED", "Failed to read external image")
		return
	}
	if int64(len(bodyBytes)) > proxyMaxBodyBytes {
		h.sendError(w, http.StatusRequestEntityTooLarge, "IMAGE_TOO_LARGE", "External image exceeds maximum allowed size")
		return
	}

	// ── 8. Process image ──────────────────────────────────────────────
	// Resolve format default now that we know params (same logic as delivery).
	if params.Format == "" {
		params.Format = defaultFormat
	}

	processedImage, err := h.imageProcessor.Process(ctx, bytes.NewReader(bodyBytes), params, cacheKey)
	if err != nil {
		logger.Error().Err(err).Str("url", rawURL).Msg("Failed to process proxy image")
		h.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
		return
	}
	defer processedImage.Data.Close()

	storageMeta := &storage.ImageMetadata{
		ID:          cacheKey,
		Format:      processedImage.Metadata.Format,
		Size:        processedImage.Metadata.Size,
		Width:       processedImage.Metadata.Width,
		Height:      processedImage.Metadata.Height,
		ContentType: processedImage.Metadata.ContentType,
		CreatedAt:   processedImage.Metadata.CreatedAt,
		// Proxy responses get long CDN TTLs; no per-request override.
		MaxAge:  86400,
		SMaxAge: 2592000,
	}
	if storageMeta.ContentType == "" {
		storageMeta.ContentType = h.imageProcessor.GetContentType(storageMeta.Format)
	}

	// ── 9. Set proxy-specific headers and serve ───────────────────────
	w.Header().Set("Access-Control-Allow-Origin", "*")
	h.serveImage(w, r, processedImage.Data, storageMeta)
}
