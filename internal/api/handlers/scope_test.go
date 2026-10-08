package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apimw "github.com/birdple/falco/internal/api/middleware"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/storage"
)

// scopeHandler builds a handler over two filesystem buckets, "main" (the
// default) and "other", with an alias "main-alias" for "main".
func scopeHandler(t *testing.T) *Handler {
	t.Helper()
	mainFS, err := storage.NewFilesystemStorage(t.TempDir())
	require.NoError(t, err)
	otherFS, err := storage.NewFilesystemStorage(t.TempDir())
	require.NoError(t, err)

	reg := storage.NewRegistry(mainFS)
	reg.Register("main", mainFS)
	reg.Register("other", otherFS)
	require.NoError(t, reg.SetDefault("main"))
	require.NoError(t, reg.RegisterAlias("main-alias", "main"))

	cfg := &config.Config{}
	cfg.Storage.Default = "main"
	cfg.Security.HMACKey = "00112233445566778899aabbccddeeff"
	cfg.Security.HMACKeySalt = "ffeeddccbbaa99887766554433221100"

	h := NewHandler(cfg, mainFS, nil, time.Now())
	h.SetRegistry(reg)
	return h
}

func onlyBucket(name string) *apimw.APIScope {
	return &apimw.APIScope{KeyName: "scoped-" + name, Buckets: map[string]bool{name: true}}
}

func TestAuthorizeBucket(t *testing.T) {
	h := scopeHandler(t)

	cases := []struct {
		name          string
		scope         *apimw.APIScope
		storage, b    string
		wantForbidden bool
	}{
		{"admin reaches anything", &apimw.APIScope{IsAdmin: true}, "other", "", false},
		{"no scope reaches anything", nil, "other", "", false},
		{"scoped to main, no bucket named", onlyBucket("main"), "", "", false},
		{"scoped to other, no bucket named, lands on default", onlyBucket("other"), "", "", true},
		{"scoped to other, asks for other", onlyBucket("other"), "", "other", false},
		{"scoped to main, asks for other via b", onlyBucket("main"), "", "other", true},
		{"scoped to main, asks for other via storage", onlyBucket("main"), "other", "", true},
		{"scoped to main, alias of main", onlyBucket("main"), "", "main-alias", false},
		{"empty scope grants nothing", &apimw.APIScope{KeyName: "empty"}, "", "main", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := h.authorizeBucket(tc.scope, tc.storage, tc.b)
			if tc.wantForbidden {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func signRequest(t *testing.T, h *Handler, scope *apimw.APIScope, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sign", strings.NewReader(body))
	req = req.WithContext(apimw.WithScope(req.Context(), scope))
	w := httptest.NewRecorder()
	h.HandleSignURL(w, req)
	return w
}

func TestHandleSignURL_EnforcesScopeLikeDelivery(t *testing.T) {
	h := scopeHandler(t)
	scope := onlyBucket("main")

	cases := []struct {
		path string
		want int
	}{
		{"/api/v1/images/abc", http.StatusOK},
		{"/api/v1/images/abc?b=main", http.StatusOK},
		{"/api/v1/images/abc?b=main-alias", http.StatusOK},
		{"/api/v1/images/abc?b=other", http.StatusForbidden},
		{"/api/v1/images/abc?bucket=other", http.StatusForbidden},
		// ?storage= is what delivery uses to pick the backend; /sign used to
		// ignore it and sign the URL anyway.
		{"/api/v1/images/abc?storage=other", http.StatusForbidden},
		{"/api/v1/images/abc?storage=other&b=main", http.StatusForbidden},
		// url.Parse and Canonicalize disagree about "#", so it is refused.
		{"/api/v1/images/#/abc?b=other", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			w := signRequest(t, h, scope, `{"path":"`+tc.path+`","expires_in":60}`)
			assert.Equal(t, tc.want, w.Code, w.Body.String())
		})
	}
}

func TestHandleSignURL_DefaultBucketNeedsScope(t *testing.T) {
	h := scopeHandler(t)
	w := signRequest(t, h, onlyBucket("other"), `{"path":"/api/v1/images/abc","expires_in":60}`)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandleSignURL_CapsExpiry(t *testing.T) {
	h := scopeHandler(t)
	w := signRequest(t, h, nil, `{"path":"/api/v1/images/abc","expires_at":253402300799}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = signRequest(t, h, nil, `{"path":"/api/v1/images/abc","expires_in":3600}`)
	assert.Equal(t, http.StatusOK, w.Code)
}
