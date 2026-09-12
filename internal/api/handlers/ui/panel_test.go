package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/security"
	"github.com/birdple/falco/internal/storage"
)

// Test HMAC material. Both must be hex: the signer decodes them.
const (
	testHMACKey  = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	testHMACSalt = "ffeeddccbbaa99887766554433221100"
)

// newTestHandler builds a panel over two filesystem buckets, "alpha" and
// "beta", with an admin key and a key scoped to alpha only.
func newTestHandler(t *testing.T) *Handler {
	t.Helper()

	alphaDir := t.TempDir()
	betaDir := t.TempDir()

	alpha, err := storage.NewFilesystemStorage(alphaDir)
	if err != nil {
		t.Fatalf("alpha backend: %v", err)
	}
	beta, err := storage.NewFilesystemStorage(betaDir)
	if err != nil {
		t.Fatalf("beta backend: %v", err)
	}

	registry := storage.NewRegistry(alpha)
	registry.Register("alpha", alpha)
	registry.Register("beta", beta)

	cfg := &config.Config{}
	cfg.Storage.Default = "alpha"
	cfg.Storage.Buckets = map[string]config.BucketConfig{
		"alpha": {Type: "filesystem", Keys: []config.BucketKeyConfig{{Name: "alphaonly", Key: "alpha-key"}}},
		"beta":  {Type: "filesystem"},
	}
	cfg.Security.APIKeyRequired = true
	cfg.Security.APIKey = "admin-key"
	cfg.Security.HMACKey = testHMACKey
	cfg.Security.HMACKeySalt = testHMACSalt
	cfg.Security.HMACSignatureSize = 32
	cfg.Security.HMACRequired = true

	// The API handler is only needed by the mutating actions, which these
	// tests do not exercise; the panel's own routes never dereference it.
	return NewHandler(cfg, registry, nil, nil)
}

// signIn returns a request carrying a valid session for the given key.
func signIn(t *testing.T, h *Handler, key string) *Session {
	t.Helper()

	scope := h.resolveKey(key)
	if scope == nil {
		t.Fatalf("key %q was rejected", key)
	}
	sess, err := h.sessions.Create(key, scope)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func withSession(r *http.Request, sess *Session) *http.Request {
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess.ID})
	return r
}

// --- authentication ---

func TestAuthPostIssuesSessionAndNeverReturnsTheKey(t *testing.T) {
	h := newTestHandler(t)
	defer h.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ui/auth", strings.NewReader(`{"key":"admin-key"}`))
	h.AuthPost(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in: got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie was set")
	}
	if !cookie.HttpOnly {
		t.Error("the session cookie must be HttpOnly")
	}
	// The old panel stored the API key itself in the cookie AND in
	// localStorage, which made HttpOnly meaningless.
	if strings.Contains(cookie.Value, "admin-key") {
		t.Error("the API key leaked into the session cookie")
	}
	if strings.Contains(rec.Body.String(), "admin-key") {
		t.Error("the API key leaked into the sign-in response body")
	}
}

func TestAuthCookieIsNotSecureOverPlainHTTP(t *testing.T) {
	// Secure was set unconditionally, so over plain HTTP the browser dropped
	// the cookie: /ui/auth answered {"ok":true}, the page redirected to the
	// dashboard, and the dashboard bounced back to the login. An endless loop
	// with no error anywhere.
	h := newTestHandler(t)
	defer h.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/ui/auth", strings.NewReader(`{"key":"admin-key"}`))
	req.TLS = nil
	h.AuthPost(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Secure {
			t.Fatal("cookie marked Secure over plain HTTP: the browser will discard it and login will loop")
		}
	}
}

func TestLogoutInvalidatesTheSessionServerSide(t *testing.T) {
	h := newTestHandler(t)
	defer h.Close()

	sess := signIn(t, h, "admin-key")

	rec := httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodPost, "/ui/logout", nil), sess)
	h.LogoutPost(rec, req)

	// Clearing the cookie is not enough: a copied cookie value would still
	// work if the session survived on the server.
	if h.sessions.Get(sess.ID) != nil {
		t.Fatal("the session is still live after logout")
	}
}

func TestRequireSessionRejectsUnknownSession(t *testing.T) {
	h := newTestHandler(t)
	defer h.Close()

	called := false
	guarded := h.RequireSession(func(http.ResponseWriter, *http.Request) { called = true })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "not-a-session"})
	guarded(rec, req)

	if called {
		t.Fatal("the handler ran without a valid session")
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("got %d, want a redirect to the login", rec.Code)
	}
}

// --- CSRF ---

func TestMutationsRequireCSRFToken(t *testing.T) {
	h := newTestHandler(t)
	defer h.Close()

	sess := signIn(t, h, "admin-key")
	called := false
	guarded := h.RequireSession(func(http.ResponseWriter, *http.Request) { called = true })

	// Without the token.
	rec := httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodPost, "/ui/objects/delete", nil), sess)
	guarded(rec, req)
	if called {
		t.Fatal("a mutation ran without a CSRF token")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}

	// With it.
	rec = httptest.NewRecorder()
	req = withSession(httptest.NewRequest(http.MethodPost, "/ui/objects/delete", nil), sess)
	req.Header.Set("X-CSRF-Token", sess.CSRFToken)
	guarded(rec, req)
	if !called {
		t.Fatal("a mutation with a valid CSRF token was rejected")
	}

	// A token from a different session must not work.
	other := signIn(t, h, "alpha-key")
	called = false
	rec = httptest.NewRecorder()
	req = withSession(httptest.NewRequest(http.MethodPost, "/ui/objects/delete", nil), sess)
	req.Header.Set("X-CSRF-Token", other.CSRFToken)
	guarded(rec, req)
	if called {
		t.Fatal("a CSRF token from another session was accepted")
	}
}

// --- scope enforcement ---

func TestScopedKeyCannotReachAnotherBucket(t *testing.T) {
	// The leak this guards: the dashboard checked bucket scope, and
	// /ui/content — the HTMX partial rendering the same data — did not. A key
	// scoped to one bucket got another bucket's full listing by changing the
	// query string.
	h := newTestHandler(t)
	defer h.Close()

	sess := signIn(t, h, "alpha-key")
	if sess.Scope.IsAdmin {
		t.Fatal("the scoped key resolved as admin")
	}

	for _, target := range []string{"/dashboard?bucket=beta", "/ui/explorer?bucket=beta"} {
		handler := h.Dashboard
		if strings.HasPrefix(target, "/ui/") {
			handler = h.Explorer
		}

		rec := httptest.NewRecorder()
		req := withSession(httptest.NewRequest(http.MethodGet, target, nil), sess)
		handler(rec, req)

		if rec.Code == http.StatusOK {
			t.Fatalf("%s: a key scoped to alpha was served beta", target)
		}
		if strings.Contains(rec.Body.String(), "beta") && rec.Code == http.StatusOK {
			t.Fatalf("%s: beta's contents leaked", target)
		}
	}
}

func TestScopedKeyOnlySeesItsOwnBuckets(t *testing.T) {
	h := newTestHandler(t)
	defer h.Close()

	scoped := h.resolveKey("alpha-key")
	if got := h.accessibleBuckets(scoped); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("scoped key sees %v, want [alpha]", got)
	}

	admin := h.resolveKey("admin-key")
	if got := h.accessibleBuckets(admin); len(got) != 2 {
		t.Fatalf("admin sees %v, want both buckets", got)
	}
}

// --- the panel refuses to run unconfigured ---

func TestPanelRefusesWhenNoKeyIsConfigured(t *testing.T) {
	// With API_KEY_REQUIRED=false and no key, the old panel invented an admin
	// scope on the spot, so anyone who could reach the port had full access to
	// every bucket. An unconfigured feature refuses; it does not open.
	h := newTestHandler(t)
	defer h.Close()

	h.cfg.Security.APIKey = ""
	h.cfg.Security.APIKeyRequired = false
	h.keys = map[string]config.KeyScope{}

	if h.enabled() {
		t.Fatal("the panel reports itself enabled with no key configured")
	}

	called := false
	guarded := h.RequireSession(func(http.ResponseWriter, *http.Request) { called = true })
	rec := httptest.NewRecorder()
	guarded(rec, httptest.NewRequest(http.MethodGet, "/dashboard", nil))

	if called {
		t.Fatal("the dashboard rendered with no key configured")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
}

// --- signed thumbnails ---

func TestThumbnailURLsCarryAVerifiableSignature(t *testing.T) {
	// This is the defect that made the panel useless in the deployment it
	// ships in: thumbnails were rendered as plain /api/v1/images/<id>?b=<x>
	// while the stack runs HMAC_REQUIRED=true, so every single one was a 403.
	//
	// The assertion is that the real verifier accepts the URL — not that the
	// markup contains an <img> tag.
	h := newTestHandler(t)
	defer h.Close()

	raw := h.signedThumb("alpha", "avatars/2024/abc123", 480)

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("thumbnail URL does not parse: %v", err)
	}
	sig := parsed.Query().Get("sig")
	if sig == "" {
		t.Fatal("thumbnail URL carries no signature")
	}
	if parsed.Query().Get("exp") == "" {
		t.Fatal("thumbnail URL carries no expiry; with HMAC_REQUIRE_EXPIRY it would be refused")
	}

	if err := security.VerifyURLWithPolicy(
		sig, raw,
		testHMACKey, testHMACSalt, 32,
		true, // required
		true, // expiry required
	); err != nil {
		t.Fatalf("delivery would reject the panel's own thumbnail: %v", err)
	}
}

func TestThumbnailSignatureIsRejectedWhenTampered(t *testing.T) {
	h := newTestHandler(t)
	defer h.Close()

	raw := h.signedThumb("alpha", "abc123", 480)
	tampered := strings.Replace(raw, "w=480", "w=1600", 1)
	sig, _ := url.Parse(tampered)

	if err := security.VerifyURLWithPolicy(
		sig.Query().Get("sig"), tampered,
		testHMACKey, testHMACSalt, 32, true, true,
	); err == nil {
		t.Fatal("a tampered thumbnail URL verified: the width is not covered by the signature")
	}
}

func TestSigningDisabledFallsBackToAnUnsignedPath(t *testing.T) {
	h := newTestHandler(t)
	defer h.Close()

	h.cfg.Security.HMACKey = ""
	got := h.signedThumb("alpha", "abc123", 480)

	if strings.Contains(got, "sig=") {
		t.Fatal("a signature was produced with no HMAC key configured")
	}
	if !strings.HasPrefix(got, "/api/v1/images/abc123") {
		t.Fatalf("unexpected fallback URL: %s", got)
	}
}

// --- navigation helpers ---

func TestBreadcrumbsReachEveryDepth(t *testing.T) {
	// The old dashboard only ever looked at the first path segment, so
	// "avatars/2024/x.webp" had no folder to click and no row in the grid: it
	// was unreachable through the UI entirely.
	crumbs := breadcrumbs("avatars/2024/summer/")

	if len(crumbs) != 3 {
		t.Fatalf("got %d crumbs, want 3", len(crumbs))
	}
	want := []struct{ name, prefix string }{
		{"avatars", "avatars/"},
		{"2024", "avatars/2024/"},
		{"summer", "avatars/2024/summer/"},
	}
	for i, w := range want {
		if crumbs[i].Name != w.name || crumbs[i].Prefix != w.prefix {
			t.Fatalf("crumb %d = %+v, want %v", i, crumbs[i], w)
		}
	}
	if !crumbs[2].IsLast {
		t.Error("the deepest crumb must be marked as current")
	}
}

func TestNormalizePrefixRefusesTraversal(t *testing.T) {
	for _, in := range []string{"../etc", "avatars/../../etc", ".."} {
		if got := normalizePrefix(in); got != "" {
			t.Fatalf("normalizePrefix(%q) = %q, want it refused", in, got)
		}
	}
	if got := normalizePrefix("avatars"); got != "avatars/" {
		t.Fatalf("normalizePrefix(avatars) = %q, want avatars/", got)
	}
}

func TestDOMIDIsAValidCSSSelector(t *testing.T) {
	// "#image-avatars/a1b2" is not a valid selector and throws inside
	// querySelector, which is what broke the delete button in every folder.
	id := domID("avatars/2024/a1b2.webp")

	if strings.ContainsAny(id, "/.:# ") {
		t.Fatalf("domID(%q) = %q contains characters invalid in a CSS id", "avatars/2024/a1b2.webp", id)
	}
	if id == domID("avatars/2024/other.webp") {
		t.Fatal("two different keys hash to the same DOM id")
	}
}

func TestFormatLabelReadsContentTypeNotTheKey(t *testing.T) {
	// falco stores content-hashed keys with no extension, so the old badge —
	// which switched on the file extension — could only ever answer "IMG".
	cases := map[string]string{
		"image/webp":              "WEBP",
		"image/jpeg":              "JPG",
		"image/png":               "PNG",
		"image/avif":              "AVIF",
		"image/svg+xml":           "SVG+XML",
		"image/webp; charset=bin": "WEBP",
		"":                        "—",
	}
	for in, want := range cases {
		if got := formatLabel(in); got != want {
			t.Errorf("formatLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- sessions ---

func TestSessionStoreDropsExpiredSessions(t *testing.T) {
	store := NewSessionStore(-1) // any TTL <= 0 falls back to the default
	defer store.Close()

	sess, err := store.Create("k", &Scope{IsAdmin: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if store.Get(sess.ID) == nil {
		t.Fatal("a fresh session is not retrievable")
	}

	sess.ExpiresAt = time.Now().Add(-time.Minute)
	if store.Get(sess.ID) != nil {
		t.Fatal("an expired session is still served")
	}
}

func TestSessionIDsAreUnique(t *testing.T) {
	store := NewSessionStore(0)
	defer store.Close()

	seen := map[string]bool{}
	for range 200 {
		s, err := store.Create("k", &Scope{IsAdmin: true})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if seen[s.ID] {
			t.Fatal("duplicate session id")
		}
		seen[s.ID] = true
	}
}

// --- disabled features are reported, not hidden ---

func TestFeatureStatesNameTheMissingVariable(t *testing.T) {
	h := newTestHandler(t)
	defer h.Close()

	h.cfg.Security.HMACKey = ""
	for _, f := range h.featureStates() {
		if f.Name != "Signed URLs" {
			continue
		}
		if f.Enabled {
			t.Fatal("signing reported as enabled with no HMAC key")
		}
		if !strings.Contains(f.Reason, "HMAC_KEY") {
			t.Fatalf("the reason does not name the variable to set: %q", f.Reason)
		}
		return
	}
	t.Fatal("no entry for signed URLs")
}

func TestEffectiveConfigNeverExposesSecrets(t *testing.T) {
	h := newTestHandler(t)
	defer h.Close()

	body, err := json.Marshal(h.effectiveConfig())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{"admin-key", testHMACKey, testHMACSalt} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("a secret value appears in the effective configuration: %s", secret)
		}
	}
	if !strings.Contains(string(body), `"set"`) {
		t.Error("secrets should still be reported as set/not set")
	}
}
