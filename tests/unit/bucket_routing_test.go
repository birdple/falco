package unit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/pkg/circuitbreaker"
	"github.com/birdple/falco/internal/processor"
	"github.com/birdple/falco/internal/storage"
	"github.com/birdple/falco/tests/mocks"
)

// These tests are about ONE question: after a request that named a bucket, in
// which bucket did the bytes end up?
//
// Asserting the status code is what let PND-0196 live. `POST /upload?b=beta`
// answered 201 with the object written into the default bucket, so a test that
// checked for 201 — or for the `url` in the response, which even carried
// `?b=beta` — passed green with the destination wrong.

// bucketProbe is one filesystem bucket plus the handle used to ask it whether
// it holds a key. Two of them, on separate temp directories, make "did it land
// in alpha or in beta" a question about bytes rather than about a log line.
type bucketProbe struct {
	name    string
	backend storage.StorageBackend
}

func (b bucketProbe) has(t *testing.T, key string) bool {
	t.Helper()
	ok, err := b.backend.Exists(context.Background(), key)
	require.NoError(t, err, "Exists on bucket %q", b.name)
	return ok
}

// newRoutingHandler builds an API handler over two real filesystem buckets,
// wrapped in the circuit breaker exactly as cmd/server does.
//
// The wrapper is not incidental. It implements BucketAware for EVERY backend,
// returning itself when the one underneath cannot switch, so
// `backend.(storage.BucketAware)` always succeeded and the failure had nothing
// to report it. A harness that registered the bare filesystem backends would
// not reproduce the defect at all.
func newRoutingHandler(t *testing.T, aliases map[string]string) (*handlers.Handler, bucketProbe, bucketProbe) {
	t.Helper()

	alphaFS, err := storage.NewFilesystemStorage(t.TempDir())
	require.NoError(t, err)
	betaFS, err := storage.NewFilesystemStorage(t.TempDir())
	require.NoError(t, err)

	alpha := circuitbreaker.NewStorageBackend(alphaFS, circuitbreaker.DefaultSettings("alpha"))
	beta := circuitbreaker.NewStorageBackend(betaFS, circuitbreaker.DefaultSettings("beta"))

	registry := storage.NewRegistry(alpha)
	registry.Register("alpha", alpha)
	registry.Register("beta", beta)
	require.NoError(t, registry.SetDefault("alpha"))
	for alias, target := range aliases {
		require.NoError(t, registry.RegisterAlias(alias, target))
	}

	cfg := &config.Config{
		Processing: config.ProcessingConfig{
			MaxFileSizeMB:  10,
			DefaultQuality: 85,
			DefaultFormat:  "webp",
		},
	}
	cfg.Storage.Default = "alpha"
	cfg.Storage.Buckets = map[string]config.BucketConfig{
		"alpha": {Type: "filesystem"},
		"beta":  {Type: "filesystem"},
	}

	h := handlers.NewHandler(cfg, alpha, passthroughProcessor(), time.Now())
	h.SetRegistry(registry)

	return h, bucketProbe{name: "alpha", backend: alpha}, bucketProbe{name: "beta", backend: beta}
}

// passthroughProcessor hands the upload back unchanged, so the bytes that reach
// storage are the bytes that were posted. libvips is not what these tests are
// about.
func passthroughProcessor() *mocks.MockImageProcessor {
	p := new(mocks.MockImageProcessor)
	p.On("Process", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(func(_ context.Context, r io.Reader, _ *processor.ProcessingParams, _ string) *processor.ProcessedImage {
			data, _ := io.ReadAll(r)
			return &processor.ProcessedImage{
				Data: io.NopCloser(bytes.NewReader(data)),
				Metadata: &processor.ImageMetadata{
					Format:      "jpeg",
					ContentType: "image/jpeg",
					Size:        int64(len(data)),
					Width:       100,
					Height:      100,
					CreatedAt:   time.Now().UTC(),
				},
			}
		}, nil).Maybe()
	return p
}

// jpegBytes is enough of a JPEG header for DetectContentType to route the
// upload down the image branch.
var jpegBytes = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}

func upload(t *testing.T, h *handlers.Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload"+query, bytes.NewReader(jpegBytes))
	req.Header.Set("Content-Type", "image/jpeg")
	rec := httptest.NewRecorder()
	h.HandleUpload(rec, req)
	return rec
}

// assertRefused checks the status, the machine-readable code AND the success
// flag together. The flag is part of it on purpose: the body of this API says
// `success` in its own field, so a refusal that forgot to clear it would still
// read as a confirmation to a client that trusts the payload over the status.
func assertRefused(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()

	assert.Equal(t, status, rec.Code, rec.Body.String())

	var body struct {
		Success bool `json:"success"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "response body: %s", rec.Body.String())
	assert.False(t, body.Success, "a refusal must not claim success")
	assert.Equal(t, code, errorCode(t, rec))
}

// The measurement in PND-0196: `?b=beta` answered 201 and the file was in
// alpha. The assertion is on the two buckets, not on the status.
func TestUploadLandsInTheRequestedBucket(t *testing.T) {
	h, alpha, beta := newRoutingHandler(t, nil)

	rec := upload(t, h, "?b=beta&id=probe")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	assert.True(t, beta.has(t, "probe"), "the object must be in the bucket the request named")
	assert.False(t, alpha.has(t, "probe"), "the object must NOT be in the default bucket")
}

// A bucket falco cannot serve has to be refused, and refused BEFORE anything is
// written. The second assertion is the one that matters: a 4xx that still left
// an object behind would be the same lie wearing a different status code.
func TestUploadRefusesAnUnknownBucketAndWritesNothing(t *testing.T) {
	h, alpha, beta := newRoutingHandler(t, nil)

	rec := upload(t, h, "?b=ghost&id=probe")

	assertRefused(t, rec, http.StatusBadRequest, "UNKNOWN_BUCKET")
	assert.False(t, alpha.has(t, "probe"), "a refused upload must not land in the default bucket")
	assert.False(t, beta.has(t, "probe"), "a refused upload must not land anywhere")
}

// The compatibility path: a name a client was configured to send, declared as
// an alias, resolves to the bucket it stands for — and the object is really
// there. This is what keeps birdple-api's `?b=birdple-dev` working against a
// bucket that can only be named "jay", without falco pretending.
func TestUploadFollowsADeclaredAlias(t *testing.T) {
	h, alpha, beta := newRoutingHandler(t, map[string]string{"birdple-dev": "beta"})

	rec := upload(t, h, "?b=birdple-dev&id=probe")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	assert.True(t, beta.has(t, "probe"), "the alias must resolve to its target bucket")
	assert.False(t, alpha.has(t, "probe"))
}

// An alias is a declaration, not a spelling rule: an undeclared name that
// merely looks like one is still refused.
func TestUndeclaredAliasIsStillRefused(t *testing.T) {
	h, _, _ := newRoutingHandler(t, map[string]string{"birdple-dev": "beta"})

	rec := upload(t, h, "?b=birdple-prod&id=probe")

	assertRefused(t, rec, http.StatusBadRequest, "UNKNOWN_BUCKET")
}

// Reads have to answer the same way writes do. Delivery resolved the bucket
// through the same helper, so it silently read from the default one too.
func TestDeliveryRefusesAnUnknownBucket(t *testing.T) {
	h, _, _ := newRoutingHandler(t, nil)

	// Through a router: HandleDelivery reads the key from the chi wildcard, so
	// calling it bare would fail on a missing id before ever reaching the
	// bucket.
	router := chi.NewRouter()
	router.Get("/api/v1/images/*", h.HandleDelivery)

	rec := get(t, router, "/api/v1/images/probe?b=ghost")

	assertRefused(t, rec, http.StatusBadRequest, "UNKNOWN_BUCKET")
}

func TestListRefusesAnUnknownBucket(t *testing.T) {
	h, _, _ := newRoutingHandler(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/list?b=ghost", nil)
	rec := httptest.NewRecorder()
	h.HandleList(rec, req)

	assertRefused(t, rec, http.StatusBadRequest, "UNKNOWN_BUCKET")
}

// An upload that names no bucket at all keeps going to the default one. The fix
// refuses names it cannot serve; it does not make `?b=` mandatory.
func TestUploadWithoutABucketStillUsesTheDefault(t *testing.T) {
	h, alpha, beta := newRoutingHandler(t, nil)

	rec := upload(t, h, "?id=probe")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	assert.True(t, alpha.has(t, "probe"))
	assert.False(t, beta.has(t, "probe"))
}

// --- backends that really can switch bucket ---

// switchingBackend is the S3/R2 shape: a backend that implements BucketAware
// for real. It records the bucket each Store went to, so "the switch happened"
// is asserted on the write and not on GetCurrentBucket alone.
type switchingBackend struct {
	bucket string
	stored *map[string]string // key -> bucket that received it
}

func newSwitchingBackend(bucket string) *switchingBackend {
	stored := make(map[string]string)
	return &switchingBackend{bucket: bucket, stored: &stored}
}

func (s *switchingBackend) WithBucket(bucket string) storage.StorageBackend {
	if bucket == "" {
		return s
	}
	return &switchingBackend{bucket: bucket, stored: s.stored}
}

func (s *switchingBackend) GetCurrentBucket() string { return s.bucket }

func (s *switchingBackend) Store(_ context.Context, key string, data io.Reader, _ *storage.ImageMetadata) error {
	if _, err := io.Copy(io.Discard, data); err != nil {
		return err
	}
	(*s.stored)[key] = s.bucket
	return nil
}

func (s *switchingBackend) Retrieve(context.Context, string) (io.ReadCloser, *storage.ImageMetadata, error) {
	return nil, nil, storage.ErrImageNotFound
}

func (s *switchingBackend) Exists(_ context.Context, key string) (bool, error) {
	return (*s.stored)[key] == s.bucket, nil
}

func (s *switchingBackend) Delete(context.Context, string) error { return nil }

func (s *switchingBackend) List(context.Context, string) ([]storage.ListResult, error) {
	return nil, nil
}

func (s *switchingBackend) Health(context.Context) error { return nil }

func (s *switchingBackend) GetStats(context.Context) (*storage.StorageStats, error) {
	return &storage.StorageStats{}, nil
}

// A backend that genuinely switches keeps working, and the object really goes
// to the remote bucket that was named — which is the one case `?b=` ever
// honoured, and the one the fix must not break.
func TestUploadSwitchesRemoteBucketOnABucketAwareBackend(t *testing.T) {
	backing := newSwitchingBackend("home")
	wrapped := circuitbreaker.NewStorageBackend(backing, circuitbreaker.DefaultSettings("main"))

	registry := storage.NewRegistry(wrapped)
	registry.Register("main", wrapped)
	require.NoError(t, registry.SetDefault("main"))

	cfg := &config.Config{
		Processing: config.ProcessingConfig{MaxFileSizeMB: 10, DefaultQuality: 85, DefaultFormat: "webp"},
	}
	cfg.Storage.Default = "main"
	cfg.Storage.Buckets = map[string]config.BucketConfig{"main": {Type: "s3"}}

	h := handlers.NewHandler(cfg, wrapped, passthroughProcessor(), time.Now())
	h.SetRegistry(registry)

	rec := upload(t, h, "?b=elsewhere&id=probe")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	assert.Equal(t, "elsewhere", (*backing.stored)["probe"],
		"a BucketAware backend must write to the bucket that was requested")
}

// Delete takes the bucket in a JSON body rather than the query string — the
// same shape /update uses. The refusal has to reach that path too, and the
// object has to still be there afterwards: a delete that answered anything
// other than "I did not do it" would be the failure this whole change is
// about, pointed at destruction instead of at writes.
func TestDeleteRefusesAnUnknownBucketAndDeletesNothing(t *testing.T) {
	h, alpha, _ := newRoutingHandler(t, nil)

	require.Equal(t, http.StatusCreated, upload(t, h, "?id=probe").Code)
	require.True(t, alpha.has(t, "probe"))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/delete",
		strings.NewReader(`{"bucket":"ghost","keys":["probe"]}`))
	rec := httptest.NewRecorder()
	h.HandleDelete(rec, req)

	assertRefused(t, rec, http.StatusBadRequest, "UNKNOWN_BUCKET")
	assert.True(t, alpha.has(t, "probe"), "a refused delete must not have removed anything")
}
