package middleware

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/pkg/logger"
)

type contextKey string

const scopeContextKey contextKey = "api_scope"

// APIScope holds the access restrictions for the current request.
// A nil scope (or IsAdmin=true) means unrestricted access.
type APIScope struct {
	IsAdmin bool
	KeyName string
	Buckets map[string]bool // allowed bucket names
}

// CanAccessBucket returns true if the scope allows the given bucket name.
func (s *APIScope) CanAccessBucket(bucket string) bool {
	if s == nil || s.IsAdmin {
		return true
	}
	if len(s.Buckets) == 0 {
		return true
	}
	return s.Buckets[bucket]
}

// GetScope retrieves the APIScope from the request context.
// Returns nil if no scope is set (unauthenticated or no scoped keys configured).
func GetScope(ctx context.Context) *APIScope {
	scope, _ := ctx.Value(scopeContextKey).(*APIScope)
	return scope
}

// WithScope returns a new context with the given APIScope attached. Handlers
// that run outside the middleware chain (e.g. delivery when HMAC is not
// required) use this to publish the scope so downstream helpers like
// getStorageBackendScoped can enforce it.
func WithScope(ctx context.Context, scope *APIScope) context.Context {
	return context.WithValue(ctx, scopeContextKey, scope)
}

// ScopedAPIKeyAuth provides API key authentication with optional scoped access.
// It checks the provided key against the admin key and all collected scoped keys
// (from bucket-level keys, group keys, and subgroup keys).
type ScopedAPIKeyAuth struct {
	adminKey           string
	scopedKeys         map[string]*APIScope // key value -> scope
	exemptPaths        map[string]bool
	exemptPathPrefixes []string
	// exemptDeliveryWhenHMACPublic is true only when HMAC_REQUIRED=true. In
	// that mode the HMAC signature itself authorizes access to a specific
	// path, so the delivery prefix "/api/v1/images/" can bypass API-key
	// auth (browsers can't carry keys). When HMAC is NOT required the
	// exemption is dropped and delivery must authenticate like any other
	// route.
	exemptDeliveryWhenHMACPublic bool
}

// NewScopedAPIKeyAuth creates a new scoped API key auth middleware.
// It uses Config.CollectAllKeys() to resolve all bucket/group/subgroup keys
// into a flat key -> scope map.
//
// By default the "/api/v1/images/" prefix is exempt — the delivery route is
// gated by HMAC, which authorizes the specific URL rather than the caller.
// Callers that wish to protect delivery with API-key + scope (because HMAC
// is not required in their deployment) must call SetDeliveryExempt(false).
func NewScopedAPIKeyAuth(adminKey string, cfg *config.Config) *ScopedAPIKeyAuth {
	allKeys := cfg.CollectAllKeys()

	skMap := make(map[string]*APIScope, len(allKeys))
	for keyVal, scope := range allKeys {
		buckets := make(map[string]bool, len(scope.Buckets))
		for b := range scope.Buckets {
			buckets[strings.TrimSpace(b)] = true
		}
		skMap[keyVal] = &APIScope{
			KeyName: scope.Name,
			Buckets: buckets,
		}
	}

	return &ScopedAPIKeyAuth{
		adminKey:   adminKey,
		scopedKeys: skMap,
		exemptPaths: map[string]bool{
			"/health":    true,
			"/":          true,
			"/dashboard": true,
		},
		exemptPathPrefixes: []string{
			"/api/v1/images/",
			"/ui/",
			"/static/",
		},
		exemptDeliveryWhenHMACPublic: true,
	}
}

// SetDeliveryExempt toggles whether the "/api/v1/images/" prefix bypasses
// API-key + scope checks. True is the default (HMAC gates delivery). Set to
// false when HMAC_REQUIRED=false so scope still applies to delivery.
func (a *ScopedAPIKeyAuth) SetDeliveryExempt(exempt bool) {
	a.exemptDeliveryWhenHMACPublic = exempt
	// Rebuild the prefix list to match.
	filtered := make([]string, 0, len(a.exemptPathPrefixes))
	seen := false
	for _, p := range a.exemptPathPrefixes {
		if p == "/api/v1/images/" {
			seen = true
			if exempt {
				filtered = append(filtered, p)
			}
			continue
		}
		filtered = append(filtered, p)
	}
	if exempt && !seen {
		filtered = append(filtered, "/api/v1/images/")
	}
	a.exemptPathPrefixes = filtered
}

// AuthenticateRequest resolves the scope for a request without being an HTTP
// middleware. Returns the resolved scope (admin or scoped) or nil when the
// provided key matches nothing. This is used by handlers that serve routes
// not wrapped by Handler (e.g. delivery when HMAC is not required).
func (a *ScopedAPIKeyAuth) AuthenticateRequest(r *http.Request) (*APIScope, bool) {
	providedKey := r.Header.Get("X-API-Key")
	if providedKey == "" {
		providedKey = r.Header.Get("Authorization")
		providedKey, _ = strings.CutPrefix(providedKey, "Bearer ")
	}
	if providedKey == "" {
		return nil, false
	}

	if a.adminKey != "" && subtle.ConstantTimeCompare([]byte(providedKey), []byte(a.adminKey)) == 1 {
		return &APIScope{IsAdmin: true}, true
	}

	var matched *APIScope
	for key, scope := range a.scopedKeys {
		if subtle.ConstantTimeCompare([]byte(providedKey), []byte(key)) == 1 {
			matched = scope
		}
	}
	if matched == nil {
		return nil, false
	}
	return matched, true
}

// HasScopedKeys returns true if there are any scoped keys configured.
func (a *ScopedAPIKeyAuth) HasScopedKeys() bool {
	return len(a.scopedKeys) > 0
}

// Handler returns the middleware handler.
func (a *ScopedAPIKeyAuth) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check exempt paths
		if a.exemptPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		for _, prefix := range a.exemptPathPrefixes {
			if strings.HasPrefix(r.URL.Path, prefix) {
				next.ServeHTTP(w, r)
				return
			}
		}

		// Extract API key
		providedKey := r.Header.Get("X-API-Key")
		if providedKey == "" {
			providedKey = r.Header.Get("Authorization")
			providedKey, _ = strings.CutPrefix(providedKey, "Bearer ")
		}

		if providedKey == "" {
			logger.Warn().
				Str("ip", httputil.GetClientIP(r)).
				Str("path", r.URL.Path).
				Msg("Missing API key")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Check admin key first (constant-time comparison)
		if a.adminKey != "" && subtle.ConstantTimeCompare([]byte(providedKey), []byte(a.adminKey)) == 1 {
			ctx := context.WithValue(r.Context(), scopeContextKey, &APIScope{IsAdmin: true})
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Check scoped keys (iterate all to maintain constant-time-ish behavior)
		var matchedScope *APIScope
		for key, scope := range a.scopedKeys {
			if subtle.ConstantTimeCompare([]byte(providedKey), []byte(key)) == 1 {
				matchedScope = scope
			}
		}

		if matchedScope != nil {
			logger.Debug().
				Str("key_name", matchedScope.KeyName).
				Str("path", r.URL.Path).
				Msg("Scoped API key authenticated")
			ctx := context.WithValue(r.Context(), scopeContextKey, matchedScope)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// No match
		logger.Warn().
			Str("ip", httputil.GetClientIP(r)).
			Str("path", r.URL.Path).
			Msg("Invalid API key")
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}
