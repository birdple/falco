package handlers

import (
	jsonv2 "encoding/json/v2"
	"net/http"
	"strings"
	"time"

	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/jsonx"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/security"
)

// SignURLRequest represents a request to sign a URL.
//
// ExpiresIn (seconds) and ExpiresAt (Unix timestamp seconds) are mutually
// exclusive. If both are zero, the signed URL carries no expiry — which is
// only accepted at delivery time when HMAC_REQUIRE_EXPIRY=false.
type SignURLRequest struct {
	Path      string `json:"path"`
	ExpiresIn int64  `json:"expires_in,omitzero"`
	ExpiresAt int64  `json:"expires_at,omitzero"`
}

// SignURLResponse represents a signed URL response
type SignURLResponse struct {
	SignedURL string `json:"signed_url"`
	Signature string `json:"signature"`
	ExpiresAt int64  `json:"expires_at,omitzero"`
}

// HandleSignURL generates a signed URL for the given path.
//
// Scope enforcement: the caller's APIScope (derived from the API key used for
// this /sign call) MUST allow the bucket referenced by the path being signed.
// Without this check, a scoped key restricted to bucket A could sign a URL
// for bucket B and gain unauthorized access at delivery time.
func (h *Handler) HandleSignURL(w http.ResponseWriter, r *http.Request) {
	if h.config.Security.HMACKey == "" {
		h.sendError(w, http.StatusNotImplemented, "SIGNING_DISABLED", "HMAC signing is not configured")
		return
	}

	var req SignURLRequest
	// The decode error is reported separately from validation: collapsing them
	// made an unknown field, or a capitalisation that does not match the tag,
	// come back as "path is required" even when the caller did send path.
	// json/v2 is case-sensitive, so that case is now reachable.
	if err := jsonv2.UnmarshalRead(r.Body, &req, jsonx.Strict); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
		return
	}
	if req.Path == "" {
		h.sendError(w, http.StatusBadRequest, "INVALID_REQUEST", "path is required")
		return
	}

	// Enforce scope on the requested path's bucket. Upload/list/delete all
	// route through getStorageBackendScoped — /sign was the missing link.
	scope := apimw.GetScope(r.Context())
	bucket, _, err := utils.ExtractBucketAndDirFromSignPath(req.Path)
	if err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_PATH", "Malformed path")
		return
	}
	if scope != nil && !scope.IsAdmin && bucket != "" && !scope.CanAccessBucket(bucket) {
		logger.Warn().
			Str("key_name", scope.KeyName).
			Str("bucket", bucket).
			Msg("Scope denied signing path for bucket")
		h.sendError(w, http.StatusForbidden, "ACCESS_DENIED", "Key not authorized for this bucket")
		return
	}
	// If no bucket in query the path is for the default bucket. When scoped
	// keys are configured and the key has any bucket restrictions, a blank
	// bucket would resolve to the server's default — which the scoped key
	// may not be allowed to access. Fail closed rather than silently signing
	// a URL the caller can't use.
	if scope != nil && !scope.IsAdmin && bucket == "" && len(scope.Buckets) > 0 {
		defaultBucket := h.config.Storage.Default
		if defaultBucket == "" || !scope.CanAccessBucket(defaultBucket) {
			logger.Warn().
				Str("key_name", scope.KeyName).
				Msg("Scope denied signing path for default bucket")
			h.sendError(w, http.StatusForbidden, "ACCESS_DENIED", "Key not authorized for default bucket")
			return
		}
	}

	// Resolve expiry. If caller supplied neither, no expiry is appended.
	var expUnix int64
	if req.ExpiresAt > 0 {
		expUnix = req.ExpiresAt
	} else if req.ExpiresIn > 0 {
		expUnix = time.Now().Unix() + req.ExpiresIn
	}

	pathToSign := req.Path
	if expUnix > 0 {
		var sig, pathWithExp string
		sig, pathWithExp, err = security.SignURLWithExpiry(
			req.Path,
			expUnix,
			h.config.Security.HMACKey,
			h.config.Security.HMACKeySalt,
			h.config.Security.HMACSignatureSize,
		)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to generate signature")
			h.sendError(w, http.StatusInternalServerError, "SIGNING_ERROR", "Failed to generate signature")
			return
		}
		writeSignedResponse(w, pathWithExp, sig, expUnix)
		return
	}

	sig, err := security.SignURL(
		pathToSign,
		h.config.Security.HMACKey,
		h.config.Security.HMACKeySalt,
		h.config.Security.HMACSignatureSize,
	)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to generate signature")
		h.sendError(w, http.StatusInternalServerError, "SIGNING_ERROR", "Failed to generate signature")
		return
	}

	writeSignedResponse(w, pathToSign, sig, 0)
}

func writeSignedResponse(w http.ResponseWriter, path, sig string, expUnix int64) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	fullURL := path + sep + "sig=" + sig

	resp := SignURLResponse{
		SignedURL: fullURL,
		Signature: sig,
	}
	if expUnix > 0 {
		resp.ExpiresAt = expUnix
	}
	writeJSON(w, http.StatusOK, resp)
}
