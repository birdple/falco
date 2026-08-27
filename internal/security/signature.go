// Package security provides HMAC-based URL signing for image delivery endpoints.
// Adapted from imgproxy (https://github.com/imgproxy/imgproxy)
// Copyright (c) 2017 Sergey "DarthSim" Aleksandrovich — MIT License
package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors returned by URL signature verification. Callers branch on
// them with errors.Is; the delivery handler maps every one of them to 403 so a
// client cannot tell a malformed signature from an expired one.
var (
	ErrNoSignatureConfig = errors.New("HMAC key and salt are not configured")
	ErrInvalidEncoding   = errors.New("invalid signature encoding")
	ErrSignatureMismatch = errors.New("signature mismatch")
	ErrMissingSignature  = errors.New("missing signature")
	ErrMissingExpiry     = errors.New("missing expiry (exp) parameter")
	ErrExpiredSignature  = errors.New("signature expired")
	ErrInvalidExpiry     = errors.New("invalid expiry (exp) parameter")
)

// ExpiryQueryParam is the query parameter name used for signed URL expiry.
// When present, its value is a Unix timestamp (seconds) after which the
// signature is considered invalid. The value is included in the HMAC input
// through Canonicalize (which sorts query params alphabetically and preserves
// "exp"), so tampering with it invalidates the signature.
const ExpiryQueryParam = "exp"

// Canonicalize returns a deterministic "path[?sortedQuery]" form for HMAC
// signing. Both the signer and the verifier MUST call this so that a URL like
// "/images/foo?w=400&format=webp" signs identically regardless of query-param
// order. The "sig" parameter, if present, is always removed before sorting.
//
// The "exp" parameter (when present) is preserved and included in the signed
// payload: this binds the signature to a specific expiry and prevents clients
// from forging or extending the validity window of a leaked URL.
//
// The canonical form is the URL path followed by url.Values.Encode(), which
// sorts keys alphabetically. If there are no query params after removing
// "sig", only the path is returned (no trailing "?").
func Canonicalize(rawPath string) string {
	path := rawPath
	query := ""
	if before, after, ok := strings.Cut(rawPath, "?"); ok {
		path = before
		query = after
	}
	if query == "" {
		return path
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		// Fall back to the raw string on parse failure. The verifier will
		// reject it because both sides will produce the same deterministic
		// failure mode.
		return rawPath
	}
	values.Del("sig")
	if len(values) == 0 {
		return path
	}
	return path + "?" + values.Encode()
}

// CanonicalizeRequest is a helper for http.Request-style callers. It builds
// the canonical string from a path and a parsed url.Values (typically the
// request's already-parsed query).
func CanonicalizeRequest(path string, values url.Values) string {
	if len(values) == 0 {
		return path
	}
	// Our own copy, so the caller can still read "sig" from theirs. Clone is a
	// deep copy: the hand-rolled version this replaced shared the value slices
	// with the original map.
	clone := values.Clone()
	clone.Del("sig")
	if len(clone) == 0 {
		return path
	}
	return path + "?" + clone.Encode()
}

// SignURL generates an HMAC-SHA256 signature for the given path.
//
// path may be any path or path+query string. It is canonicalized (sig removed,
// query params sorted alphabetically) before signing so that the signer and
// verifier always agree regardless of the caller's param order.
//
// If the path contains an "exp" query parameter, it is preserved and signed
// along with the rest of the payload (see SignURLWithExpiry for a helper).
func SignURL(path string, keyHex, saltHex string, signatureSize int) (string, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", errors.New("invalid HMAC key encoding (expected hex)")
	}
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return "", errors.New("invalid HMAC salt encoding (expected hex)")
	}
	canonical := Canonicalize(path)
	sig := computeSignature(canonical, key, salt, signatureSize)
	return base64.RawURLEncoding.EncodeToString(sig), nil
}

// SignURLWithExpiry generates a signed URL with an embedded "exp" query
// parameter (Unix timestamp in seconds). The resulting signature covers
// the expiry, so a client cannot extend validity without invalidating the
// signature.
//
// Returns the HMAC signature AND the path+query with "exp" appended.
// Callers typically combine them as "<pathWithExp>&sig=<sig>" (or "?sig=..."
// if pathWithExp has no query yet).
func SignURLWithExpiry(path string, expUnix int64, keyHex, saltHex string, signatureSize int) (sig, pathWithExp string, err error) {
	pathWithExp = appendExpiry(path, expUnix)
	sig, err = SignURL(pathWithExp, keyHex, saltHex, signatureSize)
	return sig, pathWithExp, err
}

// appendExpiry returns path with ?exp=<unix> (or &exp=...) merged in. If
// path already contains an exp, it is replaced.
func appendExpiry(path string, expUnix int64) string {
	base := path
	query := ""
	if before, after, ok := strings.Cut(path, "?"); ok {
		base = before
		query = after
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		values = url.Values{}
	}
	values.Set(ExpiryQueryParam, strconv.FormatInt(expUnix, 10))
	return base + "?" + values.Encode()
}

// VerifyURL verifies the HMAC-SHA256 signature for the given path.
//
// `path` may be any path or path+query string; it is canonicalized internally
// so the signer and verifier always agree regardless of query-param order. If
// `required` is true the signature MUST be present and valid; a missing
// key/salt is an error in required mode. In non-required mode (dev),
// verification is skipped entirely — the route is unauthenticated.
//
// Expiry: if the path includes an "exp" query parameter, it MUST be a valid
// Unix timestamp in the future (in required mode). URLs without "exp" are
// accepted for backwards compatibility — callers that need to enforce
// presence of "exp" must use VerifyURLWithPolicy.
// The `required` parameter is a genuine control switch and a dangerous one:
// false disables verification entirely. It stays in the signature because this
// is falco's public API and upstream callers depend on it; production callers
// MUST pass true (config/validator.validateSecurity enforces HMAC_REQUIRED).
func VerifyURL(signature, path string, keyHex, saltHex string, signatureSize int, required bool) error {
	return VerifyURLWithPolicy(signature, path, keyHex, saltHex, signatureSize, required, false)
}

// VerifyURLWithPolicy is VerifyURL plus an explicit requireExpiry switch. If
// requireExpiry is true, the path MUST carry a non-expired "exp" parameter.
// Use this in production to ensure leaked URLs cannot be reused indefinitely.
//
//nolint:revive // public API: `required` is the same control switch VerifyURL exposes
func VerifyURLWithPolicy(signature, path string, keyHex, saltHex string, signatureSize int, required, requireExpiry bool) error {
	if !required {
		// Non-required mode: signature is not enforced. This is a
		// development-only posture. Production callers MUST set
		// HMAC_REQUIRED=true in env; see config/validator.validateSecurity.
		return nil
	}

	if keyHex == "" || saltHex == "" {
		return ErrNoSignatureConfig
	}

	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return errors.New("server HMAC key misconfigured")
	}
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return errors.New("server HMAC salt misconfigured")
	}

	if signature == "" {
		return ErrMissingSignature
	}

	// Check expiry before HMAC to short-circuit obvious abuse, but always
	// compare HMAC before returning an expiry-specific error so we don't
	// leak timing information about whether a signature was otherwise valid.
	expValue, expPresent := extractExpiry(path)
	if requireExpiry && !expPresent {
		return ErrMissingExpiry
	}

	messageMAC, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return ErrInvalidEncoding
	}

	expected := computeSignature(Canonicalize(path), key, salt, signatureSize)
	if !hmac.Equal(messageMAC, expected) {
		return ErrSignatureMismatch
	}

	// HMAC is valid; now enforce the temporal window.
	if expPresent {
		ts, perr := strconv.ParseInt(expValue, 10, 64)
		if perr != nil {
			return ErrInvalidExpiry
		}
		if time.Now().Unix() >= ts {
			return ErrExpiredSignature
		}
	}

	return nil
}

// extractExpiry returns the raw "exp" value from the path's query string and
// whether it was present. It does NOT validate the value — only surfaces it.
func extractExpiry(path string) (string, bool) {
	_, after, ok0 := strings.Cut(path, "?")
	if !ok0 {
		return "", false
	}
	values, err := url.ParseQuery(after)
	if err != nil {
		return "", false
	}
	v, ok := values[ExpiryQueryParam]
	if !ok || len(v) == 0 {
		return "", false
	}
	return v[0], true
}

func computeSignature(str string, key, salt []byte, signatureSize int) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(salt)
	mac.Write([]byte(str))
	result := mac.Sum(nil)
	if signatureSize > 0 && signatureSize < 32 {
		return result[:signatureSize]
	}
	return result
}
