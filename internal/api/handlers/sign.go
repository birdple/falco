package handlers

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/jsonx"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/security"
)

// maxSignedURLLifetime caps how far in the future a signed URL may expire.
const maxSignedURLLifetime = 366 * 24 * time.Hour

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
	// The decode error is reported separately from validation: json/v2 is
	// case-sensitive and rejects unknown fields, and collapsing the two would
	// answer "path is required" to a caller that did send path.
	if err := jsonv2.UnmarshalRead(r.Body, &req, jsonx.Strict); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON payload")
		return
	}
	if req.Path == "" {
		h.sendError(w, http.StatusBadRequest, "INVALID_REQUEST", "path is required")
		return
	}

	// The path is authorised exactly the way delivery will resolve it, through
	// the same function: checking only ?b= here once let a key scoped to bucket
	// A sign "?storage=B" and read bucket B with the result. The query is split
	// at the first "?" because that is what Canonicalize signs; a "#" is
	// refused because url.Parse and Canonicalize disagree about it.
	if strings.ContainsRune(req.Path, '#') {
		h.sendError(w, http.StatusBadRequest, "INVALID_PATH", "Path must not contain a fragment")
		return
	}
	_, rawQuery, _ := strings.Cut(req.Path, "?")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_PATH", "Malformed path")
		return
	}
	scope := apimw.GetScope(r.Context())
	if _, _, err := h.authorizeBucket(scope, query.Get("storage"), utils.QueryParam(query, "b", "bucket")); err != nil {
		keyName := ""
		if scope != nil {
			keyName = scope.KeyName
		}
		logger.Warn().Err(err).Str("key_name", keyName).Msg("Scope denied signing path")
		h.sendError(w, http.StatusForbidden, "ACCESS_DENIED", "Key not authorized for this bucket")
		return
	}

	// Resolve expiry. If caller supplied neither, no expiry is appended.
	var expUnix int64
	if req.ExpiresAt > 0 {
		expUnix = req.ExpiresAt
	} else if req.ExpiresIn > 0 {
		expUnix = time.Now().Unix() + req.ExpiresIn
	}
	// An expiry centuries away is no expiry at all, and would make
	// HMAC_REQUIRE_EXPIRY=true meaningless.
	if expUnix > time.Now().Add(maxSignedURLLifetime).Unix() {
		h.sendError(w, http.StatusBadRequest, "INVALID_EXPIRY",
			fmt.Sprintf("Expiry may be at most %d days away", int(maxSignedURLLifetime.Hours()/24)))
		return
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
