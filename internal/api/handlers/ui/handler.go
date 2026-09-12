// Package ui serves falco's admin panel.
//
// The panel authenticates on its own rather than sitting behind the API-key
// middleware, because a browser cannot attach a header to a navigation. What it
// does NOT do is grant anything of its own: a request is resolved to a scope,
// the scope is published into the request context, and mutating actions are
// then executed by the real API handlers. The panel can never do something the
// API would refuse.
package ui

import (
	"crypto/subtle"
	"net/http"
	"sort"
	"time"

	"github.com/a-h/templ"
	"github.com/birdple/falco/internal/api/handlers"
	views "github.com/birdple/falco/internal/api/views/templ"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
	"github.com/birdple/falco/internal/version"
)

// Handler serves the panel.
type Handler struct {
	cfg      *config.Config
	registry *storage.Registry
	// api is the real API handler. Panel actions are delegated to it with the
	// session's scope injected, so upload/delete go through exactly the same
	// validation, ownership checks and cache invalidation as any API client.
	api       *handlers.Handler
	processor processor.ImageProcessor
	sessions  *SessionStore
	started   time.Time

	// keys is the flattened scoped-key table, computed once at startup.
	keys map[string]config.KeyScope
}

// NewHandler builds the panel handler.
func NewHandler(
	cfg *config.Config,
	registry *storage.Registry,
	api *handlers.Handler,
	imageProcessor processor.ImageProcessor,
) *Handler {
	return &Handler{
		cfg:       cfg,
		registry:  registry,
		api:       api,
		processor: imageProcessor,
		sessions:  NewSessionStore(sessionTTL),
		started:   time.Now(),
		keys:      cfg.CollectAllKeys(),
	}
}

// Close releases the panel's background work.
func (h *Handler) Close() {
	h.sessions.Close()
}

// enabled reports whether the panel can be served at all.
//
// The panel is an authenticated surface over every bucket. With no key
// configured there is nothing to authenticate against, and the old code
// responded to that by inventing an admin scope on the spot — so an operator
// who simply had not set API_KEY got a wide-open panel. A feature that is not
// configured refuses to run; it does not open.
func (h *Handler) enabled() bool {
	return h.cfg.Security.APIKey != "" || len(h.keys) > 0
}

// disabledReason explains, in the UI, why the panel is not serving.
func (h *Handler) disabledReason() string {
	return "The admin panel is disabled because no API key is configured. " +
		"Set API_KEY (or define scoped keys) and restart falco."
}

// resolveKey validates a key and returns the scope it grants.
func (h *Handler) resolveKey(key string) *Scope {
	if key == "" {
		return nil
	}

	if h.cfg.Security.APIKey != "" &&
		subtle.ConstantTimeCompare([]byte(key), []byte(h.cfg.Security.APIKey)) == 1 {
		return &Scope{IsAdmin: true, KeyName: "admin"}
	}

	var matched *Scope
	for keyVal, scope := range h.keys {
		// Every entry is compared even after a match: bailing out early would
		// make the loop's duration depend on where the key sits in the table.
		if subtle.ConstantTimeCompare([]byte(key), []byte(keyVal)) == 1 {
			matched = &Scope{KeyName: scope.Name, Buckets: scope.Buckets}
		}
	}
	return matched
}

// accessibleBuckets lists the buckets a scope may see, sorted, with the
// internal "default" alias filtered out — it duplicates a bucket that is
// already listed under its real name.
func (h *Handler) accessibleBuckets(scope *Scope) []string {
	var out []string
	for _, name := range h.registry.Names() {
		if name == registryDefaultAlias {
			continue
		}
		if scope.CanAccessBucket(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// resolveBucket picks the bucket to operate on and enforces the scope.
//
// It returns an error rather than silently substituting an allowed bucket. The
// old dashboard swapped in the first accessible bucket, and the HTMX partial
// did not check at all — so a key scoped to one bucket could read another
// bucket's full listing just by changing the query string.
func (h *Handler) resolveBucket(scope *Scope, requested string) (string, error) {
	name := requested
	if name == "" {
		name = h.cfg.GetDefaultBucketName()
	}
	if !scope.CanAccessBucket(name) {
		return "", errForbiddenBucket
	}
	if _, err := h.registry.Get(name); err != nil {
		return "", errUnknownBucket
	}
	return name, nil
}

// bucketType reports a bucket's backend type ("jay", "s3", "r2",
// "filesystem"), or "" when it is not configured.
func (h *Handler) bucketType(name string) string {
	cfg, err := h.cfg.GetBucketConfig(name)
	if err != nil {
		return ""
	}
	return cfg.Type
}

// render writes a templ component.
//
// A failed render is logged rather than discarded: the four call sites in the
// old panel wrote `_ = ...Render(...)`, so a render that died halfway left a
// 200 with a truncated body and no trace anywhere.
func (h *Handler) render(w http.ResponseWriter, r *http.Request, name string, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		logger.Error().Err(err).Str("view", name).Msg("Failed to render panel view")
	}
}

// pageData fills in the shell every page shares.
//
// The theme is resolved here, server-side, so it is already correct in the
// first byte of HTML.
func (h *Handler) pageData(r *http.Request, sess *Session, title, current string, buckets []views.BucketItem) views.PageData {
	page := views.PageData{
		Title:       title,
		Theme:       themeFrom(r),
		CurrentPage: current,
		Buckets:     buckets,
		Version:     version.Version,
	}
	if sess != nil {
		page.CSRFToken = sess.CSRFToken
		page.KeyName = sess.Scope.KeyName
		page.IsAdmin = sess.Scope.IsAdmin
	}
	return page
}

// registryDefaultAlias is the extra key the registry files its default backend
// under, alongside the backend's real name.
const registryDefaultAlias = "default"

// panelUptime is used by the ops screen.
func (h *Handler) panelUptime(start time.Time) string {
	return time.Since(start).Round(time.Second).String()
}
