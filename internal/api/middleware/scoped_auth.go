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
	IsAdmin  bool
	KeyName  string
	Storages map[string]bool // allowed storage backend names
	Buckets  map[string]bool // allowed bucket names
}

// CanAccessStorage returns true if the scope allows the given storage name.
func (s *APIScope) CanAccessStorage(name string) bool {
	if s == nil || s.IsAdmin {
		return true
	}
	if len(s.Storages) == 0 {
		return true // no storage restriction, only bucket restriction
	}
	return s.Storages[name]
}

// CanAccessBucket returns true if the scope allows the given bucket name.
func (s *APIScope) CanAccessBucket(bucket string) bool {
	if s == nil || s.IsAdmin {
		return true
	}
	if len(s.Buckets) == 0 {
		return true // no bucket restriction, only storage restriction
	}
	return s.Buckets[bucket]
}

// GetScope retrieves the APIScope from the request context.
// Returns nil if no scope is set (unauthenticated or no scoped keys configured).
func GetScope(ctx context.Context) *APIScope {
	scope, _ := ctx.Value(scopeContextKey).(*APIScope)
	return scope
}

// ScopedAPIKeyAuth provides API key authentication with optional scoped access.
// It checks the provided key against the admin key and all configured scoped keys.
type ScopedAPIKeyAuth struct {
	adminKey           string
	scopedKeys         map[string]*APIScope // key value -> scope
	exemptPaths        map[string]bool
	exemptPathPrefixes []string
}

// NewScopedAPIKeyAuth creates a new scoped API key auth middleware.
func NewScopedAPIKeyAuth(adminKey string, scopedKeys []config.ScopedKeyConfig) *ScopedAPIKeyAuth {
	skMap := make(map[string]*APIScope, len(scopedKeys))
	for _, sk := range scopedKeys {
		storages := make(map[string]bool, len(sk.Storages))
		for _, s := range sk.Storages {
			storages[strings.TrimSpace(s)] = true
		}
		buckets := make(map[string]bool, len(sk.Buckets))
		for _, b := range sk.Buckets {
			buckets[strings.TrimSpace(b)] = true
		}
		skMap[sk.Key] = &APIScope{
			KeyName:  sk.Name,
			Storages: storages,
			Buckets:  buckets,
		}
	}

	return &ScopedAPIKeyAuth{
		adminKey:   adminKey,
		scopedKeys: skMap,
		exemptPaths: map[string]bool{
			"/health": true,
			"/":       true,
		},
		exemptPathPrefixes: []string{
			"/api/v1/images/",
		},
	}
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
