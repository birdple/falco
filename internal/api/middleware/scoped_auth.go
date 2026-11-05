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
	// An empty set grants nothing. The validator refuses keys that resolve to
	// no bucket, and this keeps the check fail-closed even if one slips past.
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
	adminKey   string
	scopedKeys map[string]*APIScope // key value -> scope
}

// NewScopedAPIKeyAuth creates a new scoped API key auth middleware.
// It uses Config.CollectAllKeys() to resolve all bucket/group/subgroup keys
// into a flat key -> scope map.
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

	return &ScopedAPIKeyAuth{adminKey: adminKey, scopedKeys: skMap}
}

// HasScopedKeys returns true if there are any scoped keys configured.
func (a *ScopedAPIKeyAuth) HasScopedKeys() bool {
	return len(a.scopedKeys) > 0
}

// resolve returns the scope a key grants, or nil when it matches nothing.
// Every scoped key is compared, with no early exit, so the time taken does not
// reveal which one matched.
func (a *ScopedAPIKeyAuth) resolve(providedKey string) *APIScope {
	if providedKey == "" {
		return nil
	}
	if a.adminKey != "" && subtle.ConstantTimeCompare([]byte(providedKey), []byte(a.adminKey)) == 1 {
		return &APIScope{IsAdmin: true}
	}
	var matched *APIScope
	for key, scope := range a.scopedKeys {
		if subtle.ConstantTimeCompare([]byte(providedKey), []byte(key)) == 1 {
			matched = scope
		}
	}
	return matched
}

// Handler returns the middleware handler. It has no exempt paths: it is only
// ever mounted on groups that are protected in full.
func (a *ScopedAPIKeyAuth) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providedKey := providedAPIKey(r)
		scope := a.resolve(providedKey)
		if scope == nil {
			msg := "Invalid API key"
			if providedKey == "" {
				msg = "Missing API key"
			}
			logger.Warn().
				Str("ip", httputil.GetClientIP(r)).
				Str("path", r.URL.Path).
				Msg(msg)
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "API key required")
			return
		}

		if !scope.IsAdmin {
			logger.Debug().
				Str("key_name", scope.KeyName).
				Str("path", r.URL.Path).
				Msg("Scoped API key authenticated")
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), scopeContextKey, scope)))
	})
}
