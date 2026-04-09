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
	"strings"
)

var (
	ErrNoSignatureConfig = errors.New("HMAC key and salt are not configured")
	ErrInvalidEncoding   = errors.New("invalid signature encoding")
	ErrSignatureMismatch = errors.New("signature mismatch")
	ErrMissingSignature  = errors.New("missing signature")
)

// Canonicalize returns a deterministic "path[?sortedQuery]" form for HMAC
// signing. Both the signer and the verifier MUST call this so that a URL like
// "/images/foo?w=400&format=webp" signs identically regardless of query-param
// order. The "sig" parameter, if present, is always removed before sorting.
//
// The canonical form is the URL path followed by url.Values.Encode(), which
// sorts keys alphabetically. If there are no query params after removing
// "sig", only the path is returned (no trailing "?").
func Canonicalize(rawPath string) string {
	path := rawPath
	query := ""
	if i := strings.IndexByte(rawPath, '?'); i >= 0 {
		path = rawPath[:i]
		query = rawPath[i+1:]
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
	if values == nil || len(values) == 0 {
		return path
	}
	// Work on a copy so callers can still read "sig" from their original.
	clone := make(url.Values, len(values))
	for k, v := range values {
		if k == "sig" {
			continue
		}
		clone[k] = v
	}
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

// VerifyURL verifies the HMAC-SHA256 signature for the given path.
//
// `path` may be any path or path+query string; it is canonicalized internally
// so the signer and verifier always agree regardless of query-param order. If
// `required` is true the signature MUST be present and valid; a missing
// key/salt is an error in required mode. In non-required mode (dev),
// verification is skipped entirely — the route is unauthenticated.
func VerifyURL(signature, path string, keyHex, saltHex string, signatureSize int, required bool) error {
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

	messageMAC, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return ErrInvalidEncoding
	}

	expected := computeSignature(Canonicalize(path), key, salt, signatureSize)
	if !hmac.Equal(messageMAC, expected) {
		return ErrSignatureMismatch
	}
	return nil
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
