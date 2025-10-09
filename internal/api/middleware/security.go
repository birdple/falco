package middleware

import (
	"crypto/subtle"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/birdple/imagine/internal/pkg/httputil"
	"github.com/sirupsen/logrus"
)

// SecurityHeaders adds security headers to responses
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// Prevent clickjacking
		w.Header().Set("X-Frame-Options", "DENY")

		// XSS protection
		w.Header().Set("X-XSS-Protection", "1; mode=block")

		// Content Security Policy
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https:; script-src 'self'")

		// Referrer Policy
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// Permissions Policy
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

		next.ServeHTTP(w, r)
	})
}

// APIKeyAuth provides API key authentication
type APIKeyAuth struct {
	apiKey      string
	logger      *logrus.Logger
	exemptPaths map[string]bool
}

// NewAPIKeyAuth creates a new API key authentication middleware
func NewAPIKeyAuth(apiKey string, logger *logrus.Logger) *APIKeyAuth {
	exemptPaths := map[string]bool{
		"/health": true,
		"/":       true,
	}

	return &APIKeyAuth{
		apiKey:      apiKey,
		logger:      logger,
		exemptPaths: exemptPaths,
	}
}

// Handler returns the middleware handler
func (a *APIKeyAuth) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip authentication for exempt paths
		if a.exemptPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		// Get API key from header
		providedKey := r.Header.Get("X-API-Key")
		if providedKey == "" {
			providedKey = r.Header.Get("Authorization")
			providedKey, _ = strings.CutPrefix(providedKey, "Bearer ")
		}

		// Check if API key is required
		if a.apiKey != "" {
			if providedKey == "" {
				a.logger.WithFields(logrus.Fields{
					"ip":         httputil.GetClientIP(r),
					"user_agent": httputil.GetUserAgent(r),
					"path":       r.URL.Path,
				}).Warn("Missing API key")

				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Use constant-time comparison to prevent timing attacks
			if subtle.ConstantTimeCompare([]byte(providedKey), []byte(a.apiKey)) != 1 {
				a.logger.WithFields(logrus.Fields{
					"ip":         httputil.GetClientIP(r),
					"user_agent": httputil.GetUserAgent(r),
					"path":       r.URL.Path,
				}).Warn("Invalid API key")

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
	logger            *logrus.Logger
}

// clientLimiter tracks requests for a specific client
type clientLimiter struct {
	requests    []time.Time
	lastCleanup time.Time
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(requestsPerMinute, burst int, logger *logrus.Logger) *RateLimiter {
	return &RateLimiter{
		requestsPerMinute: requestsPerMinute,
		burst:             burst,
		clients:           make(map[string]*clientLimiter),
		logger:            logger,
	}
}

// Handler returns the rate limiting middleware handler
func (rl *RateLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := httputil.GetClientIP(r)

		// Get or create client limiter
		limiter, exists := rl.clients[clientIP]
		if !exists {
			limiter = &clientLimiter{
				requests:    make([]time.Time, 0),
				lastCleanup: time.Now(),
			}
			rl.clients[clientIP] = limiter
		}

		// Clean up old requests
		rl.cleanupOldRequests(limiter)

		// Check rate limit
		if len(limiter.requests) >= rl.requestsPerMinute+rl.burst {
			rl.logger.WithFields(logrus.Fields{
				"ip":         clientIP,
				"user_agent": httputil.GetUserAgent(r),
				"path":       r.URL.Path,
			}).Warn("Rate limit exceeded")

			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rl.requestsPerMinute))
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("Retry-After", "60")

			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}

		// Add current request
		limiter.requests = append(limiter.requests, time.Now())

		// Set rate limit headers
		remaining := rl.requestsPerMinute + rl.burst - len(limiter.requests)
		if remaining < 0 {
			remaining = 0
		}

		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(rl.requestsPerMinute))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))

		next.ServeHTTP(w, r)
	})
}

// cleanupOldRequests removes requests older than 1 minute
func (rl *RateLimiter) cleanupOldRequests(limiter *clientLimiter) {
	now := time.Now()
	oneMinuteAgo := now.Add(-time.Minute)

	// Clean up old requests
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
	logger  *logrus.Logger
}

// NewRequestSizeLimiter creates a new request size limiter
func NewRequestSizeLimiter(maxSize int64, logger *logrus.Logger) *RequestSizeLimiter {
	return &RequestSizeLimiter{
		maxSize: maxSize,
		logger:  logger,
	}
}

// Handler returns the request size limiting middleware handler
func (rsl *RequestSizeLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check Content-Length header
		if r.ContentLength > rsl.maxSize {
			rsl.logger.WithFields(logrus.Fields{
				"ip":             httputil.GetClientIP(r),
				"content_length": r.ContentLength,
				"max_size":       rsl.maxSize,
			}).Warn("Request too large")

			http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
			return
		}

		// Wrap the request body to limit reading
		r.Body = &limitedReader{
			reader:    r.Body,
			remaining: rsl.maxSize,
			logger:    rsl.logger,
			ip:        httputil.GetClientIP(r),
		}

		next.ServeHTTP(w, r)
	})
}

// limitedReader wraps an io.Reader to limit the amount of data read
type limitedReader struct {
	reader    io.ReadCloser
	remaining int64
	logger    *logrus.Logger
	ip        string
	totalRead int64
}

func (lr *limitedReader) Read(p []byte) (n int, err error) {
	if lr.remaining <= 0 {
		lr.logger.WithFields(logrus.Fields{
			"ip":         lr.ip,
			"total_read": lr.totalRead,
			"max_size":   lr.remaining,
		}).Warn("Request size limit exceeded during read")

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
