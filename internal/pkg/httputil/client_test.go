package httputil

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
		{"::1", true},                   // IPv6 loopback
		{"::", true},                    // IPv6 unspecified
		{"::ffff:127.0.0.1", true},      // IPv4-mapped loopback
		{"64:ff9b::7f00:1", true},       // NAT64-embedded 127.0.0.1
		{"2002:7f00:1::", true},         // 6to4-embedded 127.0.0.1
		{"224.0.0.1", true},             // Multicast
		{"255.255.255.255", true},       // Reserved
		{"ff02::1", true},               // IPv6 multicast
		{"fec0::1", true},               // IPv6 site-local
		{"2001:4860:4860::8888", false}, // Public IPv6 (Google DNS)
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

// redirectRequest builds the request Go would hand to CheckRedirect for a hop
// to target, carrying ctx the way a real redirect inherits it.
func redirectRequest(t *testing.T, ctx context.Context, target string) *http.Request {
	t.Helper()
	u, err := url.Parse(target)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	require.NoError(t, err)
	return req
}

func TestCheckRedirect_NoPolicyRefuses(t *testing.T) {
	// A fetch that never declared a policy follows nothing: fail-closed is
	// the default, not "unrestricted".
	req := redirectRequest(t, context.Background(), "https://example.com/img.png")
	assert.Error(t, checkRedirect(req, nil))
}

func TestCheckRedirect_HostAllowlist(t *testing.T) {
	allowed := map[string]struct{}{"cf.geekdo-images.com": {}}
	ctx := WithHostAllowlist(context.Background(), allowed)

	assert.NoError(t, checkRedirect(redirectRequest(t, ctx, "https://cf.geekdo-images.com/a.png"), nil))
	// Case is normalised on both sides.
	assert.NoError(t, checkRedirect(redirectRequest(t, ctx, "https://CF.Geekdo-Images.com/a.png"), nil))
	// The whole point: an allowed host cannot bounce the fetch elsewhere.
	assert.Error(t, checkRedirect(redirectRequest(t, ctx, "https://evil.example/a.png"), nil))
	// Nor onto a subdomain that is not itself listed.
	assert.Error(t, checkRedirect(redirectRequest(t, ctx, "https://x.cf.geekdo-images.com/a.png"), nil))
}

func TestCheckRedirect_AnyPublicHost(t *testing.T) {
	ctx := WithAnyPublicHost(context.Background())
	assert.NoError(t, checkRedirect(redirectRequest(t, ctx, "https://anything.example/a.png"), nil))
}

func TestCheckRedirect_UnsupportedScheme(t *testing.T) {
	ctx := WithAnyPublicHost(context.Background())
	req := redirectRequest(t, ctx, "https://example.com/a.png")
	req.URL.Scheme = "file"
	assert.Error(t, checkRedirect(req, nil))
}

func TestCheckRedirect_TooManyHops(t *testing.T) {
	ctx := WithAnyPublicHost(context.Background())
	req := redirectRequest(t, ctx, "https://example.com/a.png")
	assert.NoError(t, checkRedirect(req, make([]*http.Request, maxRedirects-1)))
	assert.Error(t, checkRedirect(req, make([]*http.Request, maxRedirects)))
}

// TestCheckRedirect_EndToEnd asserts the network effect, not the predicate:
// that Go carries the request context across a redirect, so the policy set on
// the original fetch still governs the hop. The dialer is the stock one because
// httptest listens on loopback, which the safe client refuses by design — the
// piece under test here is CheckRedirect.
func TestCheckRedirect_EndToEnd(t *testing.T) {
	var upstreamHit bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHit = true
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("payload"))
	}))
	defer upstream.Close()

	// Same listener, different hostname: the hop is only ever stopped by the
	// policy, never by the connection failing, so a pass really means the
	// redirect was refused.
	upstreamAlias := strings.Replace(upstream.URL, "127.0.0.1", "localhost", 1)
	require.NotEqual(t, upstream.URL, upstreamAlias)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, upstreamAlias, http.StatusFound)
	}))
	defer redirector.Close()

	client := &http.Client{CheckRedirect: checkRedirect, Timeout: 5 * time.Second}
	redirectorHost, _, err := net.SplitHostPort(mustURL(t, redirector.URL).Host)
	require.NoError(t, err)

	// Only the redirector is allowed, so the hop to upstream must not happen.
	ctx := WithHostAllowlist(context.Background(), map[string]struct{}{redirectorHost: {}})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, redirector.URL, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.Error(t, err)
	if resp != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	assert.False(t, upstreamHit, "redirect target was fetched despite being outside the allowlist")

	// With the target allowed, the same chain completes.
	ctx = WithAnyPublicHost(context.Background())
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, redirector.URL, nil)
	require.NoError(t, err)
	resp, err = client.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, "payload", string(body))
	assert.True(t, upstreamHit)
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
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
