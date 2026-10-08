package middleware

import (
	"crypto/subtle"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/rs/zerolog"
)

// ZerologRequestLogger is a Chi-compatible middleware that logs requests via zerolog.
func ZerologRequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(wrapped, r)

		var evt *zerolog.Event
		switch {
		case wrapped.statusCode >= 500:
			evt = logger.Error()
		case wrapped.statusCode >= 400:
			evt = logger.Warn()
		default:
			evt = logger.Info()
		}

		evt.
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", wrapped.statusCode).
			Int("size", wrapped.size).
			Dur("duration", time.Since(start)).
			Str("ip", httputil.GetClientIP(r)).
			Str("user_agent", httputil.GetUserAgent(r)).
			Str("referer", r.Referer()).
			Str("request_id", middleware.GetReqID(r.Context())).
			Msg("request")
	})
}

// SecurityHeaders adds security headers to responses
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		// The legacy XSS auditor is gone from every current browser and its
		// "block" mode was itself an info-leak vector; "0" is the recommended
		// value now that the CSP below does the real work.
		w.Header().Set("X-XSS-Protection", "0")
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}

		// Strict CSP for API and image delivery routes — no unsafe-eval needed.
		csp := "default-src 'self'; " +
			"script-src 'self'; " +
			"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
			"font-src 'self' https://fonts.gstatic.com; " +
			"img-src 'self' data:; " +
			"connect-src 'self'"

		// Dashboard UI uses Alpine.js which requires 'unsafe-eval' for x-data/@click.
		// Scoped to UI routes only so API/image paths keep the strict policy.
		if isUIPath(r.URL.Path) {
			csp = "default-src 'self'; " +
				"script-src 'self' 'unsafe-eval'; " +
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
				"font-src 'self' https://fonts.gstatic.com; " +
				"img-src 'self' data:; " +
				"connect-src 'self'"
		}

		if strings.HasPrefix(r.URL.Path, "/docs") {
			csp = "default-src 'self' https://cdn.redoc.ly; " +
				"script-src 'self' https://cdn.redoc.ly blob: 'unsafe-eval'; " +
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
				"font-src 'self' https://fonts.gstatic.com; " +
				"img-src 'self' data: https://cdn.redoc.ly; " +
				"worker-src 'self' blob:; " +
				"connect-src 'self' https://cdn.redoc.ly"
		}
		w.Header().Set("Content-Security-Policy", csp)

		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

		next.ServeHTTP(w, r)
	})
}

// isUIPath reports whether the request targets a dashboard/UI route that
// requires a relaxed CSP (Alpine.js needs 'unsafe-eval'). API, image, and
// metrics routes are excluded so they keep the strict default policy.
func isUIPath(path string) bool {
	switch path {
	case "/", "/dashboard", "/object", "/playground", "/signer", "/ops":
		return true
	}
	return strings.HasPrefix(path, "/ui/") || strings.HasPrefix(path, "/static/")
}

// RestrictedFileServer serves only files with known safe extensions.
func RestrictedFileServer(root http.FileSystem) http.Handler {
	fs := http.FileServer(root)
	allowedExtensions := map[string]bool{
		".css":   true,
		".js":    true,
		".ico":   true,
		".png":   true,
		".jpg":   true,
		".jpeg":  true,
		".svg":   true,
		".woff":  true,
		".woff2": true,
		".ttf":   true,
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ext := strings.ToLower(filepath.Ext(r.URL.Path))
		if !allowedExtensions[ext] {
			http.NotFound(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	})
}

// APIKeyAuth provides API key authentication for a route group. It has no
// exempt paths: it is only ever mounted on groups that are protected in full.
type APIKeyAuth struct {
	apiKey string
}

// NewAPIKeyAuth creates a new API key authentication middleware.
func NewAPIKeyAuth(apiKey string) *APIKeyAuth {
	return &APIKeyAuth{apiKey: apiKey}
}

// providedAPIKey reads the key from X-API-Key, or from a Bearer token.
func providedAPIKey(r *http.Request) string {
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return key
}

// Handler returns the middleware handler. With an empty configured key,
// authentication is disabled and every request passes.
func (a *APIKeyAuth) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.apiKey == "" {
			next.ServeHTTP(w, r)
			return
		}

		providedKey := providedAPIKey(r)
		if providedKey == "" || subtle.ConstantTimeCompare([]byte(providedKey), []byte(a.apiKey)) != 1 {
			msg := "Invalid API key"
			if providedKey == "" {
				msg = "Missing API key"
			}
			logger.Warn().
				Str("ip", httputil.GetClientIP(r)).
				Str("user_agent", httputil.GetUserAgent(r)).
				Str("path", r.URL.Path).
				Msg(msg)
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// RequestSizeLimiter limits the size of incoming requests
type RequestSizeLimiter struct {
	maxSize int64
}

// NewRequestSizeLimiter creates a new request size limiter
func NewRequestSizeLimiter(maxSize int64) *RequestSizeLimiter {
	return &RequestSizeLimiter{
		maxSize: maxSize,
	}
}

// Handler returns the request size limiting middleware handler.
//
// A declared Content-Length over the limit is refused up front; a body without
// one is wrapped in http.MaxBytesReader, so reading past the limit is an error
// the handler sees (and the connection is closed) rather than a silent EOF
// that looks like a complete, truncated body.
func (rsl *RequestSizeLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > rsl.maxSize {
			logger.Warn().
				Str("ip", httputil.GetClientIP(r)).
				Int64("content_length", r.ContentLength).
				Int64("max_size", rsl.maxSize).
				Msg("Request too large")

			writeError(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "Request body too large")
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, rsl.maxSize)
		next.ServeHTTP(w, r)
	})
}
