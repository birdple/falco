package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/birdple/falco/internal/pkg/httputil"
)

// RealIP rewrites r.RemoteAddr based on X-Forwarded-For / X-Real-IP headers,
// but only when the direct peer is in the httputil trusted-proxy allowlist.
//
// chi's middleware.RealIP does not fit here: it trusts those headers
// unconditionally, so any client could spoof X-Forwarded-For and defeat per-IP
// rate limiting.
//
//   - Untrusted peer: RemoteAddr is left untouched.
//   - Trusted peer: the leftmost valid IP from X-Forwarded-For wins, or
//     X-Real-IP if it is missing. The source port is preserved so that
//     net.SplitHostPort keeps working downstream.
//
// IsTrustedProxy and the fail-closed default (loopback only) live in httputil.
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

	if !httputil.IsTrustedProxy(remoteHost) {
		return ""
	}

	forwarded := extractForwardedIP(r)
	if forwarded == "" {
		return ""
	}

	// Preserve the original port so downstream consumers of RemoteAddr (which
	// assume host:port) keep parsing cleanly. If we had no port we still
	// return a bare host — net.SplitHostPort will then fall back to treating
	// the whole string as the host, matching existing behavior.
	if remotePort != "" {
		return net.JoinHostPort(forwarded, remotePort)
	}
	return forwarded
}

// extractForwardedIP returns the leftmost valid IP from X-Forwarded-For, or
// X-Real-IP if XFF is absent/empty. Invalid entries are rejected so that a
// malformed header cannot corrupt RemoteAddr.
func extractForwardedIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for candidate := range strings.SplitSeq(xff, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate != "" && net.ParseIP(candidate) != nil {
				return candidate
			}
		}
	}

	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if net.ParseIP(xri) != nil {
			return xri
		}
	}

	return ""
}
