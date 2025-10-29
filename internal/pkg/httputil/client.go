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
// It checks X-Forwarded-For, X-Real-IP headers and falls back to RemoteAddr
func GetClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (for proxies/load balancers)
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		// Take the first IP in the chain
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			ip := strings.TrimSpace(ips[0])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}

	// Check X-Real-IP header
	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		if net.ParseIP(xri) != nil {
			return xri
		}
	}

	// Fall back to RemoteAddr
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

// IsSecure returns true if the request was made over HTTPS
func IsSecure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
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

	// Get content type
	contentType := resp.Header.Get("Content-Type")

	// Validate content type is an image
	if !strings.HasPrefix(contentType, "image/") {
		return nil, "", fmt.Errorf("invalid content type: %s (expected image/*)", contentType)
	}

	// Check content length if available
	if resp.ContentLength > maxSize {
		return nil, "", fmt.Errorf("file too large: %d bytes (max %d)", resp.ContentLength, maxSize)
	}

	// Use LimitReader to prevent reading more than maxSize
	limitedReader := io.LimitReader(resp.Body, maxSize+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response: %w", err)
	}

	// Check if we read more than maxSize (indicating file is too large)
	if int64(len(data)) > maxSize {
		return nil, "", fmt.Errorf("file too large: exceeds %d bytes", maxSize)
	}

	return data, contentType, nil
}
