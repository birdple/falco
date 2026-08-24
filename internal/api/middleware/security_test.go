package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecurityHeaders(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/images", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	assert.Equal(t, "1; mode=block", w.Header().Get("X-XSS-Protection"))
	assert.Equal(t, "strict-origin-when-cross-origin", w.Header().Get("Referrer-Policy"))
	assert.Contains(t, w.Header().Get("Permissions-Policy"), "camera=()")

	// Regular path should NOT have unsafe-eval
	csp := w.Header().Get("Content-Security-Policy")
	assert.NotContains(t, csp, "unsafe-eval")
	assert.Contains(t, csp, "default-src 'self'")
}

func TestSecurityHeaders_DocsPath(t *testing.T) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/docs/openapi.yaml", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	csp := w.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "unsafe-eval")
	assert.Contains(t, csp, "cdn.redoc.ly")
}

func TestRestrictedFileServer_AllowedExtensions(t *testing.T) {
	// Create a test file system with a handler that just returns 200
	handler := RestrictedFileServer(http.Dir("."))

	allowed := []string{".css", ".js", ".ico", ".png", ".jpg", ".jpeg", ".svg", ".woff", ".woff2", ".ttf"}
	for _, ext := range allowed {
		t.Run(ext, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test"+ext, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			// Should NOT return 404 for extension check (may 404 for file not found)
			// The key is it passes the extension filter
		})
	}
}

func TestRestrictedFileServer_BlockedExtensions(t *testing.T) {
	handler := RestrictedFileServer(http.Dir("."))

	blocked := []string{".go", ".env", ".yaml", ".json", ".sh", ".exe", ".php", ".html"}
	for _, ext := range blocked {
		t.Run(ext, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/test"+ext, nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	}
}

func TestAPIKeyAuth_ExemptPaths(t *testing.T) {
	auth := NewAPIKeyAuth("secret-key")

	handler := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// /health should be exempt
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// / should be exempt
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAPIKeyAuth_ExemptPrefixes(t *testing.T) {
	auth := NewAPIKeyAuth("secret-key")

	handler := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// /api/v1/images/ prefix should be exempt
	req := httptest.NewRequest(http.MethodGet, "/api/v1/images/abc123", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAPIKeyAuth_MissingKey(t *testing.T) {
	auth := NewAPIKeyAuth("secret-key")

	handler := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAPIKeyAuth_InvalidKey(t *testing.T) {
	auth := NewAPIKeyAuth("secret-key")

	handler := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestAPIKeyAuth_ValidKey_XAPIKey(t *testing.T) {
	auth := NewAPIKeyAuth("secret-key")

	handler := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", nil)
	req.Header.Set("X-API-Key", "secret-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAPIKeyAuth_ValidKey_BearerToken(t *testing.T) {
	auth := NewAPIKeyAuth("secret-key")

	handler := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", nil)
	req.Header.Set("Authorization", "Bearer secret-key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAPIKeyAuth_EmptyAPIKey(t *testing.T) {
	// When API key is empty, all requests should pass
	auth := NewAPIKeyAuth("")

	handler := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRateLimiter_AllowsRequests(t *testing.T) {
	rl := &RateLimiter{
		requestsPerMinute: 10,
		burst:             5,
		clients:           make(map[string]*clientLimiter),
		maxClients:        1000,
		stopCleanup:       make(chan struct{}),
	}
	defer rl.Stop()

	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Limit"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
}

func TestRateLimiter_BlocksExcessiveRequests(t *testing.T) {
	rl := &RateLimiter{
		requestsPerMinute: 2,
		burst:             0,
		clients:           make(map[string]*clientLimiter),
		maxClients:        1000,
		stopCleanup:       make(chan struct{}),
	}
	defer rl.Stop()

	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Make 2 allowed requests
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// Third request should be rate limited
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "0", w.Header().Get("X-RateLimit-Remaining"))
	assert.Equal(t, "60", w.Header().Get("Retry-After"))
}

func TestRateLimiter_DifferentClients(t *testing.T) {
	rl := &RateLimiter{
		requestsPerMinute: 1,
		burst:             0,
		clients:           make(map[string]*clientLimiter),
		maxClients:        1000,
		stopCleanup:       make(chan struct{}),
	}
	defer rl.Stop()

	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// First client uses their single allowed request
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// Second client should still be allowed
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "5.6.7.8:1234"
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
}

func TestRateLimiter_EvictsOldestClient(t *testing.T) {
	rl := &RateLimiter{
		requestsPerMinute: 100,
		burst:             10,
		clients:           make(map[string]*clientLimiter),
		maxClients:        2,
		stopCleanup:       make(chan struct{}),
	}
	defer rl.Stop()

	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Fill up to max clients
	for _, ip := range []string{"1.1.1.1:1", "2.2.2.2:1", "3.3.3.3:1"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// Should still have maxClients entries
	rl.mu.RLock()
	assert.LessOrEqual(t, len(rl.clients), rl.maxClients+1)
	rl.mu.RUnlock()
}

func TestRequestSizeLimiter_AllowsSmallRequests(t *testing.T) {
	limiter := NewRequestSizeLimiter(1024)

	handler := limiter.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "hello", string(body))
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello"))
	req.ContentLength = 5
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestRequestSizeLimiter_BlocksLargeContentLength(t *testing.T) {
	limiter := NewRequestSizeLimiter(100)

	handler := limiter.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("data"))
	req.ContentLength = 200
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestRequestSizeLimiter_LimitsBodyRead(t *testing.T) {
	limiter := NewRequestSizeLimiter(5)

	handler := limiter.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// Should only read up to maxSize bytes
		assert.LessOrEqual(t, len(body), 5)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("this is longer than 5 bytes"))
	req.ContentLength = -1 // unknown content length
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestResponseWriter_WriteHeader(t *testing.T) {
	w := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

	rw.WriteHeader(http.StatusNotFound)
	assert.Equal(t, http.StatusNotFound, rw.statusCode)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestResponseWriter_Write(t *testing.T) {
	w := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

	n, err := rw.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, 5, rw.size)

	n, err = rw.Write([]byte(" world"))
	require.NoError(t, err)
	assert.Equal(t, 6, n)
	assert.Equal(t, 11, rw.size)
}

func TestLimitedReader_Read(t *testing.T) {
	reader := io.NopCloser(strings.NewReader("hello world"))
	lr := &limitedReader{reader: reader, remaining: 5, ip: "1.2.3.4"}

	buf := make([]byte, 20)
	n, _ := lr.Read(buf)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", string(buf[:n]))
	assert.Equal(t, int64(5), lr.totalRead)
}

func BenchmarkSecurityHeaders(b *testing.B) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/images", nil)
	b.ResetTimer()
	for range b.N {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}
}

func BenchmarkSecurityHeaders_DocsPath(b *testing.B) {
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/docs/index.html", nil)
	b.ResetTimer()
	for range b.N {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}
}

func BenchmarkAPIKeyAuth_ValidKey(b *testing.B) {
	auth := NewAPIKeyAuth("my-secret-api-key")
	handler := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", nil)
	req.Header.Set("X-API-Key", "my-secret-api-key")
	b.ResetTimer()
	for range b.N {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}
}

func BenchmarkAPIKeyAuth_ExemptPath(b *testing.B) {
	auth := NewAPIKeyAuth("my-secret-api-key")
	handler := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	b.ResetTimer()
	for range b.N {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}
}

func BenchmarkRateLimiter_Allow(b *testing.B) {
	rl := &RateLimiter{
		requestsPerMinute: 1000000,
		burst:             1000000,
		clients:           make(map[string]*clientLimiter),
		maxClients:        100000,
		stopCleanup:       make(chan struct{}),
	}
	defer rl.Stop()
	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "1.2.3.4:1234"
	b.ResetTimer()
	for range b.N {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}
}

func BenchmarkRateLimiter_Parallel(b *testing.B) {
	rl := &RateLimiter{
		requestsPerMinute: 1000000,
		burst:             1000000,
		clients:           make(map[string]*clientLimiter),
		maxClients:        100000,
		stopCleanup:       make(chan struct{}),
	}
	defer rl.Stop()
	handler := rl.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "1.2.3.4:1234"
		for pb.Next() {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
		}
	})
}

func TestLimitedReader_Close(t *testing.T) {
	reader := io.NopCloser(strings.NewReader("data"))
	lr := &limitedReader{reader: reader, remaining: 100, ip: "1.2.3.4"}
	assert.NoError(t, lr.Close())
}
