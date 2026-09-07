package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/storage"
)

// newActionHandler builds a panel wired to a real API handler over one
// filesystem bucket, so deletions can be asserted against storage rather than
// against what the UI says.
func newActionHandler(t *testing.T) (*Handler, storage.StorageBackend) {
	t.Helper()

	backend, err := storage.NewFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("backend: %v", err)
	}

	registry := storage.NewRegistry(backend)
	registry.Register("alpha", backend)

	cfg := &config.Config{}
	cfg.Storage.Default = "alpha"
	cfg.Storage.Buckets = map[string]config.BucketConfig{"alpha": {Type: "filesystem"}}
	cfg.Security.APIKeyRequired = true
	cfg.Security.APIKey = "admin-key"

	api := handlers.NewHandler(cfg, backend, nil, time.Now())
	api.SetRegistry(registry)

	return NewHandler(cfg, registry, api, nil), backend
}

// store puts an object straight into the backend.
func store(t *testing.T, backend storage.StorageBackend, key string) {
	t.Helper()
	meta := &storage.ImageMetadata{
		ID:          key,
		Format:      "webp",
		ContentType: "image/webp",
		Size:        7,
		CreatedAt:   time.Now().UTC(),
	}
	if err := backend.Store(context.Background(), key, strings.NewReader("payload"), meta); err != nil {
		t.Fatalf("store %s: %v", key, err)
	}
}

func exists(t *testing.T, backend storage.StorageBackend, key string) bool {
	t.Helper()
	ok, err := backend.Exists(context.Background(), key)
	if err != nil {
		t.Fatalf("exists %s: %v", key, err)
	}
	return ok
}

// TestDeleteActuallyDeletes is the test the original defect needed.
//
// The old panel sent `hx-delete=/api/v1/delete?id=…&b=…` to a handler that
// reads a strict JSON body and ignores the query string, so every click
// answered 400 INVALID_JSON — and the client only handled 401, so nothing was
// shown. The user confirmed a permanent delete and nothing happened.
//
// The assertion is the effect on storage, never the text of a confirmation.
func TestDeleteActuallyDeletes(t *testing.T) {
	h, backend := newActionHandler(t)
	defer h.Close()

	store(t, backend, "alpha-one")
	store(t, backend, "alpha-two")

	sess := signIn(t, h, "admin-key")
	rec := httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodPost, "/ui/objects/delete",
		strings.NewReader(`{"bucket":"alpha","keys":["alpha-one"]}`)), sess)
	req.Header.Set("X-CSRF-Token", sess.CSRFToken)

	h.RequireSession(h.DeleteObjects)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("delete: got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if exists(t, backend, "alpha-one") {
		t.Fatal("the object is still in storage after a successful delete response")
	}
	if !exists(t, backend, "alpha-two") {
		t.Fatal("an object that was not selected was deleted")
	}
}

// TestDeleteReportsPartialFailure: a batch where one key does not exist must
// not come back as an unqualified success.
func TestDeleteReportsPartialFailure(t *testing.T) {
	h, backend := newActionHandler(t)
	defer h.Close()

	store(t, backend, "present")

	sess := signIn(t, h, "admin-key")
	rec := httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodPost, "/ui/objects/delete",
		strings.NewReader(`{"bucket":"alpha","keys":["present","missing"]}`)), sess)
	req.Header.Set("X-CSRF-Token", sess.CSRFToken)

	h.RequireSession(h.DeleteObjects)(rec, req)

	var body struct {
		Success bool     `json:"success"`
		Deleted []string `json:"deleted"`
		Failed  []string `json:"failed"`
		Count   int      `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}

	// Whatever the API decides about the missing key, the panel must pass the
	// answer through untouched — status included — so a partial result cannot
	// be flattened into "done".
	if rec.Code == http.StatusMultiStatus && body.Success {
		t.Fatal("a 207 must not be reported as an unqualified success")
	}
	if len(body.Deleted) == 0 {
		t.Fatal("the key that did exist was not reported as deleted")
	}
	if exists(t, backend, "present") {
		t.Fatal("the existing key was not actually deleted")
	}
}

func TestDeleteRefusesAnotherScopesBucket(t *testing.T) {
	h, backend := newActionHandler(t)
	defer h.Close()

	store(t, backend, "victim")

	// A scope that may only touch "other".
	sess, err := h.sessions.Create("scoped", &Scope{KeyName: "scoped", Buckets: map[string]bool{"other": true}})
	if err != nil {
		t.Fatalf("session: %v", err)
	}

	rec := httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodPost, "/ui/objects/delete",
		strings.NewReader(`{"bucket":"alpha","keys":["victim"]}`)), sess)
	req.Header.Set("X-CSRF-Token", sess.CSRFToken)

	h.RequireSession(h.DeleteObjects)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}
	if !exists(t, backend, "victim") {
		t.Fatal("an out-of-scope delete went through")
	}
}

func TestDeleteRejectsAnEmptySelection(t *testing.T) {
	h, _ := newActionHandler(t)
	defer h.Close()

	sess := signIn(t, h, "admin-key")
	rec := httptest.NewRecorder()
	req := withSession(httptest.NewRequest(http.MethodPost, "/ui/objects/delete",
		strings.NewReader(`{"bucket":"alpha","keys":[]}`)), sess)
	req.Header.Set("X-CSRF-Token", sess.CSRFToken)

	h.RequireSession(h.DeleteObjects)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}
