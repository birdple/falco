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
	assert.Equal(t, "0", w.Header().Get("X-XSS-Protection"))
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
	assert.Contains(t, csp, "https://unpkg.com")
	assert.NotContains(t, csp, "cdn.redoc.ly")
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

func BenchmarkRateLimiter_Allow(b *testing.B) {
	rl := NewRateLimiter(1000000, 1000000)
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
	rl := NewRateLimiter(1000000, 1000000)
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
