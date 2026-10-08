package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func postSignIn(h *Handler, key string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/ui/auth", strings.NewReader(`{"key":"`+key+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.9:4000"
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	h.AuthPost(rec, req)
	return rec
}

// Another site must not be able to sign a visitor into the panel under its
// own key, nor sign them out.
func TestAuthPost_RefusesCrossSite(t *testing.T) {
	h := newTestHandler(t)
	defer h.Close()

	cases := map[string]func(*http.Request){
		"foreign origin":   func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"cross-site fetch": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"form body":        func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := postSignIn(h, "admin-key", mutate); rec.Code != http.StatusForbidden {
				t.Fatalf("got %d, want 403", rec.Code)
			}
		})
	}

	same := postSignIn(h, "admin-key", func(r *http.Request) {
		r.Header.Set("Origin", "http://"+r.Host)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	})
	if same.Code != http.StatusOK {
		t.Fatalf("same-origin sign-in: got %d", same.Code)
	}

	logout := httptest.NewRequest(http.MethodPost, "/ui/logout", nil)
	logout.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.LogoutPost(rec, logout)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site logout: got %d, want 403", rec.Code)
	}
}

func TestAuthPost_ThrottlesFailures(t *testing.T) {
	h := newTestHandler(t)
	defer h.Close()

	for i := range loginFailureLimit {
		if rec := postSignIn(h, "wrong", nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i, rec.Code)
		}
	}
	// Locked out now, even with the right key.
	rec := postSignIn(h, "admin-key", nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After on a lockout")
	}

	// Another client is unaffected.
	if rec := postSignIn(h, "admin-key", func(r *http.Request) { r.RemoteAddr = "198.51.100.1:1" }); rec.Code != http.StatusOK {
		t.Fatalf("other client: got %d", rec.Code)
	}

	// After the window, the client may try again.
	h.logins.now = func() time.Time { return time.Now().Add(loginFailureWindow + time.Second) }
	if rec := postSignIn(h, "admin-key", nil); rec.Code != http.StatusOK {
		t.Fatalf("after the window: got %d", rec.Code)
	}
}

func TestAuthPost_CookieSecureCanBeForced(t *testing.T) {
	h := newTestHandler(t)
	defer h.Close()
	h.cfg.Security.CookieSecure = true

	rec := postSignIn(h, "admin-key", nil)
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && !c.Secure {
			t.Fatal("COOKIE_SECURE=true but the session cookie is not Secure")
		}
	}
}

func TestSessionStore_CapsSessionsPerKey(t *testing.T) {
	s := NewSessionStore(time.Hour)
	defer s.Close()

	for range maxSessionsPerKey + 5 {
		if _, err := s.Create("k", &Scope{IsAdmin: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create("other", &Scope{IsAdmin: true}); err != nil {
		t.Fatal(err)
	}
	if got := s.Count(); got != maxSessionsPerKey+1 {
		t.Fatalf("got %d sessions, want %d", got, maxSessionsPerKey+1)
	}
}
