package ui

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"strings"

	apimw "github.com/birdple/falco/internal/api/middleware"
	views "github.com/birdple/falco/internal/api/views/templ"
	"github.com/birdple/falco/internal/jsonx"
	"github.com/birdple/falco/internal/pkg/httputil"
	"github.com/birdple/falco/internal/pkg/logger"
)

var (
	errForbiddenBucket = errors.New("this key cannot access that bucket")
	errUnknownBucket   = errors.New("no such bucket")
)

// sessionFrom returns the live session for a request, or nil.
func (h *Handler) sessionFrom(r *http.Request) *Session {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	return h.sessions.Get(c.Value)
}

// themeFrom reads the remembered theme so the server can render it into the
// first byte of HTML, with no flash and no inline script.
func themeFrom(r *http.Request) string {
	c, err := r.Cookie(themeCookie)
	if err != nil || c.Value != "light" {
		return "dark"
	}
	return "light"
}

// RequireSession guards every authenticated panel route.
//
// This replaces the per-handler guards the panel used to have. Those drifted:
// the dashboard checked the bucket scope and the HTMX partial that rendered the
// very same data did not.
func (h *Handler) RequireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.enabled() {
			h.deny(w, r, http.StatusForbidden, h.disabledReason())
			return
		}

		sess := h.sessionFrom(r)
		if sess == nil {
			h.deny(w, r, http.StatusUnauthorized, "Your session has expired. Sign in again.")
			return
		}

		// Mutations carry a CSRF token. The session cookie is SameSite=Lax, so
		// a cross-site POST would otherwise arrive authenticated.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !checkCSRF(r, sess) {
			h.deny(w, r, http.StatusForbidden, "Invalid or missing CSRF token. Reload the page and try again.")
			return
		}

		// Publish the caller's authority so the API handlers this panel
		// delegates to enforce exactly what they would for an API client.
		ctx := apimw.WithScope(r.Context(), sess.Scope.APIScope())
		next(w, r.WithContext(ctx))
	}
}

// deny answers an unauthorised panel request in the shape the caller expects:
// a redirect for a navigation, an error fragment for HTMX, JSON otherwise.
func (h *Handler) deny(w http.ResponseWriter, r *http.Request, status int, message string) {
	switch {
	case r.Header.Get("HX-Request") == "true":
		// HX-Redirect makes the browser navigate; a plain 401 body would be
		// swapped into the page as if it were content.
		if status == http.StatusUnauthorized {
			w.Header().Set("HX-Redirect", "/")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = views.InlineError(message).Render(r.Context(), w)
	case wantsHTML(r):
		if status == http.StatusUnauthorized {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = views.LoginPage(views.LoginData{Theme: themeFrom(r), Disabled: !h.enabled(), Error: message}).Render(r.Context(), w)
	default:
		writeJSON(w, status, map[string]any{"ok": false, "error": message})
	}
}

// wantsHTML reports whether the caller is a navigation rather than a
// programmatic call, so a denial can be a redirect instead of a JSON body.
//
// The test is what the caller ASKED FOR, not what it merely tolerates: a
// browser navigating sends "text/html" in Accept, while fetch() from the panel
// sends "application/json". Treating the wildcard "*/*" as HTML matters — that
// is what curl and most HTTP clients send by default, and answering them with
// a bare 401 body made a plain `curl /dashboard` look broken.
func wantsHTML(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return !strings.Contains(r.Header.Get("Accept"), "application/json")
}

// Login renders the sign-in page.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	data := views.LoginData{Theme: themeFrom(r)}

	if !h.enabled() {
		data.Disabled = true
		data.Error = h.disabledReason()
		w.WriteHeader(http.StatusServiceUnavailable)
		h.render(w, r, "login", views.LoginPage(data))
		return
	}

	if sess := h.sessionFrom(r); sess != nil {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}

	h.render(w, r, "login", views.LoginPage(data))
}

// AuthPost exchanges an API key for a session.
func (h *Handler) AuthPost(w http.ResponseWriter, r *http.Request) {
	if !h.enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": h.disabledReason()})
		return
	}

	var body struct {
		Key string `json:"key"`
	}
	if err := jsonv2.UnmarshalRead(r.Body, &body, jsonx.Strict); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid request"})
		return
	}

	scope := h.resolveKey(body.Key)
	if scope == nil {
		logger.Warn().Str("ip", httputil.GetClientIP(r)).Msg("Panel sign-in rejected")
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Invalid API key"})
		return
	}

	sess, err := h.sessions.Create(body.Key, scope)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to create panel session")
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Could not start a session"})
		return
	}
	setSessionCookie(w, r, sess)

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"name":  scope.KeyName,
		"admin": scope.IsAdmin,
	})
}

// LogoutPost ends the session. It must happen server-side: the cookie is
// HttpOnly and the session itself lives in the store, so clearing anything in
// the browser alone would leave it usable.
func (h *Handler) LogoutPost(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		h.sessions.Delete(c.Value)
	}
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ThemePost remembers the light/dark choice server-side.
//
// The theme is applied during server rendering, which is what removes the
// first-paint flash: the old panel did it from an inline script that the
// panel's own CSP blocked.
func (h *Handler) ThemePost(w http.ResponseWriter, r *http.Request) {
	theme := r.URL.Query().Get("theme")
	if theme != "light" {
		theme = "dark"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     themeCookie,
		Value:    theme,
		Path:     "/",
		HttpOnly: false, // read by the client too, to avoid a round-trip on toggle
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   365 * 24 * 60 * 60,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "theme": theme})
}

// writeJSON writes a JSON body, marshalling before touching the ResponseWriter
// so a marshal failure can still become a 500 instead of a truncated 200.
func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := jsonv2.Marshal(v)
	if err != nil {
		logger.Error().Err(err).Int("status_code", status).Msg("Failed to marshal panel JSON")
		http.Error(w, `{"ok":false,"error":"Failed to encode response"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(data); err != nil {
		logger.Error().Err(err).Msg("Failed to write panel JSON")
	}
}
