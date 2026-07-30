package httputil

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetClientIP_RemoteAddr(t *testing.T) {
	// Reset trusted proxies for this test
	oldProxies := trustedProxyCIDRs
	trustedProxyCIDRs = nil
	defer func() { trustedProxyCIDRs = oldProxies }()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.50:12345"

	ip := GetClientIP(req)
	assert.Equal(t, "203.0.113.50", ip)
}

func TestGetClientIP_XForwardedFor(t *testing.T) {
	oldProxies := trustedProxyCIDRs
	SetTrustedProxies([]string{"10.0.0.0/8"})
	defer func() { trustedProxyCIDRs = oldProxies }()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.50, 70.41.3.18")

	ip := GetClientIP(req)
	assert.Equal(t, "203.0.113.50", ip)
}

func TestGetClientIP_XRealIP(t *testing.T) {
	oldProxies := trustedProxyCIDRs
	SetTrustedProxies([]string{"10.0.0.0/8"})
	defer func() { trustedProxyCIDRs = oldProxies }()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Real-IP", "203.0.113.50")

	ip := GetClientIP(req)
	assert.Equal(t, "203.0.113.50", ip)
}

// TestGetClientIP_FailClosedNoAllowlist verifies that when the allowlist is
// empty (default on unconfigured deploys), forwarded headers from a public IP
// are ignored — the direct peer address wins.
func TestGetClientIP_FailClosedNoAllowlist(t *testing.T) {
	oldProxies := trustedProxyCIDRs
	trustedProxyCIDRs = nil
	defer func() { trustedProxyCIDRs = oldProxies }()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.50:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	ip := GetClientIP(req)
	assert.Equal(t, "203.0.113.50", ip)
}

// TestGetClientIP_LoopbackAlwaysTrusted verifies that calls from 127.0.0.1
// continue to be trusted as proxies without explicit configuration. This
// covers health checks, local dev, and Docker host-network scenarios.
func TestGetClientIP_LoopbackAlwaysTrusted(t *testing.T) {
	oldProxies := trustedProxyCIDRs
	SetTrustedProxies(nil) // collapses to loopback-only
	defer func() { trustedProxyCIDRs = oldProxies }()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")

	ip := GetClientIP(req)
	assert.Equal(t, "203.0.113.50", ip)
}

func TestGetClientIP_UntrustedProxy(t *testing.T) {
	oldProxies := trustedProxyCIDRs
	SetTrustedProxies([]string{"10.0.0.0/8"})
	defer func() { trustedProxyCIDRs = oldProxies }()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.50:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	// Should NOT trust forwarded header from non-trusted proxy
	ip := GetClientIP(req)
	assert.Equal(t, "203.0.113.50", ip)
}

func TestGetClientIP_TrustedProxy(t *testing.T) {
	oldProxies := trustedProxyCIDRs
	SetTrustedProxies([]string{"10.0.0.0/8"})
	defer func() { trustedProxyCIDRs = oldProxies }()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")

	ip := GetClientIP(req)
	assert.Equal(t, "203.0.113.50", ip)
}

func TestSetTrustedProxies(t *testing.T) {
	oldProxies := trustedProxyCIDRs
	defer func() { trustedProxyCIDRs = oldProxies }()

	// Loopback (127.0.0.0/8 + ::1/128) is always included: +2 on every call.
	const loopbackEntries = 2

	SetTrustedProxies([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.1.1"})
	assert.Len(t, trustedProxyCIDRs, loopbackEntries+3)

	// Bare IP should be converted to /32
	SetTrustedProxies([]string{"1.2.3.4"})
	assert.Len(t, trustedProxyCIDRs, loopbackEntries+1)

	// IPv6 bare IP (duplicate of loopback ::1 is allowed — just counts twice)
	SetTrustedProxies([]string{"::1"})
	assert.Len(t, trustedProxyCIDRs, loopbackEntries+1)

	// Invalid CIDR should be skipped
	SetTrustedProxies([]string{"invalid", "10.0.0.0/8"})
	assert.Len(t, trustedProxyCIDRs, loopbackEntries+1)

	// Empty list collapses to loopback-only (fail-closed baseline).
	SetTrustedProxies(nil)
	assert.Len(t, trustedProxyCIDRs, loopbackEntries)
}

func TestGetUserAgent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("User-Agent", "FalcoBot/1.0")
	assert.Equal(t, "FalcoBot/1.0", GetUserAgent(req))
}

func TestGetUserAgent_Empty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	assert.Equal(t, "", GetUserAgent(req))
}

func TestNewHTTPClient(t *testing.T) {
	client := NewHTTPClient(30 * time.Second)
	require.NotNil(t, client)
	assert.Equal(t, 30*time.Second, client.Timeout)
}

func TestNewSafeHTTPClient(t *testing.T) {
	client := NewSafeHTTPClient(30 * time.Second)
	require.NotNil(t, client)
	assert.Equal(t, 30*time.Second, client.Timeout)
	assert.NotNil(t, client.CheckRedirect)
}

func TestIsPrivateOrReservedIP(t *testing.T) {
	tests := []struct {
		ip       string
		expected bool
	}{
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"127.0.0.1", true},
		{"169.254.169.254", true}, // AWS metadata
		{"0.0.0.0", true},
		{"8.8.8.8", false},
		{"203.0.113.50", false},
		{"1.1.1.1", false},
		{"::1", true}, // IPv6 loopback
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			require.NotNil(t, ip)
			assert.Equal(t, tt.expected, isPrivateOrReservedIP(ip))
		})
	}
}

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name     string
		err      string
		expected bool
	}{
		{"server error", "server error: status 503", true},
		{"download failure", "failed to download: connection reset", true},
		{"read failure", "failed to read response: unexpected EOF", true},
		{"invalid content type", "invalid content type: text/html", false},
		{"file too large", "file too large: exceeds 10485760 bytes", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &testError{msg: tt.err}
			assert.Equal(t, tt.expected, isTransientError(err))
		})
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func BenchmarkGetClientIP_RemoteAddr(b *testing.B) {
	oldProxies := trustedProxyCIDRs
	trustedProxyCIDRs = nil
	defer func() { trustedProxyCIDRs = oldProxies }()

	req := &http.Request{RemoteAddr: "203.0.113.50:12345", Header: http.Header{}}
	b.ResetTimer()
	for range b.N {
		GetClientIP(req)
	}
}

func BenchmarkGetClientIP_WithXFF(b *testing.B) {
	oldProxies := trustedProxyCIDRs
	trustedProxyCIDRs = nil
	defer func() { trustedProxyCIDRs = oldProxies }()

	req := &http.Request{
		RemoteAddr: "10.0.0.1:1234",
		Header:     http.Header{"X-Forwarded-For": []string{"203.0.113.50"}},
	}
	b.ResetTimer()
	for range b.N {
		GetClientIP(req)
	}
}

func BenchmarkGetClientIP_TrustedProxy(b *testing.B) {
	oldProxies := trustedProxyCIDRs
	SetTrustedProxies([]string{"10.0.0.0/8"})
	defer func() { trustedProxyCIDRs = oldProxies }()

	req := &http.Request{
		RemoteAddr: "10.0.0.1:1234",
		Header:     http.Header{"X-Forwarded-For": []string{"203.0.113.50"}},
	}
	b.ResetTimer()
	for range b.N {
		GetClientIP(req)
	}
}

func BenchmarkIsPrivateOrReservedIP(b *testing.B) {
	ip := net.ParseIP("10.0.0.1")
	b.ResetTimer()
	for range b.N {
		isPrivateOrReservedIP(ip)
	}
}

func BenchmarkIsPrivateOrReservedIP_Public(b *testing.B) {
	ip := net.ParseIP("8.8.8.8")
	b.ResetTimer()
	for range b.N {
		isPrivateOrReservedIP(ip)
	}
}
