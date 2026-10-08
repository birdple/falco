package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func hit(h http.Handler, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remote
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestRateLimiter_AllowsBurstThenBlocks(t *testing.T) {
	h := NewRateLimiter(2, 1).Handler(okHandler())

	for i := range 3 {
		w := hit(h, "1.2.3.4:1234")
		require.Equal(t, http.StatusOK, w.Code, "request %d", i)
		assert.Equal(t, "2", w.Header().Get("X-RateLimit-Limit"))
	}

	w := hit(h, "1.2.3.4:1234")
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "0", w.Header().Get("X-RateLimit-Remaining"))
	assert.Equal(t, "30", w.Header().Get("Retry-After"), "one token every 30s at 2/min")

	var body struct {
		Success bool `json:"success"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.False(t, body.Success)
	assert.Equal(t, "RATE_LIMITED", body.Error.Code)
}

func TestRateLimiter_ClientsAreIndependent(t *testing.T) {
	h := NewRateLimiter(1, 0).Handler(okHandler())
	assert.Equal(t, http.StatusOK, hit(h, "1.2.3.4:1").Code)
	assert.Equal(t, http.StatusTooManyRequests, hit(h, "1.2.3.4:1").Code)
	assert.Equal(t, http.StatusOK, hit(h, "5.6.7.8:1").Code)
}

// One host owns a whole /64; rotating addresses inside it must not reset the
// limit.
func TestRateLimiter_IPv6GroupedBySlash64(t *testing.T) {
	h := NewRateLimiter(1, 0).Handler(okHandler())
	assert.Equal(t, http.StatusOK, hit(h, "[2001:db8:1:2::1]:1").Code)
	assert.Equal(t, http.StatusTooManyRequests, hit(h, "[2001:db8:1:2::ffff]:1").Code)
	assert.Equal(t, http.StatusOK, hit(h, "[2001:db8:1:3::1]:1").Code)
}

func TestRateLimiter_TableIsBounded(t *testing.T) {
	rl := newBoundedRateLimiter(100, 0, rateShards) // one client per shard
	h := rl.Handler(okHandler())
	for i := range 1000 {
		hit(h, "10.1."+strconv.Itoa(i/256)+"."+strconv.Itoa(i%256)+":1")
	}
	total := 0
	for i := range rl.shards {
		total += rl.shards[i].lru.Len()
		assert.Len(t, rl.shards[i].entries, rl.shards[i].lru.Len())
	}
	assert.LessOrEqual(t, total, rateShards)
}

func TestRequestSizeLimiter_OverflowIsAnError(t *testing.T) {
	limiter := NewRequestSizeLimiter(5)
	var readErr error
	h := limiter.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 64)
		for readErr == nil {
			_, readErr = r.Body.Read(buf)
		}
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("this is longer than 5 bytes"))
	req.ContentLength = -1
	h.ServeHTTP(httptest.NewRecorder(), req)

	var maxErr *http.MaxBytesError
	assert.ErrorAs(t, readErr, &maxErr, "reading past the limit must fail, not look like EOF")
}

func TestAPIKeyAuth_UnauthorizedIsJSON(t *testing.T) {
	h := NewAPIKeyAuth("secret").Handler(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "no path is exempt any more")
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Contains(t, w.Body.String(), `"UNAUTHORIZED"`)
}
