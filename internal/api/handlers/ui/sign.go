package ui

import (
	"errors"
	"strings"
	"time"

	"github.com/birdple/falco/internal/security"
)

// errSigningDisabled means falco has no HMAC key, so nothing can be signed.
var errSigningDisabled = errors.New("URL signing is disabled because HMAC_KEY is not configured")

// signingEnabled reports whether signed URLs can be produced at all.
func (h *Handler) signingEnabled() bool {
	return h.cfg.Security.HMACKey != "" && h.cfg.Security.HMACKeySalt != ""
}

// signPath returns a signed delivery URL for path, valid for ttl.
//
// This is the only place the panel touches the HMAC key, and it runs on the
// server: the browser receives finished URLs and never the key itself. The old
// panel had no signing at all, so with HMAC_REQUIRED=true — the setting the
// stack actually runs — every image in the grid was a 403.
//
// The returned string is used verbatim. Signing rewrites the query (the expiry
// is merged in and the parameters are re-encoded in sorted order), so
// rebuilding the URL from its parts breaks the signature.
func (h *Handler) signPath(path string, ttl time.Duration) (string, error) {
	if !h.signingEnabled() {
		return "", errSigningDisabled
	}

	expiry := time.Now().Add(ttl).Unix()
	sig, pathWithExp, err := security.SignURLWithExpiry(
		path,
		expiry,
		h.cfg.Security.HMACKey,
		h.cfg.Security.HMACKeySalt,
		h.cfg.Security.HMACSignatureSize,
	)
	if err != nil {
		return "", err
	}

	sep := "?"
	if strings.Contains(pathWithExp, "?") {
		sep = "&"
	}
	return pathWithExp + sep + "sig=" + sig, nil
}

// signedExpiry reports when a URL signed now with ttl stops working, so the UI
// can show it rather than leave the reader guessing.
func signedExpiry(ttl time.Duration) time.Time {
	return time.Now().Add(ttl)
}
