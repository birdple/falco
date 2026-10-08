package middleware

import (
	"net"
	"net/http"

	"github.com/birdple/falco/internal/pkg/httputil"
)

// RealIP rewrites r.RemoteAddr based on X-Forwarded-For / X-Real-IP headers,
// but only when the direct peer is in the httputil trusted-proxy allowlist.
//
// chi's middleware.RealIP does not fit here: it trusts those headers
// unconditionally, so any client could spoof X-Forwarded-For and defeat per-IP
// rate limiting.
//
// The address is resolved by httputil.ClientIP — the rightmost entry that is
// not itself a trusted proxy — so the logger, the rate limiter and every later
// IsTrustedProxy check see the same client. The source port is preserved so
// that net.SplitHostPort keeps working downstream.
func RealIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rewritten := realIPFor(r); rewritten != "" {
			r.RemoteAddr = rewritten
		}
		next.ServeHTTP(w, r)
	})
}

// realIPFor returns the rewritten RemoteAddr value, or "" if no change is
// warranted (untrusted peer, no forwarded headers, or header values invalid).
func realIPFor(r *http.Request) string {
	remoteHost, remotePort, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr without a port (rare, but httptest.NewRequest does this)
		remoteHost = r.RemoteAddr
		remotePort = ""
	}

	client := httputil.ClientIP(remoteHost, r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Real-IP"))
	if client == remoteHost {
		return ""
	}

	if remotePort != "" {
		return net.JoinHostPort(client, remotePort)
	}
	return client
}
