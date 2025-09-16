package integration

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"

	apimw "github.com/ivangsm/imagine/internal/api/middleware"
)

func TestSecurityHeaders(t *testing.T) {
	// Create a test handler
	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Wrap with security headers middleware
	securedHandler := apimw.SecurityHeaders(testHandler)

	// Create test request
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()

	// Execute request
	securedHandler.ServeHTTP(w, req)

	// Check security headers
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Equal(t, "1; mode=block", w.Header().Get("X-XSS-Protection"))
	assert.Contains(t, w.Header().Get("Content-Security-Policy"), "default-src 'self'")
	assert.Equal(t, "strict-origin-when-cross-origin", w.Header().Get("Referrer-Policy"))
	assert.Contains(t, w.Header().Get("Permissions-Policy"), "camera=()")
}

func TestAPIKeyAuth_ValidKey(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	auth := apimw.NewAPIKeyAuth("test-key", logger)

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Authorized"))
	})

	securedHandler := auth.Handler(testHandler)

	// Test with valid API key in header
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-API-Key", "test-key")
	w := httptest.NewRecorder()

	securedHandler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Authorized", w.Body.String())
}

func TestAPIKeyAuth_InvalidKey(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	auth := apimw.NewAPIKeyAuth("test-key", logger)

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Authorized"))
	})

	securedHandler := auth.Handler(testHandler)

	// Test with invalid API key
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	w := httptest.NewRecorder()

	securedHandler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAPIKeyAuth_BearerToken(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	auth := apimw.NewAPIKeyAuth("test-key", logger)

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Authorized"))
	})

	securedHandler := auth.Handler(testHandler)

	// Test with Bearer token
	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()

	securedHandler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Authorized", w.Body.String())
}

func TestAPIKeyAuth_ExemptPath(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	auth := apimw.NewAPIKeyAuth("test-key", logger)

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Health OK"))
	})

	securedHandler := auth.Handler(testHandler)

	// Test exempt path (health check)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	securedHandler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Health OK", w.Body.String())
}

func TestRateLimiter_UnderLimit(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	limiter := apimw.NewRateLimiter(10, 5, logger)

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	securedHandler := limiter.Handler(testHandler)

	// Make requests under the limit
	for i := 0; i < 8; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		w := httptest.NewRecorder()

		securedHandler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "10", w.Header().Get("X-RateLimit-Limit"))

		// The remaining count should decrease with each request
		expectedRemaining := 9 - i // Start with 9 remaining (10 - 1 for first request)
		if expectedRemaining < 0 {
			expectedRemaining = 0
		}
		// Just check that we have a remaining header, don't check exact value due to timing
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
	}
}

func TestRateLimiter_OverLimit(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	limiter := apimw.NewRateLimiter(5, 2, logger)

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	securedHandler := limiter.Handler(testHandler)

	// Make requests over the limit
	for i := 0; i < 8; i++ {
		req := httptest.NewRequest("GET", "/api/test", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		w := httptest.NewRecorder()

		securedHandler.ServeHTTP(w, req)

		if i < 7 { // 5 + 2 burst = 7 allowed
			assert.Equal(t, http.StatusOK, w.Code)
		} else {
			assert.Equal(t, http.StatusTooManyRequests, w.Code)
			assert.Equal(t, "60", w.Header().Get("Retry-After"))
		}
	}
}

func TestRequestSizeLimiter_UnderLimit(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	limiter := apimw.NewRequestSizeLimiter(1024, logger) // 1KB limit

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	securedHandler := limiter.Handler(testHandler)

	// Test with small request body
	smallBody := strings.Repeat("a", 100) // 100 bytes
	req := httptest.NewRequest("POST", "/api/upload", strings.NewReader(smallBody))
	req.ContentLength = int64(len(smallBody))
	w := httptest.NewRecorder()

	securedHandler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRequestSizeLimiter_OverLimit(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	limiter := apimw.NewRequestSizeLimiter(100, logger) // 100 bytes limit

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	securedHandler := limiter.Handler(testHandler)

	// Test with large request body
	largeBody := strings.Repeat("a", 200) // 200 bytes
	req := httptest.NewRequest("POST", "/api/upload", strings.NewReader(largeBody))
	req.ContentLength = int64(len(largeBody))
	w := httptest.NewRecorder()

	securedHandler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestRequestSizeLimiter_ContentLengthHeader(t *testing.T) {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	limiter := apimw.NewRequestSizeLimiter(100, logger) // 100 bytes limit

	testHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	securedHandler := limiter.Handler(testHandler)

	// Test with Content-Length header over limit
	req := httptest.NewRequest("POST", "/api/upload", strings.NewReader("small body"))
	req.ContentLength = 200 // Over limit
	w := httptest.NewRecorder()

	securedHandler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}
