package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/pkg/httputil"
)

const (
	// sessionCookie holds an opaque session id. It replaces the old falco_key
	// cookie, which carried the API key itself — and whose HttpOnly flag was
	// pointless because the same key was also copied into localStorage, where
	// any script on the page could read it.
	sessionCookie = "falco_session"

	// themeCookie remembers the light/dark choice. It is read on the server so
	// the correct theme is in the first byte of HTML: doing it from an inline
	// script was blocked by the panel's own CSP, which allows 'unsafe-eval'
	// (Alpine needs it) but not 'unsafe-inline'.
	themeCookie = "falco_theme"

	sessionTTL     = 12 * time.Hour
	sessionSweep   = 15 * time.Minute
	sessionIDBytes = 32
)

// Scope is the resolved access of a signed-in session.
type Scope struct {
	IsAdmin bool
	KeyName string
	Buckets map[string]bool
}

// CanAccessBucket reports whether this scope may touch the named bucket.
//
// It mirrors middleware.APIScope deliberately: the panel must not be able to
// reach anything the API would refuse, so both answer the same question the
// same way.
func (s *Scope) CanAccessBucket(bucket string) bool {
	if s == nil || s.IsAdmin {
		return true
	}
	if len(s.Buckets) == 0 {
		return true
	}
	return s.Buckets[bucket]
}

// APIScope converts to the middleware scope, so panel requests can be handed to
// the real API handlers with the caller's authority already attached.
func (s *Scope) APIScope() *apimw.APIScope {
	if s == nil {
		return nil
	}
	return &apimw.APIScope{IsAdmin: s.IsAdmin, KeyName: s.KeyName, Buckets: s.Buckets}
}

// Session is one signed-in browser.
type Session struct {
	ID string
	// APIKey is kept server-side so panel actions can be executed with the
	// caller's own authority. It is never sent to the browser.
	APIKey    string
	Scope     *Scope
	CSRFToken string
	ExpiresAt time.Time
}

// SessionStore holds live sessions in memory.
//
// In memory is the right scope for this: a restart signing everyone out of an
// admin panel is a non-event, and the alternative — persisting API keys — would
// mean writing credentials to disk for no gain.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	ttl      time.Duration
	stop     chan struct{}
}

// NewSessionStore starts a store with a background sweeper.
func NewSessionStore(ttl time.Duration) *SessionStore {
	if ttl <= 0 {
		ttl = sessionTTL
	}
	s := &SessionStore{
		sessions: make(map[string]*Session),
		ttl:      ttl,
		stop:     make(chan struct{}),
	}
	go s.sweep()
	return s
}

// Close stops the sweeper.
func (s *SessionStore) Close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

func (s *SessionStore) sweep() {
	ticker := time.NewTicker(sessionSweep)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			s.mu.Lock()
			for id, sess := range s.sessions {
				if now.After(sess.ExpiresAt) {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		case <-s.stop:
			return
		}
	}
}

// Create issues a new session. The id is fresh every time, so signing in never
// reuses an id an attacker could have planted beforehand.
func (s *SessionStore) Create(apiKey string, scope *Scope) (*Session, error) {
	id, err := randomToken()
	if err != nil {
		return nil, err
	}
	csrf, err := randomToken()
	if err != nil {
		return nil, err
	}

	sess := &Session{
		ID:        id,
		APIKey:    apiKey,
		Scope:     scope,
		CSRFToken: csrf,
		ExpiresAt: time.Now().Add(s.ttl),
	}

	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	return sess, nil
}

// Get returns a live session, or nil if it is unknown or expired.
func (s *SessionStore) Get(id string) *Session {
	if id == "" {
		return nil
	}
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	if time.Now().After(sess.ExpiresAt) {
		s.Delete(id)
		return nil
	}
	return sess
}

// Delete drops a session.
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// Count reports how many sessions are live. Used by the ops screen.
func (s *SessionStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

func randomToken() (string, error) {
	buf := make([]byte, sessionIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// isSecureRequest reports whether the browser reached us over TLS.
//
// The cookie used to be written with Secure unconditionally. Over plain HTTP
// the browser silently drops it, so /ui/auth answered {"ok":true}, the page
// redirected to the dashboard, and the dashboard bounced straight back to the
// login — an infinite loop with no error anywhere.
//
// X-Forwarded-Proto is honoured only from a trusted proxy, matching the rule
// the rest of falco already applies to client IPs: otherwise any client could
// claim HTTPS and have the cookie marked Secure over a plaintext hop.
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	// Honour the forwarded header only from a proxy falco was told to trust.
	// TRUSTED_PROXIES empty means nobody, which is the same fail-closed rule
	// the rest of falco applies to client IPs.
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}
	if !httputil.IsTrustedProxy(remoteIP) {
		return false
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	// A proxy chain sends a comma-separated list; the first entry is the
	// original client's protocol.
	if first, _, ok := strings.Cut(proto, ","); ok {
		proto = first
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// setSessionCookie writes the session cookie for this request's scheme.
func setSessionCookie(w http.ResponseWriter, r *http.Request, sess *Session) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		// Lax rather than Strict: Strict drops the cookie when the panel is
		// opened from a link elsewhere, which reads as a random signed-out
		// state. CSRF is handled by the token, not by the cookie policy.
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

// clearSessionCookie expires the cookie. It has to happen server-side: the
// cookie is HttpOnly, so scripts cannot remove it.
func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// checkCSRF compares the submitted token with the session's.
func checkCSRF(r *http.Request, sess *Session) bool {
	if sess == nil {
		return false
	}
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		token = r.FormValue("csrf_token")
	}
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(sess.CSRFToken)) == 1
}
