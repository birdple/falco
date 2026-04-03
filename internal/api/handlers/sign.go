package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/birdple/falco/internal/security"
)

// SignURLRequest represents a request to sign a URL
type SignURLRequest struct {
	Path string `json:"path"` // e.g. "/api/v1/images/abc123?w=800&h=600"
}

// SignURLResponse represents a signed URL response
type SignURLResponse struct {
	SignedURL  string `json:"signed_url"`
	Signature string `json:"signature"`
}

// HandleSignURL generates a signed URL for the given path.
// Requires API key. Used by backend services to generate URLs for clients.
func (h *Handler) HandleSignURL(w http.ResponseWriter, r *http.Request) {
	if h.config.Security.HMACKey == "" {
		h.sendError(w, http.StatusNotImplemented, "SIGNING_DISABLED", "HMAC signing is not configured")
		return
	}

	var req SignURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		h.sendError(w, http.StatusBadRequest, "INVALID_REQUEST", "path is required")
		return
	}

	sig, err := security.SignURL(
		req.Path,
		h.config.Security.HMACKey,
		h.config.Security.HMACKeySalt,
		h.config.Security.HMACSignatureSize,
	)
	if err != nil {
		h.logger.WithError(err).Error("Failed to generate signature")
		h.sendError(w, http.StatusInternalServerError, "SIGNING_ERROR", "Failed to generate signature")
		return
	}

	// Build the full signed URL by appending sig param
	sep := "?"
	if strings.Contains(req.Path, "?") {
		sep = "&"
	}
	fullURL := req.Path + sep + "sig=" + sig

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(SignURLResponse{
		SignedURL:  fullURL,
		Signature: sig,
	})
}
