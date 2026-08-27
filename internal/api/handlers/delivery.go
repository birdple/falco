package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/pkg/metrics"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/security"
	"github.com/birdple/falco/internal/storage"
)

// authenticateAndScope authenticates a delivery request via X-API-Key /
// Authorization when HMAC is not gating the route. Returns the resolved
// APIScope (possibly admin) and true on success; false on any auth failure.
//
// It honors scoped API keys (bucket/group/subgroup keys from scoped_auth.go)
// when they exist, and falls back to the admin API key otherwise. This keeps
// upstream multi-tenant deployments working while closing the bypass that
// previously exempted /api/v1/images/ from all auth.
func (h *Handler) authenticateAndScope(r *http.Request) (*apimw.APIScope, bool) {
	// Prefer scoped auth when any scoped keys are configured.
	scopedAuth := apimw.NewScopedAPIKeyAuth(h.config.Security.APIKey, h.config)
	if scopedAuth.HasScopedKeys() {
		return scopedAuth.AuthenticateRequest(r)
	}
	// No scoped keys — the birdple-v2 simple case. Validate the admin key
	// directly and yield an admin scope (unrestricted).
	simple := apimw.NewAPIKeyAuth(h.config.Security.APIKey)
	if !simple.AuthenticateRequest(r) {
		return nil, false
	}
	return &apimw.APIScope{IsAdmin: true}, true
}

// hmacRequireExpiry reads HMAC_REQUIRE_EXPIRY from the environment. Per the
// monorepo "no insecure default" rule, this variable MUST be set explicitly;
// the function returns an error if it is missing or unparseable. Callers
// should reject the request when the error is non-nil rather than pick a
// default.
func hmacRequireExpiry() (bool, error) {
	raw := strings.TrimSpace(os.Getenv("HMAC_REQUIRE_EXPIRY"))
	if raw == "" {
		return false, errors.New("HMAC_REQUIRE_EXPIRY is not set")
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("HMAC_REQUIRE_EXPIRY invalid: %w", err)
	}
	return v, nil
}

// HandleDelivery handles image delivery requests with optional transformations
func (h *Handler) HandleDelivery(w http.ResponseWriter, r *http.Request) {
	// Parsed once for the whole handler: r.URL.Query() reparses RawQuery from
	// scratch on every call, and it is consulted 16 times below.
	query := r.URL.Query()

	imageID, extFormat, idErr := resolveImageID(r)
	if idErr != nil {
		h.sendError(w, http.StatusBadRequest, idErr.code, idErr.message)
		return
	}

	authorized, r := h.authorizeDelivery(w, r, query)
	if !authorized {
		return
	}

	// Get storage, bucket, and directory parameters
	storageName := query.Get("storage")
	bucket := utils.QueryParam(query, "b", "bucket")
	directory := utils.QueryParam(query, "d", "dir", "directory")

	directory = utils.NormalizeDirectoryPath(directory)
	if err := utils.ValidateDirectoryPath(directory); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_DIRECTORY", fmt.Sprintf("Invalid directory path: %v", err))
		return
	}

	storageKey := utils.BuildStorageKey(directory, imageID)

	storageBackend, err := h.getStorageBackendScoped(r, storageName, bucket)
	if err != nil {
		h.sendError(w, http.StatusForbidden, "ACCESS_DENIED", err.Error())
		return
	}

	params, paramErr := h.parseDeliveryParams(query, extFormat)
	if paramErr != nil {
		h.sendError(w, http.StatusBadRequest, paramErr.code, paramErr.message)
		return
	}

	m := metrics.Default()
	hasTransformations := wantsTransformation(params)

	// When the request carries transformations or an explicit format, the cache
	// key is computable from (storageKey, params) alone — no storage round-trip
	// needed — so that path gets to answer from cache before touching Jay.
	if hasTransformations || params.Format != "" {
		h.deliverProcessed(w, r, deliveryRequest{
			storageBackend:     storageBackend,
			storageKey:         storageKey,
			imageID:            imageID,
			params:             params,
			hasTransformations: hasTransformations,
			metrics:            m,
		})
		return
	}

	h.deliverRaw(w, r, deliveryRequest{
		storageBackend:     storageBackend,
		storageKey:         storageKey,
		params:             params,
		hasTransformations: hasTransformations,
		metrics:            m,
	})
}

// deliveryRequest bundles what both delivery paths need, so neither ends up
// with a nine-parameter signature.
type deliveryRequest struct {
	storageBackend     storage.StorageBackend
	storageKey         string
	imageID            string
	params             *processor.ProcessingParams
	hasTransformations bool
	metrics            *metrics.Metrics
}

// deliverProcessed serves a request that needs work done on the image.
//
// The cache is consulted first, before any storage round-trip. On a miss, the
// retrieve-and-process work is deduplicated with singleflight so that N
// concurrent requests for the same resize pay for one Jay fetch, one decode and
// one encode between them, instead of N of each.
//
// The result is buffered rather than streamed precisely because it is shared:
// siblings waiting on the same key all get the same bytes.
func (h *Handler) deliverProcessed(w http.ResponseWriter, r *http.Request, req deliveryRequest) {
	storageKey, imageID, params := req.storageKey, req.imageID, req.params

	// Resolve the output format now so the cache key is deterministic.
	params.Format = h.resolveOutputFormat(params.Format)

	cacheKey := h.imageProcessor.GenerateCacheKey(storageKey, params)
	if cachedData, found := h.imageProcessor.GetFromCache(cacheKey); found {
		h.serveImage(w, r, bytes.NewReader(cachedData), h.buildCachedMetadata(imageID, params, len(cachedData)))
		return
	}

	// Cache miss with an explicit transform: cacheKey is already known,
	// so dedup the retrieve+process work across concurrent requests for
	// the same (storageKey, params) — same fix as HandleProxy's
	// singleflight, and the same shape of bug (N concurrent requests for
	// the same resize each paying their own Jay round-trip + decode +
	// encode). Deliberately built on context.Background(), not
	// r.Context(): this work is shared, so one client disconnecting must
	// not cancel it for siblings still waiting on it.
	v, sfErr, _ := h.sf.Do(cacheKey, func() (any, error) {
		return h.fetchAndProcess(req, cacheKey)
	})

	if sfErr != nil {
		if fe, ok := errors.AsType[*fetchError](sfErr); ok {
			h.sendError(w, fe.status, fe.code, fe.message)
		} else {
			logger.Error().Err(sfErr).Msg("Unexpected delivery singleflight error")
			h.sendError(w, http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to deliver image")
		}
		return
	}
	result := v.(*deliveryResult)
	h.serveImage(w, r, bytes.NewReader(result.data), result.meta)
}

// deliverRaw streams the stored object straight through.
//
// No deduplication here on purpose: there is no CPU-heavy work worth sharing,
// and streaming keeps memory flat no matter how large the file is — which is
// what the common "download the original as-is" case needs.
func (h *Handler) deliverRaw(w http.ResponseWriter, r *http.Request, req deliveryRequest) {
	ctx := r.Context()
	storageBackend, storageKey := req.storageBackend, req.storageKey
	params, hasTransformations, m := req.params, req.hasTransformations, req.metrics
	var cacheKey string

	// ── Raw delivery (no explicit transform requested) — fetch from
	// storage and stream directly. No dedup here: there's no CPU-heavy
	// work to share, and streaming (vs. the buffered path above) keeps
	// memory flat regardless of file size — worth preserving for the
	// common case of downloading a large original as-is.
	storageStart := time.Now()
	reader, metadata, err := storageBackend.Retrieve(ctx, storageKey)
	storageDuration := time.Since(storageStart).Seconds()

	if err != nil {
		m.StorageOperationsTotal.WithLabelValues("retrieve", h.defaultStorageType(), "error").Inc()
		if storage.IsNotFound(err) {
			h.sendError(w, http.StatusNotFound, "IMAGE_NOT_FOUND", "Image not found")
			return
		}
		logger.Error().Err(err).Msg("Failed to retrieve image")
		h.sendError(w, http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to retrieve image")
		return
	}
	m.StorageOperationsTotal.WithLabelValues("retrieve", h.defaultStorageType(), "success").Inc()
	m.StorageOperationDuration.WithLabelValues("retrieve", h.defaultStorageType()).Observe(storageDuration)
	defer func() { _ = reader.Close() }()

	// Non-image files: serve directly without any processing
	isImage := utils.IsImageContentType(metadata.ContentType)
	if !isImage {
		h.serveImage(w, r, reader, metadata)
		return
	}

	// Re-evaluate processing need now that we have storage metadata
	// (handles edge cases like unknown format in storage).
	needsProcessing := hasTransformations ||
		(params.Format != "" && params.Format != metadata.Format) ||
		(metadata.Format == "" || metadata.ContentType == "" || metadata.ContentType == "application/octet-stream")

	if !needsProcessing {
		h.serveImage(w, r, reader, metadata)
		return
	}

	// Ensure format + cacheKey are set (handles the metadata-only edge
	// case where wantsProcessing was false but needsProcessing is true).
	// Not deduplicated: cacheKey couldn't be known before this retrieve, so
	// there's no key to dedup on ahead of time. Rare in practice — the
	// common "unknown storage format" case is handled by upload-time
	// metadata, not discovered here.
	params.Format = h.resolveOutputFormat(params.Format)
	if cacheKey == "" {
		cacheKey = h.imageProcessor.GenerateCacheKey(storageKey, params)
	}

	// Duration (split into semaphore_wait + transform) is recorded inside
	// Process() itself now — see vips_processor.go — so only the pass/fail
	// counter, which needs the format labels, stays here.
	processedImage, err := h.imageProcessor.Process(ctx, reader, params, cacheKey)

	if err != nil {
		m.ImageProcessingTotal.WithLabelValues(metadata.Format, params.Format, "error").Inc()
		logger.Error().Err(err).Msg("Failed to process image")
		h.sendError(w, http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image")
		return
	}
	m.ImageProcessingTotal.WithLabelValues(metadata.Format, params.Format, "success").Inc()

	defer func() { _ = processedImage.Data.Close() }()

	h.serveImage(w, r, processedImage.Data, h.buildProcessedMetadata(processedImage, params))
}

// deliveryResult is the singleflight.Do payload shared across every request
// waiting on the same cacheKey — must be safe to read concurrently, hence
// plain bytes rather than a one-shot io.ReadCloser.
type deliveryResult struct {
	data []byte
	meta *storage.ImageMetadata
}

// paramError is a rejected query parameter: the API error code and the message
// that goes back to the client. Carrying both lets the parsing helpers stay
// free of the ResponseWriter.
type paramError struct {
	code    string
	message string
}

// parseDeliveryParams turns the delivery query string into processing
// parameters.
//
// It is deliberately free of I/O and of the ResponseWriter: every rejection
// comes back as a paramError so the whole parameter contract can be exercised
// in a unit test without a server.
//
// Two classes of parameter, and the difference is intentional:
//
//   - the ones that change the image geometry or encoding (w, h, q, f, fit)
//     reject the request when malformed — silently ignoring a typo would serve
//     an image that is not the one asked for;
//   - the cosmetic and caching ones (maxage, gravity, padding, wm_scale) fall
//     back to their default when malformed, because the useful response is
//     still the image.
//
// extFormat is the format taken from a path extension (/images/abc.webp) and
// acts as the default when no ?f= is given.
func (h *Handler) parseDeliveryParams(query url.Values, extFormat string) (*processor.ProcessingParams, *paramError) {
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

	switch raw := utils.QueryParam(query, "f", "format"); {
	case raw != "" && h.imageProcessor.ValidateFormat(raw):
		params.Format = raw
	case raw != "":
		return nil, &paramError{"INVALID_FORMAT", "Unsupported format"}
	default:
		// A known extension in the path acts as the format default. This is
		// what lets a CDN cache by file extension with no query-string tricks.
		params.Format = extFormat
	}

	if raw := query.Get("fit"); raw != "" {
		if raw != FitCover && raw != FitContain && raw != FitFill {
			return nil, &paramError{"INVALID_FIT", "Invalid fit parameter"}
		}
		params.Fit = raw
	}

	// From here down every parameter is best-effort: a malformed value leaves
	// the default in place instead of failing the request.
	params.MaxAge = nonNegativeInt(query.Get("maxage"), params.MaxAge)
	params.SMaxAge = nonNegativeInt(query.Get("smaxage"), params.SMaxAge)

	if raw := query.Get("gravity"); validGravities[raw] {
		params.Gravity = raw
	}

	if raw := query.Get("wm_scale"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 && v <= 1 {
			params.WatermarkScale = v
		}
	}

	if query.Get("trim") == "1" {
		params.TrimEnabled = true
		if raw := query.Get("trim_threshold"); raw != "" {
			if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 && v <= maxTrimThreshold {
				params.TrimThreshold = v
			}
		}
	}

	params.PaddingTop = nonNegativeInt(query.Get("pad_top"), params.PaddingTop)
	params.PaddingRight = nonNegativeInt(query.Get("pad_right"), params.PaddingRight)
	params.PaddingBottom = nonNegativeInt(query.Get("pad_bottom"), params.PaddingBottom)
	params.PaddingLeft = nonNegativeInt(query.Get("pad_left"), params.PaddingLeft)
	params.PaddingColor = query.Get("pad_color")

	// Both default to on, so the query string opts *out* rather than in.
	params.AutoOrient = query.Get("orient") != "0"
	params.StripMetadata = query.Get("meta") != "1"

	return params, nil
}

// parseDimension parses a width or height and checks it against the configured
// ceiling. The floor exists because anything smaller is not a thumbnail, it is
// a decode plus an encode for an image nobody can see.
func parseDimension(raw string, maxValue int) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maxValue {
		return 0, errors.New("invalid parameter")
	}
	if value < MinDimensionPixels {
		return 0, fmt.Errorf("must be at least %d pixels", MinDimensionPixels)
	}
	return value, nil
}

// nonNegativeInt parses an optional non-negative integer, returning fallback
// when the value is absent or malformed.
func nonNegativeInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
		return v
	}
	return fallback
}

// resolveImageID pulls the image id out of the path and separates the format
// hint carried by a file extension.
//
// The chi "*" wildcard can match a directory/id pair, so only the final segment
// is validated as an id; the directory part is checked separately by
// ValidateDirectoryPath.
//
// A trailing ".webp" is stripped and returned as extFormat, which then acts as
// the default when no ?f= is given — that is what lets a CDN cache the URL by
// file extension with no query-string tricks. The dot is looked for in the LAST
// segment only: a directory with a dot in its name (dir.v2/abc123) carries no
// extension and must not be rejected. A dot followed by anything that is not a
// known extension IS rejected, because guessing there would silently serve a
// different image than the one asked for.
func resolveImageID(r *http.Request) (imageID, extFormat string, err *paramError) {
	imageID = chi.URLParam(r, "*")
	if imageID == "" {
		imageID = chi.URLParam(r, "id")
	}
	if imageID == "" {
		return "", "", &paramError{"MISSING_ID", "Image ID is required"}
	}

	dirPrefix, finalSegment := "", imageID
	if before, after, ok := strings.CutLast(imageID, "/"); ok {
		dirPrefix, finalSegment = before+"/", after
	}

	if base, possibleExt, ok := strings.CutLast(finalSegment, "."); ok {
		mapped, found := AllowedImageExtensions[strings.ToLower(possibleExt)]
		if !found {
			return "", "", &paramError{"INVALID_ID", "Invalid image id"}
		}
		extFormat = mapped
		finalSegment = base
		// Rebuilt by re-joining the prefix, never by trimming lengths off the
		// original string.
		imageID = dirPrefix + base
	}

	if !utils.IsValidImageID(finalSegment) {
		return "", "", &paramError{"INVALID_ID", "Invalid image id"}
	}
	return imageID, extFormat, nil
}

// authorizeDelivery enforces access control on the delivery route and returns
// the request to carry on with — it may have gained an auth scope in its
// context.
//
// Two regimes, and which one applies is a deployment decision:
//
//   - HMAC_REQUIRED=true: delivery is public-by-signature. The URL signature
//     authorizes that exact path plus query, so no API key is needed — a
//     browser cannot attach one to an <img> URL anyway.
//   - HMAC_REQUIRED=false: fall back to API key plus scope, so delivery is not
//     left wide open on a dev or misconfigured deployment.
//
// Returns false once it has already written the error response.
func (h *Handler) authorizeDelivery(w http.ResponseWriter, r *http.Request, query url.Values) (bool, *http.Request) {
	if h.config.Security.HMACRequired {
		return h.verifyDeliverySignature(w, r, query), r
	}

	if !h.config.Security.APIKeyRequired {
		return true, r
	}

	// Scope is honoured here too, so a key limited to one bucket cannot be
	// used to read another one through the delivery route.
	scope, ok := h.authenticateAndScope(r)
	if !ok {
		h.sendError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
		return false, r
	}
	return true, r.WithContext(apimw.WithScope(r.Context(), scope))
}

// verifyDeliverySignature checks the HMAC signature covering path and query.
//
// A missing or unparseable HMAC_REQUIRE_EXPIRY is a 500, not a default: the
// monorepo rule is that an unset variable refuses to work rather than degrading
// silently, and the degradation here would be accepting signed URLs that never
// expire.
func (h *Handler) verifyDeliverySignature(w http.ResponseWriter, r *http.Request, query url.Values) bool {
	signedPath := r.URL.Path
	if raw := r.URL.RawQuery; raw != "" {
		signedPath = r.URL.Path + "?" + raw
	}

	requireExpiry, err := hmacRequireExpiry()
	if err != nil {
		logger.Error().Err(err).Msg("HMAC_REQUIRE_EXPIRY misconfigured")
		h.sendError(w, http.StatusInternalServerError, "CONFIG_ERROR", "server HMAC expiry policy not configured")
		return false
	}

	if err := security.VerifyURLWithPolicy(
		query.Get("sig"),
		signedPath,
		h.config.Security.HMACKey,
		h.config.Security.HMACKeySalt,
		h.config.Security.HMACSignatureSize,
		true,
		requireExpiry,
	); err != nil {
		logger.Warn().Str("error", err.Error()).Msg("Invalid signature")
		h.sendError(w, http.StatusForbidden, "INVALID_SIGNATURE", "Invalid or missing URL signature")
		return false
	}
	return true
}

// fallbackFormat is the encoding used when neither the request nor the
// configuration names one.
const fallbackFormat = "webp"

// wantsTransformation reports whether the request asks for anything that
// changes the pixels.
//
// It is decidable from the query string alone, before any storage I/O — which
// is the whole point: it is what lets the cache-first path compute a cache key
// and answer without ever talking to storage.
//
// Format is deliberately NOT part of this: asking for the same image in another
// encoding needs processing but is not a transformation of the image itself,
// and the two are handled by different branches below.
func wantsTransformation(p *processor.ProcessingParams) bool {
	return p.Width != 0 || p.Height != 0 || p.Quality != 0 ||
		p.CropW != 0 || p.CropH != 0 ||
		p.Rotate != 0 || p.Flip != "" || p.Flop ||
		p.Brightness != 0 || p.Contrast != 0 || p.Gamma != 0 ||
		p.Saturation != 0 || p.Hue != 0 || p.Blur != 0 || p.Sharpen != 0 ||
		p.Gravity != "" || p.TrimEnabled ||
		p.PaddingTop != 0 || p.PaddingRight != 0 || p.PaddingBottom != 0 || p.PaddingLeft != 0
}

// resolveOutputFormat settles the encoding to produce: what the caller asked
// for, else the configured default, else webp.
func (h *Handler) resolveOutputFormat(requested string) string {
	if requested != "" {
		return requested
	}
	if h.config.Processing.DefaultFormat != "" {
		return h.config.Processing.DefaultFormat
	}
	return fallbackFormat
}

// buildProcessedMetadata converts processor output into the storage metadata
// shape that serveImage expects, carrying over the caching directives that came
// from the query string.
func (h *Handler) buildProcessedMetadata(processed *processor.ProcessedImage, params *processor.ProcessingParams) *storage.ImageMetadata {
	meta := &storage.ImageMetadata{
		ID:           processed.Metadata.ID,
		OriginalName: processed.Metadata.OriginalName,
		Format:       processed.Metadata.Format,
		Size:         processed.Metadata.Size,
		Width:        processed.Metadata.Width,
		Height:       processed.Metadata.Height,
		ContentType:  processed.Metadata.ContentType,
		CreatedAt:    processed.Metadata.CreatedAt,
		MaxAge:       params.MaxAge,
		SMaxAge:      params.SMaxAge,
	}
	if meta.ContentType == "" {
		meta.ContentType = h.imageProcessor.GetContentType(meta.Format)
	}
	return meta
}

// buildCachedMetadata describes a byte slice served straight from the LRU cache.
//
// CreatedAt is a fixed epoch on purpose: the content is addressed by id, so any
// real timestamp here would be arbitrary — and an arbitrary one would make the
// ETag and Last-Modified differ between a cache hit and a cache miss for the
// exact same bytes.
func (h *Handler) buildCachedMetadata(imageID string, params *processor.ProcessingParams, size int) *storage.ImageMetadata {
	return &storage.ImageMetadata{
		ID:          imageID,
		ContentType: h.imageProcessor.GetContentType(params.Format),
		Format:      params.Format,
		Size:        int64(size),
		MaxAge:      params.MaxAge,
		SMaxAge:     params.SMaxAge,
		CreatedAt:   time.Unix(0, 0),
	}
}

// fetchAndProcess retrieves the original and transforms it. It is the body
// shared by every request that collapsed onto the same singleflight key.
//
// Its contexts are built on context.Background() rather than the request's, and
// that is deliberate: this work is shared, so one client hanging up must not
// cancel it for the siblings still waiting on the result.
func (h *Handler) fetchAndProcess(req deliveryRequest, cacheKey string) (*deliveryResult, error) {
	storageBackend, storageKey, imageID := req.storageBackend, req.storageKey, req.imageID
	params, hasTransformations, m := req.params, req.hasTransformations, req.metrics

	// Re-check: another goroutine may have filled the cache while this
	// one waited for its turn at the key.
	if cachedData, found := h.imageProcessor.GetFromCache(cacheKey); found {
		return &deliveryResult{
			data: cachedData,
			meta: h.buildCachedMetadata(imageID, params, len(cachedData)),
		}, nil
	}

	retrieveCtx, retrieveCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer retrieveCancel()
	storageStart := time.Now()
	reader, metadata, err := storageBackend.Retrieve(retrieveCtx, storageKey)
	storageDuration := time.Since(storageStart).Seconds()
	if err != nil {
		m.StorageOperationsTotal.WithLabelValues("retrieve", h.defaultStorageType(), "error").Inc()
		if storage.IsNotFound(err) {
			return nil, &fetchError{http.StatusNotFound, "IMAGE_NOT_FOUND", "Image not found"}
		}
		logger.Error().Err(err).Msg("Failed to retrieve image")
		return nil, &fetchError{http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to retrieve image"}
	}
	m.StorageOperationsTotal.WithLabelValues("retrieve", h.defaultStorageType(), "success").Inc()
	m.StorageOperationDuration.WithLabelValues("retrieve", h.defaultStorageType()).Observe(storageDuration)
	defer func() { _ = reader.Close() }()

	// Non-image, or metadata reveals no processing is actually
	// needed (mirrors the top-level needsProcessing re-check):
	// buffer and serve as-is. Buffering (unlike the raw-delivery
	// path below) is required here because this result may be
	// shared with sibling callers waiting on sf.Do.
	isImage := utils.IsImageContentType(metadata.ContentType)
	needsProcessing := hasTransformations ||
		(params.Format != "" && params.Format != metadata.Format) ||
		(metadata.Format == "" || metadata.ContentType == "" || metadata.ContentType == "application/octet-stream")
	if !isImage || !needsProcessing {
		data, err := io.ReadAll(reader)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to read raw image body")
			return nil, &fetchError{http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to read image"}
		}
		return &deliveryResult{data: data, meta: metadata}, nil
	}

	processCtx, processCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer processCancel()
	// Duration (split into semaphore_wait + transform) is recorded
	// inside Process() itself now — see vips_processor.go — so only
	// the pass/fail counter, which needs the format labels, stays here.
	processedImage, err := h.imageProcessor.Process(processCtx, reader, params, cacheKey)
	if err != nil {
		m.ImageProcessingTotal.WithLabelValues(metadata.Format, params.Format, "error").Inc()
		logger.Error().Err(err).Msg("Failed to process image")
		return nil, &fetchError{http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image"}
	}
	m.ImageProcessingTotal.WithLabelValues(metadata.Format, params.Format, "success").Inc()
	defer func() { _ = processedImage.Data.Close() }()

	data, err := io.ReadAll(processedImage.Data)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to read processed image")
		return nil, &fetchError{http.StatusInternalServerError, "PROCESSING_FAILED", "Failed to read processed image"}
	}

	return &deliveryResult{data: data, meta: h.buildProcessedMetadata(processedImage, params)}, nil
}
