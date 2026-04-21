package httputil

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// defaultTrustedLoopbacks are the CIDRs always trusted as proxies when no
// explicit allowlist is configured. Loopback is safe because only processes on
// the same host can originate packets with these source addresses.
var defaultTrustedLoopbacks = []string{"127.0.0.0/8", "::1/128"}

// trustedProxyCIDRs holds the parsed trusted proxy networks.
// Set via SetTrustedProxies at startup. When empty, only loopback addresses
// are trusted to set X-Forwarded-For / X-Real-IP — this fail-closed default
// prevents spoofed forwarded headers from bypassing per-IP rate limiting.
var trustedProxyCIDRs = parseCIDRs(defaultTrustedLoopbacks)

// parseCIDRs converts a list of CIDRs or bare IPs into *net.IPNet values.
// Bare IPs are promoted to /32 (IPv4) or /128 (IPv6). Invalid entries are
// silently skipped — the caller is expected to have already validated input.
func parseCIDRs(cidrs []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		if !strings.Contains(cidr, "/") {
			// Bare IP — make it a /32 or /128
			if strings.Contains(cidr, ":") {
				cidr += "/128"
			} else {
				cidr += "/32"
			}
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		out = append(out, network)
	}
	return out
}

// SetTrustedProxies parses and stores the list of trusted proxy CIDRs/IPs.
// Call once at startup. Loopback (127.0.0.0/8, ::1/128) is always included so
// that local health checks and dev setups keep working. When the provided
// list is empty, the set collapses to loopback-only (fail-closed).
func SetTrustedProxies(cidrs []string) {
	combined := make([]string, 0, len(cidrs)+len(defaultTrustedLoopbacks))
	combined = append(combined, defaultTrustedLoopbacks...)
	combined = append(combined, cidrs...)
	trustedProxyCIDRs = parseCIDRs(combined)
}

// isTrustedProxy checks if the remote address is from a trusted proxy.
// Fail-closed: if the allowlist is empty (misconfiguration) no forwarded
// header is trusted. See package doc on trustedProxyCIDRs.
func isTrustedProxy(remoteIP string) bool {
	if len(trustedProxyCIDRs) == 0 {
		return false
	}
	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return false
	}
	for _, network := range trustedProxyCIDRs {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// IsTrustedProxy is the exported form of isTrustedProxy, used by the
// Falco-owned RealIP middleware (internal/api/middleware/realip.go) to gate
// X-Forwarded-For / X-Real-IP rewriting.
func IsTrustedProxy(remoteIP string) bool {
	return isTrustedProxy(remoteIP)
}

// GetClientIP extracts the client IP address from the request.
// Forwarded headers are only trusted when the direct connection is from a trusted proxy.
func GetClientIP(r *http.Request) string {
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}

	if isTrustedProxy(remoteIP) {
		xff := r.Header.Get("X-Forwarded-For")
		if xff != "" {
			ips := strings.Split(xff, ",")
			if len(ips) > 0 {
				ip := strings.TrimSpace(ips[0])
				if net.ParseIP(ip) != nil {
					return ip
				}
			}
		}

		xri := r.Header.Get("X-Real-IP")
		if xri != "" {
			if net.ParseIP(xri) != nil {
				return xri
			}
		}
	}

	return remoteIP
}

// GetUserAgent returns the User-Agent header from the request
func GetUserAgent(r *http.Request) string {
	return r.Header.Get("User-Agent")
}

// NewHTTPClient creates a new HTTP client with sensible timeouts
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

// NewSafeHTTPClient creates an HTTP client that blocks requests to private/reserved IPs (SSRF protection).
func NewSafeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, fmt.Errorf("invalid address: %w", err)
				}

				ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, fmt.Errorf("DNS lookup failed: %w", err)
				}

				for _, ip := range ips {
					if isPrivateOrReservedIP(ip.IP) {
						return nil, fmt.Errorf("resolved to private/reserved IP: %s", ip.IP)
					}
				}

				// Connect to the first valid IP
				return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
			},
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

// isPrivateOrReservedIP checks if an IP is in a private or reserved range
func isPrivateOrReservedIP(ip net.IP) bool {
	privateRanges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
		"169.254.0.0/16", // Link-local / AWS metadata
		"0.0.0.0/8",
		"100.64.0.0/10",  // Carrier-grade NAT
		"192.0.0.0/24",
		"198.18.0.0/15",  // Benchmarking
		"fc00::/7",       // IPv6 unique local
		"fe80::/10",      // IPv6 link-local
		"::1/128",        // IPv6 loopback
	}

	for _, cidr := range privateRanges {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(ip) {
			return true
		}
	}

	return false
}

// DownloadURL downloads a file from a URL with timeout, size limits, and retry.
// Retries up to 3 times with exponential backoff on transient errors.
func DownloadURL(ctx context.Context, client *http.Client, url string, maxSize int64) ([]byte, string, error) {
	const maxRetries = 3
	var lastErr error

	for attempt := range maxRetries {
		if attempt > 0 {
			// Exponential backoff with jitter: 500ms, 1s, 2s base
			backoff := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(backoff):
			}
		}

		data, contentType, err := downloadOnce(ctx, client, url, maxSize)
		if err == nil {
			return data, contentType, nil
		}

		// Don't retry on non-transient errors (validation failures, too large, etc.)
		if !isTransientError(err) {
			return nil, "", err
		}
		lastErr = err
	}

	return nil, "", fmt.Errorf("download failed after %d attempts: %w", maxRetries, lastErr)
}

// downloadOnce performs a single download attempt
func downloadOnce(ctx context.Context, client *http.Client, url string, maxSize int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return nil, "", fmt.Errorf("server error: status %d", resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")

	if !strings.HasPrefix(contentType, "image/") {
		return nil, "", fmt.Errorf("invalid content type: %s (expected image/*)", contentType)
	}

	if resp.ContentLength > maxSize {
		return nil, "", fmt.Errorf("file too large: %d bytes (max %d)", resp.ContentLength, maxSize)
	}

	limitedReader := io.LimitReader(resp.Body, maxSize+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response: %w", err)
	}

	if int64(len(data)) > maxSize {
		return nil, "", fmt.Errorf("file too large: exceeds %d bytes", maxSize)
	}

	return data, contentType, nil
}

// isTransientError checks if an error is likely transient and worth retrying
func isTransientError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "server error:") ||
		strings.Contains(msg, "failed to download:") ||
		strings.Contains(msg, "failed to read response:")
}
