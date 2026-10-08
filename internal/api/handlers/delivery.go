package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/singleflight"

	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/pkg/metrics"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/security"
	"github.com/birdple/falco/internal/storage"
)

// hmacRequireExpiry reads HMAC_REQUIRE_EXPIRY from the environment. It has no
// default: when missing or unparseable it returns an error, and the caller
// rejects the request rather than picking a value.
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
	namespace := h.backendNamespace(storageName, bucket)

	storageBackend, err := h.getStorageBackendScoped(r, storageName, bucket)
	if err != nil {
		h.sendStorageBackendError(w, err)
		return
	}

	params, paramErr := h.parseDeliveryParams(query, extFormat)
	if paramErr != nil {
		h.sendError(w, http.StatusBadRequest, paramErr.code, paramErr.message)
		return
	}

	req := deliveryRequest{
		storageBackend: storageBackend,
		namespace:      namespace,
		storageKey:     storageKey,
		imageID:        imageID,
		params:         params,
		metrics:        metrics.Default(),
	}

	// A transformation is CPU worth sharing, so it goes through the cache and
	// the singleflight. Everything else — the original as stored, or the
	// original in another encoding — starts by streaming from storage.
	if wantsTransformation(params) {
		h.deliverProcessed(w, r, req)
		return
	}
	h.deliverRaw(w, r, req)
}

// deliveryRequest bundles what both delivery paths need, so neither ends up
// with a nine-parameter signature.
type deliveryRequest struct {
	storageBackend storage.StorageBackend
	// namespace is the backend the request resolved to; with storageKey it
	// names the original for every cache and singleflight key.
	namespace  string
	storageKey string
	imageID    string
	params     *processor.ProcessingParams
	metrics    *metrics.Metrics
}

// deliverProcessed serves a request that transforms the image.
//
// The cache is consulted first, before any storage round-trip. On a miss, the
// retrieve-and-process work is deduplicated with singleflight so that N
// concurrent requests for the same resize pay for one Jay fetch, one decode and
// one encode between them, instead of N of each.
//
// The result is buffered rather than streamed precisely because it is shared:
// siblings waiting on the same key all get the same bytes.
func (h *Handler) deliverProcessed(w http.ResponseWriter, r *http.Request, req deliveryRequest) {
	imageID, params := req.imageID, req.params

	// Resolve the output format now so the cache key is deterministic.
	params.Format = h.resolveOutputFormat(params.Format)

	cacheKey := h.imageProcessor.GenerateCacheKey(cacheObjectKey(req.namespace, req.storageKey), params)
	if cachedData, found := h.imageProcessor.GetFromCache(cacheKey); found {
		h.serveImage(w, r, bytes.NewReader(cachedData), h.buildCachedMetadata(imageID, params, len(cachedData)))
		return
	}

	// DoChan rather than Do: the shared work runs on its own context, but a
	// caller that hangs up stops waiting for it instead of holding a
	// goroutine (and a connection slot) until the slowest sibling finishes.
	ch := h.sf.DoChan(cacheKey, func() (any, error) {
		return h.fetchAndProcess(req, cacheKey)
	})
	var res singleflight.Result
	select {
	case res = <-ch:
	case <-r.Context().Done():
		return
	}

	if res.Err != nil {
		if fe, ok := errors.AsType[*fetchError](res.Err); ok {
			h.sendError(w, fe.status, fe.code, fe.message)
		} else {
			logger.Error().Err(res.Err).Msg("Unexpected delivery singleflight error")
			h.sendError(w, http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to deliver image")
		}
		return
	}
	result := res.Val.(*deliveryResult)
	// The result is shared with every request that collapsed onto this key,
	// but the caching directives are not part of the key: each caller gets its
	// own maxage/smaxage rather than the leader's.
	meta := *result.meta
	meta.MaxAge, meta.SMaxAge = params.MaxAge, params.SMaxAge
	h.serveImage(w, r, bytes.NewReader(result.data), &meta)
}

// deliverRaw serves the stored object without transforming it: as stored, or
// re-encoded when the request names another format (?f= or a path extension
// such as /images/abc.webp).
//
// The common case — the stored format already matches — streams straight
// through: no deduplication, no buffering, memory flat however large the file.
// A format conversion is CPU work, so it is answered from cache when it can be
// (the key is computable from the query alone) and cached when it is not.
func (h *Handler) deliverRaw(w http.ResponseWriter, r *http.Request, req deliveryRequest) {
	ctx := r.Context()
	params, m := req.params, req.metrics
	object := cacheObjectKey(req.namespace, req.storageKey)

	if params.Format != "" {
		if cachedData, found := h.imageProcessor.GetFromCache(h.imageProcessor.GenerateCacheKey(object, params)); found {
			h.serveImage(w, r, bytes.NewReader(cachedData), h.buildCachedMetadata(req.imageID, params, len(cachedData)))
			return
		}
	}

	reader, metadata, fe := h.retrieveOriginal(ctx, req)
	if fe != nil {
		h.sendError(w, fe.status, fe.code, fe.message)
		return
	}
	defer func() { _ = reader.Close() }()

	if !utils.IsImageContentType(metadata.ContentType) || !needsReencode(params, metadata) {
		h.serveImage(w, r, reader, metadata)
		return
	}

	params.Format = h.resolveOutputFormat(params.Format)
	cacheKey := h.imageProcessor.GenerateCacheKey(object, params)

	// Duration (semaphore_wait + transform) is recorded by Process(); only the
	// pass/fail counter, which needs the format labels, stays here.
	processedImage, err := h.imageProcessor.Process(ctx, reader, params, cacheKey)
	if err != nil {
		m.ImageProcessingTotal.WithLabelValues(metadata.Format, params.Format, "error").Inc()
		fe := processFailure(err)
		h.sendError(w, fe.status, fe.code, fe.message)
		return
	}
	m.ImageProcessingTotal.WithLabelValues(metadata.Format, params.Format, "success").Inc()
	defer func() { _ = processedImage.Data.Close() }()

	h.serveImage(w, r, processedImage.Data, h.buildProcessedMetadata(req.imageID, processedImage, params))
}

// needsReencode reports whether an untransformed image still has to go through
// the encoder: another format was asked for, or the stored metadata is too
// incomplete to serve the bytes with an honest Content-Type.
func needsReencode(params *processor.ProcessingParams, metadata *storage.ImageMetadata) bool {
	return (params.Format != "" && params.Format != metadata.Format) ||
		metadata.Format == "" || metadata.ContentType == "" ||
		metadata.ContentType == "application/octet-stream"
}

// retrieveOriginal reads the original from storage, recording the storage
// metrics, and maps a failure onto the response it deserves.
func (h *Handler) retrieveOriginal(ctx context.Context, req deliveryRequest) (io.ReadCloser, *storage.ImageMetadata, *fetchError) {
	m := req.metrics
	storageStart := time.Now()
	reader, metadata, err := req.storageBackend.Retrieve(ctx, req.storageKey)
	if err != nil {
		m.StorageOperationsTotal.WithLabelValues("retrieve", h.defaultStorageType(), "error").Inc()
		return nil, nil, retrieveFailure(err)
	}
	m.StorageOperationsTotal.WithLabelValues("retrieve", h.defaultStorageType(), "success").Inc()
	m.StorageOperationDuration.WithLabelValues("retrieve", h.defaultStorageType()).Observe(time.Since(storageStart).Seconds())
	return reader, metadata, nil
}

// retrieveFailure maps a storage error onto the response it deserves: a
// missing original is a 404, a backend the breaker has given up on is a 503 —
// a client should retry later, not report a server bug — and anything else is
// a 500.
func retrieveFailure(err error) *fetchError {
	switch {
	case storage.IsNotFound(err):
		return &fetchError{http.StatusNotFound, "IMAGE_NOT_FOUND", "Image not found"}
	case storage.IsUnavailable(err):
		logger.Warn().Err(err).Msg("Storage unavailable")
		return &fetchError{http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "Storage is temporarily unavailable"}
	default:
		logger.Error().Err(err).Msg("Failed to retrieve image")
		return &fetchError{http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to retrieve image"}
	}
}

// processFailure maps a Process error onto a response. Running out of time —
// usually waiting for a processing slot under load — is a 503 to retry, not a
// verdict on the image; an oversized or unsupported input is the caller's.
func processFailure(err error) *fetchError {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		logger.Warn().Err(err).Msg("Image processing timed out")
		return &fetchError{http.StatusServiceUnavailable, "PROCESSING_BUSY", "Image processing is busy, retry later"}
	case errors.Is(err, processor.ErrImageTooLarge):
		return &fetchError{http.StatusUnprocessableEntity, "IMAGE_TOO_LARGE", "Image exceeds the pixel limit"}
	case errors.Is(err, processor.ErrUnsupportedInput):
		return &fetchError{http.StatusUnsupportedMediaType, "UNSUPPORTED_IMAGE", "Image format is not supported"}
	default:
		logger.Error().Err(err).Msg("Failed to process image")
		return &fetchError{http.StatusUnprocessableEntity, "PROCESSING_FAILED", "Failed to process image"}
	}
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

	format, ok := h.parseFormat(utils.QueryParam(query, "f", "format"))
	switch {
	case !ok:
		return nil, &paramError{"INVALID_FORMAT", "Unsupported format"}
	case format != "":
		params.Format = format
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

	// Manual crop is all-or-nothing: an origin without a size is a request the
	// caller did not mean, and guessing a size for it would serve a different
	// image than the one asked for.
	cropX, cropY, cropW, cropH, cropErr := parseCrop(query)
	if cropErr != nil {
		return nil, cropErr
	}
	params.CropX, params.CropY, params.CropW, params.CropH = cropX, cropY, cropW, cropH

	if raw := query.Get("rotate"); raw != "" {
		angle, err := strconv.ParseFloat(raw, 64)
		// NaN passes every range comparison, so it is rejected explicitly.
		if err != nil || !isFinite(angle) || angle < -maxRotateDegrees || angle > maxRotateDegrees {
			return nil, &paramError{"INVALID_ROTATE", "rotate must be between -360 and 360 degrees"}
		}
		params.Rotate = angle
	}

	if raw := query.Get("flip"); raw != "" {
		if raw != FlipHorizontal && raw != FlipVertical {
			return nil, &paramError{"INVALID_FLIP", "flip must be horizontal or vertical"}
		}
		params.Flip = raw
	}

	// From here down every parameter is best-effort: a malformed value leaves
	// the default in place instead of failing the request.
	params.MaxAge = nonNegativeInt(query.Get("maxage"), params.MaxAge)
	params.SMaxAge = nonNegativeInt(query.Get("smaxage"), params.SMaxAge)

	if raw := query.Get("gravity"); validGravities[raw] {
		params.Gravity = raw
	}

	// Colour and effects. Out of range counts as malformed, so it falls back to
	// "not requested" rather than being clamped: a silently clamped value is
	// indistinguishable from one that worked.
	params.Brightness = floatInRange(query.Get("brightness"), -100, 100, params.Brightness)
	params.Contrast = floatInRange(query.Get("contrast"), -100, 100, params.Contrast)
	params.Gamma = floatInRange(query.Get("gamma"), 0, 3, params.Gamma)
	params.Saturation = floatInRange(query.Get("saturation"), -100, 500, params.Saturation)
	params.Hue = int(floatInRange(query.Get("hue"), -180, 180, float64(params.Hue)))
	params.Blur = floatInRange(query.Get("blur"), 0, 100, params.Blur)
	params.Sharpen = floatInRange(query.Get("sharpen"), 0, 100, params.Sharpen)

	// The watermark source is only recorded here — resolving it reaches storage
	// or the network, which this function deliberately does not do. What it
	// does decide is that naming both is a contradiction rather than a
	// precedence rule nobody would remember.
	wmID, wmURL := query.Get("wm"), query.Get("wm_url")
	switch {
	case wmID != "" && wmURL != "":
		return nil, &paramError{"INVALID_WATERMARK", "wm and wm_url are mutually exclusive"}
	case wmID != "":
		// The same shape the image id itself takes: an optional directory plus
		// a final segment, validated the same way, so a watermark cannot be the
		// one path that escapes its bucket.
		wmDir, wmFinal := utils.SplitDirectoryAndID(wmID)
		wmDir = utils.NormalizeDirectoryPath(wmDir)
		if err := utils.ValidateDirectoryPath(wmDir); err != nil {
			return nil, &paramError{"INVALID_WATERMARK", "wm has an invalid directory"}
		}
		if !utils.IsValidImageID(wmFinal) {
			return nil, &paramError{"INVALID_WATERMARK", "wm is not a valid image id"}
		}
		params.WatermarkSource = watermarkStoredPrefix + utils.BuildStorageKey(wmDir, wmFinal)
	case wmURL != "":
		params.WatermarkSource = wmURL
	}

	params.WatermarkOpacity = floatInRange(query.Get("wm_opacity"), 0, 1, params.WatermarkOpacity)

	if raw := query.Get("wm_position"); processor.IsValidWatermarkPosition(raw) {
		params.WatermarkPosition = raw
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

	// Padding is capped per side at the configured maximum dimension: it is
	// applied after every resize limit, so an uncapped pad_top=30000 built a
	// canvas of gigabytes. The processor also checks the final pixel count.
	maxPadX, maxPadY := h.config.Processing.MaxDimensions.Width, h.config.Processing.MaxDimensions.Height
	params.PaddingTop = boundedInt(query.Get("pad_top"), maxPadY, params.PaddingTop)
	params.PaddingRight = boundedInt(query.Get("pad_right"), maxPadX, params.PaddingRight)
	params.PaddingBottom = boundedInt(query.Get("pad_bottom"), maxPadY, params.PaddingBottom)
	params.PaddingLeft = boundedInt(query.Get("pad_left"), maxPadX, params.PaddingLeft)
	// Normalised here so equivalent spellings share a cache entry; anything
	// that is not six hex digits falls back to the default white.
	params.PaddingColor, _ = processor.NormalizeHexColor(query.Get("pad_color"))

	// Both default to on, so the query string opts *out* rather than in.
	params.SkipAutoOrient = query.Get("orient") == "0"
	params.KeepMetadata = query.Get("meta") == "1"

	return params, nil
}

// parseFormat validates ?f=. It accepts the same spellings as a path
// extension — "jpg" included — and returns the canonical format name. An empty
// value is valid and returns "".
func (h *Handler) parseFormat(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if mapped, ok := AllowedImageExtensions[strings.ToLower(raw)]; ok {
		raw = mapped
	}
	if !h.imageProcessor.ValidateFormat(raw) {
		return "", false
	}
	return raw, true
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

// parseCrop reads the four manual-crop parameters as one unit.
//
// A crop is either fully specified or absent. Accepting an origin with no size
// would silently ignore half of what the caller wrote, and accepting a size
// with no origin would crop from a corner they never named.
func parseCrop(query url.Values) (x, y, w, h int, err *paramError) {
	rawX, rawY := query.Get("crop_x"), query.Get("crop_y")
	rawW, rawH := query.Get("crop_w"), query.Get("crop_h")

	if rawX == "" && rawY == "" && rawW == "" && rawH == "" {
		return 0, 0, 0, 0, nil
	}
	if rawW == "" || rawH == "" {
		return 0, 0, 0, 0, &paramError{"INVALID_CROP", "crop_w and crop_h are both required to crop"}
	}

	x, err = cropCoordinate(rawX, "crop_x")
	if err != nil {
		return 0, 0, 0, 0, err
	}
	y, err = cropCoordinate(rawY, "crop_y")
	if err != nil {
		return 0, 0, 0, 0, err
	}

	w, err = cropExtent(rawW, "crop_w")
	if err != nil {
		return 0, 0, 0, 0, err
	}
	h, err = cropExtent(rawH, "crop_h")
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return x, y, w, h, nil
}

// cropCoordinate parses an optional non-negative crop origin.
func cropCoordinate(raw, name string) (int, *paramError) {
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, &paramError{"INVALID_CROP", name + " must be zero or a positive integer"}
	}
	return v, nil
}

// cropExtent parses a crop width or height, which unlike an origin cannot be
// zero: a zero-sized crop is an empty image, not a request.
func cropExtent(raw, name string) (int, *paramError) {
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 || v > maxCropExtent {
		return 0, &paramError{"INVALID_CROP", name + " must be a positive integer"}
	}
	return v, nil
}

// floatInRange parses an optional float and returns fallback when it is absent,
// unparseable, or outside the range the transformation accepts.
//
// Out of range is treated as malformed rather than clamped on purpose: a
// clamped value produces an image that is not the one asked for and gives the
// caller no way to notice, while the fallback at least matches what an omitted
// parameter does.
func floatInRange(raw string, low, high, fallback float64) float64 {
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || !isFinite(v) || v < low || v > high {
		return fallback
	}
	return v
}

// isFinite reports whether v is neither NaN nor ±Inf. strconv.ParseFloat
// accepts "NaN" and "Inf", and NaN slips through every < and > check.
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// boundedInt parses an optional integer in [0, upper], returning fallback when
// it is absent, malformed or out of range. A non-positive upper means no cap.
func boundedInt(raw string, upper, fallback int) int {
	v := nonNegativeInt(raw, -1)
	if v < 0 || (upper > 0 && v > upper) {
		return fallback
	}
	return v
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
		// The directory part of the path is held to the same rules as ?d=:
		// "/images/../x/abc" must not reach storage as the key "../x/abc".
		if utils.ValidateDirectoryPath(utils.NormalizeDirectoryPath(before)) != nil || strings.Contains(before, "//") {
			return "", "", &paramError{"INVALID_ID", "Invalid image id"}
		}
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

// authorizeDelivery enforces access control on the delivery and proxy routes.
//
// With HMAC_REQUIRED=true they are public-by-signature: the URL signature
// authorizes that exact path plus query, so no API key is needed — a browser
// cannot attach one to an <img> URL anyway.
//
// With HMAC_REQUIRED=false the route is open. There is no "API key instead"
// regime: validateSecurity refuses API_KEY_REQUIRED without HMAC_REQUIRED (and
// the converse), so the only deployment that reaches this branch is one with
// auth switched off altogether — a local or development setup.
//
// Returns false once it has already written the error response.
func (h *Handler) authorizeDelivery(w http.ResponseWriter, r *http.Request, query url.Values) (bool, *http.Request) {
	if h.config.Security.HMACRequired {
		return h.verifyDeliverySignature(w, r, query), r
	}
	return true, r
}

// verifyDeliverySignature checks the HMAC signature covering path and query.
//
// A missing or unparseable HMAC_REQUIRE_EXPIRY is a 500, not a default: the
// silent degradation would be accepting signed URLs that never expire.
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
		p.Rotate != 0 || p.Flip != "" || p.WatermarkSource != "" ||
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
//
// ID and CreatedAt are the same ones buildCachedMetadata uses, so the ETag and
// Last-Modified of a fresh render match those of the cache hit that follows it;
// otherwise no conditional request could ever match across the two.
func (h *Handler) buildProcessedMetadata(imageID string, processed *processor.ProcessedImage, params *processor.ProcessingParams) *storage.ImageMetadata {
	meta := &storage.ImageMetadata{
		ID:           imageID,
		OriginalName: processed.Metadata.OriginalName,
		Format:       processed.Metadata.Format,
		Size:         processed.Metadata.Size,
		Width:        processed.Metadata.Width,
		Height:       processed.Metadata.Height,
		ContentType:  processed.Metadata.ContentType,
		CreatedAt:    time.Unix(0, 0),
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
	imageID, params, m := req.imageID, req.params, req.metrics

	// Re-check: another goroutine may have filled the cache while this
	// one waited for its turn at the key.
	if cachedData, found := h.imageProcessor.GetFromCache(cacheKey); found {
		return &deliveryResult{
			data: cachedData,
			meta: h.buildCachedMetadata(imageID, params, len(cachedData)),
		}, nil
	}

	retrieveCtx, retrieveCancel := context.WithTimeout(context.Background(), deliveryRetrieveTimeout)
	defer retrieveCancel()
	reader, metadata, fe := h.retrieveOriginal(retrieveCtx, req)
	if fe != nil {
		return nil, fe
	}
	defer func() { _ = reader.Close() }()

	// A non-image cannot be transformed: buffer and serve it as-is. Buffering
	// (unlike deliverRaw) is required because the result may be shared with
	// sibling callers waiting on the singleflight.
	if !utils.IsImageContentType(metadata.ContentType) {
		data, err := io.ReadAll(reader)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to read raw image body")
			return nil, &fetchError{http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to read image"}
		}
		return &deliveryResult{data: data, meta: metadata}, nil
	}

	processCtx, processCancel := context.WithTimeout(context.Background(), deliveryProcessTimeout)
	defer processCancel()

	// The overlay is loaded here and not at parse time: this is the cache-miss
	// path, so a request answered from cache never pays for it. A failure is
	// returned rather than swallowed — an image served without the watermark it
	// was asked for looks exactly like one that worked. It is resolved before
	// the original is read into the processor, so a slow overlay fetch does
	// not hold the storage stream open any longer than needed.
	if wmErr := h.resolveWatermark(processCtx, req.storageBackend, req.namespace, params); wmErr != nil {
		return nil, wmErr
	}

	// Duration (semaphore_wait + transform) is recorded by Process(); only the
	// pass/fail counter, which needs the format labels, stays here.
	processedImage, err := h.imageProcessor.Process(processCtx, reader, params, cacheKey)
	if err != nil {
		m.ImageProcessingTotal.WithLabelValues(metadata.Format, params.Format, "error").Inc()
		return nil, processFailure(err)
	}
	m.ImageProcessingTotal.WithLabelValues(metadata.Format, params.Format, "success").Inc()
	defer func() { _ = processedImage.Data.Close() }()

	data, err := io.ReadAll(processedImage.Data)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to read processed image")
		return nil, &fetchError{http.StatusInternalServerError, "PROCESSING_FAILED", "Failed to read processed image"}
	}

	return &deliveryResult{data: data, meta: h.buildProcessedMetadata(imageID, processedImage, params)}, nil
}
