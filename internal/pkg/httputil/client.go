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

// GetClientIP extracts the client IP address from the request
func GetClientIP(r *http.Request) string {
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

	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return ip
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

// DownloadURL downloads a file from a URL with timeout and size limits
func DownloadURL(ctx context.Context, client *http.Client, url string, maxSize int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

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
