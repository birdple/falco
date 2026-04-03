package middleware

import (
	"crypto/subtle"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
			Str("request_id", middleware.GetReqID(r.Context())).
			Msg("request")
	})
}

// SecurityHeaders adds security headers to responses
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")

		// Use a stricter CSP for the main app, allow unsafe-eval only for /docs
		csp := "default-src 'self'; " +
			"script-src 'self'; " +
			"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
			"font-src 'self' https://fonts.gstatic.com; " +
			"img-src 'self' data:; " +
			"connect-src 'self'"

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

// RestrictedFileServer serves only files with known safe extensions.
func RestrictedFileServer(root http.FileSystem) http.Handler {
	fs := http.FileServer(root)
	allowedExtensions := map[string]bool{
		".css":  true,
		".js":   true,
		".ico":  true,
		".png":  true,
		".jpg":  true,
		".jpeg": true,
		".svg":  true,
		".woff": true,
		".woff2": true,
		".ttf":  true,
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

// APIKeyAuth provides API key authentication
type APIKeyAuth struct {
	apiKey             string
	exemptPaths        map[string]bool
	exemptPathPrefixes []string
}

// NewAPIKeyAuth creates a new API key authentication middleware
func NewAPIKeyAuth(apiKey string) *APIKeyAuth {
	exemptPaths := map[string]bool{
		"/health": true,
		"/":       true,
	}

	exemptPathPrefixes := []string{
		"/api/v1/images/",
	}

	return &APIKeyAuth{
		apiKey:             apiKey,
		exemptPaths:        exemptPaths,
		exemptPathPrefixes: exemptPathPrefixes,
	}
}

// Handler returns the middleware handler
func (a *APIKeyAuth) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.exemptPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		for _, prefix := range a.exemptPathPrefixes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				next.ServeHTTP(w, r)
				return
			}
		}

		providedKey := r.Header.Get("X-API-Key")
		if providedKey == "" {
			providedKey = r.Header.Get("Authorization")
			providedKey, _ = strings.CutPrefix(providedKey, "Bearer ")
		}

		if a.apiKey != "" {
			if providedKey == "" {
				logger.Warn().
					Str("ip", httputil.GetClientIP(r)).
					Str("user_agent", httputil.GetUserAgent(r)).
					Str("path", r.URL.Path).
					Msg("Missing API key")

				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			if subtle.ConstantTimeCompare([]byte(providedKey), []byte(a.apiKey)) != 1 {
				logger.Warn().
					Str("ip", httputil.GetClientIP(r)).
					Str("user_agent", httputil.GetUserAgent(r)).
					Str("path", r.URL.Path).
					Msg("Invalid API key")

				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// RateLimiter provides rate limiting functionality
type RateLimiter struct {
	requestsPerMinute int
	burst             int
	clients           map[string]*clientLimiter
	mu                sync.RWMutex
	cleanupInterval   time.Duration
	maxClientAge      time.Duration
}

// clientLimiter tracks requests for a specific client
type clientLimiter struct {
	requests    []time.Time
	lastCleanup time.Time
	lastSeen    time.Time
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(requestsPerMinute, burst int) *RateLimiter {
	rl := &RateLimiter{
		requestsPerMinute: requestsPerMinute,
		burst:             burst,
		clients:           make(map[string]*clientLimiter),
		cleanupInterval:   5 * time.Minute,
		maxClientAge:      15 * time.Minute,
	}

	go rl.backgroundCleanup()

	return rl
}

// backgroundCleanup periodically removes inactive clients to prevent memory leak
func (rl *RateLimiter) backgroundCleanup() {
	defer func() {
		if r := recover(); r != nil {
			logger.Error().Interface("panic", r).Msg("RateLimiter cleanup panic recovered")
			go rl.backgroundCleanup()
		}
	}()

	ticker := time.NewTicker(rl.cleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, limiter := range rl.clients {
			if now.Sub(limiter.lastSeen) > rl.maxClientAge {
				delete(rl.clients, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// Handler returns the rate limiting middleware handler
func (rl *RateLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := httputil.GetClientIP(r)
		now := time.Now()

		rl.mu.Lock()
		limiter, exists := rl.clients[clientIP]
		if !exists {
			limiter = &clientLimiter{
				requests:    make([]time.Time, 0),
				lastCleanup: now,
				lastSeen:    now,
			}
			rl.clients[clientIP] = limiter
		}
		limiter.lastSeen = now

		rl.cleanupOldRequests(limiter)

		requestCount := len(limiter.requests)

		if requestCount >= rl.requestsPerMinute+rl.burst {
			rl.mu.Unlock()
			logger.Warn().
				Str("ip", clientIP).
				Str("user_agent", httputil.GetUserAgent(r)).
				Str("path", r.URL.Path).
				Msg("Rate limit exceeded")

			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rl.requestsPerMinute))
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("Retry-After", "60")

			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}

		limiter.requests = append(limiter.requests, now)
		remaining := rl.requestsPerMinute + rl.burst - len(limiter.requests)
		rl.mu.Unlock()

		if remaining < 0 {
			remaining = 0
		}

		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rl.requestsPerMinute))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))

		next.ServeHTTP(w, r)
	})
}

// cleanupOldRequests removes requests older than 1 minute.
// IMPORTANT: Caller MUST hold rl.mu lock before calling this function.
func (rl *RateLimiter) cleanupOldRequests(limiter *clientLimiter) {
	now := time.Now()
	oneMinuteAgo := now.Add(-time.Minute)

	validRequests := make([]time.Time, 0)
	for _, reqTime := range limiter.requests {
		if reqTime.After(oneMinuteAgo) {
			validRequests = append(validRequests, reqTime)
		}
	}

	limiter.requests = validRequests
	limiter.lastCleanup = now
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

// Handler returns the request size limiting middleware handler
func (rsl *RequestSizeLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > rsl.maxSize {
			logger.Warn().
				Str("ip", httputil.GetClientIP(r)).
				Int64("content_length", r.ContentLength).
				Int64("max_size", rsl.maxSize).
				Msg("Request too large")

			http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
			return
		}

		r.Body = &limitedReader{
			reader:    r.Body,
			remaining: rsl.maxSize,
			ip:        httputil.GetClientIP(r),
		}

		next.ServeHTTP(w, r)
	})
}

// limitedReader wraps an io.Reader to limit the amount of data read
type limitedReader struct {
	reader    io.ReadCloser
	remaining int64
	ip        string
	totalRead int64
}

func (lr *limitedReader) Read(p []byte) (n int, err error) {
	if lr.remaining <= 0 {
		logger.Warn().
			Str("ip", lr.ip).
			Int64("total_read", lr.totalRead).
			Msg("Request size limit exceeded during read")

		return 0, io.EOF
	}

	if int64(len(p)) > lr.remaining {
		p = p[:lr.remaining]
	}

	n, err = lr.reader.Read(p)
	lr.remaining -= int64(n)
	lr.totalRead += int64(n)

	return n, err
}

func (lr *limitedReader) Close() error {
	return lr.reader.Close()
}
