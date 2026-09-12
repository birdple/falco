package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jayclient "github.com/ivangsm/jay/proto/client"
)

// fakeJayClient implements jayClientIface for testing.
type fakeJayClient struct {
	putFn    func(ctx context.Context, bucket, key string, data io.Reader, size int64, opts *jayclient.PutOptions) (*jayclient.PutResult, error)
	getFn    func(ctx context.Context, bucket, key string) (*jayclient.GetResult, error)
	headFn   func(ctx context.Context, bucket, key string) (*jayclient.ObjectInfo, error)
	delFn    func(ctx context.Context, bucket, key string) error
	listFn   func(ctx context.Context, bucket string, opts *jayclient.ListOptions) (*jayclient.ListResult, error)
	hBucket  func(ctx context.Context, name string) (*jayclient.BucketInfo, error)
	cBucket  func(ctx context.Context, name string) (*jayclient.BucketInfo, error)
	closeErr error
}

func (f *fakeJayClient) PutObject(ctx context.Context, bucket, key string, data io.Reader, size int64, opts *jayclient.PutOptions) (*jayclient.PutResult, error) {
	if f.putFn == nil {
		return nil, errors.New("fakeJayClient: unexpected PutObject call")
	}
	return f.putFn(ctx, bucket, key, data, size, opts)
}
func (f *fakeJayClient) GetObject(ctx context.Context, bucket, key string) (*jayclient.GetResult, error) {
	if f.getFn == nil {
		return nil, errors.New("fakeJayClient: unexpected GetObject call")
	}
	return f.getFn(ctx, bucket, key)
}
func (f *fakeJayClient) HeadObject(ctx context.Context, bucket, key string) (*jayclient.ObjectInfo, error) {
	if f.headFn == nil {
		return nil, errors.New("fakeJayClient: unexpected HeadObject call")
	}
	return f.headFn(ctx, bucket, key)
}
func (f *fakeJayClient) DeleteObject(ctx context.Context, bucket, key string) error {
	if f.delFn == nil {
		return errors.New("fakeJayClient: unexpected DeleteObject call")
	}
	return f.delFn(ctx, bucket, key)
}
func (f *fakeJayClient) ListObjects(ctx context.Context, bucket string, opts *jayclient.ListOptions) (*jayclient.ListResult, error) {
	if f.listFn == nil {
		return nil, errors.New("fakeJayClient: unexpected ListObjects call")
	}
	return f.listFn(ctx, bucket, opts)
}
func (f *fakeJayClient) HeadBucket(ctx context.Context, name string) (*jayclient.BucketInfo, error) {
	if f.hBucket == nil {
		return nil, errors.New("fakeJayClient: unexpected HeadBucket call")
	}
	return f.hBucket(ctx, name)
}
func (f *fakeJayClient) CreateBucket(ctx context.Context, name string) (*jayclient.BucketInfo, error) {
	if f.cBucket == nil {
		return nil, errors.New("fakeJayClient: unexpected CreateBucket call")
	}
	return f.cBucket(ctx, name)
}
func (f *fakeJayClient) Close() error { return f.closeErr }

func newJayStorageWithClient(c jayClientIface, bucket string) *JayStorage {
	return &JayStorage{client: c, bucket: bucket}
}

func TestJayStorage_Store_Success(t *testing.T) {
	var gotBucket, gotKey string
	var gotSize int64
	var gotMeta map[string]string
	fc := &fakeJayClient{
		putFn: func(ctx context.Context, bucket, key string, data io.Reader, size int64, opts *jayclient.PutOptions) (*jayclient.PutResult, error) {
			gotBucket, gotKey, gotSize = bucket, key, size
			if opts != nil {
				gotMeta = opts.Metadata
			}
			_, _ = io.Copy(io.Discard, data)
			return &jayclient.PutResult{ETag: "etag-123", ChecksumSHA256: "abc"}, nil
		},
	}
	js := newJayStorageWithClient(fc, "falco-images")
	m := &ImageMetadata{
		ID: "img-1", OriginalName: "pic.jpg", Format: "jpeg",
		Size: 100, Width: 640, Height: 480, ContentType: "image/jpeg",
		CreatedAt: time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC),
	}
	body := bytes.NewReader([]byte("hello world-----100chars-------------------------------------------------------------------------"))
	if err := js.Store(context.Background(), "img-1", body, m); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if gotBucket != "falco-images" || gotKey != "img-1" {
		t.Fatalf("wrong bucket/key: %s/%s", gotBucket, gotKey)
	}
	if gotSize != 100 {
		t.Fatalf("wrong size: %d", gotSize)
	}
	if gotMeta["id"] != "img-1" || gotMeta["format"] != "jpeg" {
		t.Fatalf("metadata not mapped correctly: %+v", gotMeta)
	}
}

func TestJayStorage_MetadataRoundtrip(t *testing.T) {
	orig := &ImageMetadata{
		ID: "id-x", OriginalName: "a.png", Format: "png",
		Width: 1920, Height: 1080, ContentType: "image/png",
		MaxAge: 3600, SMaxAge: 7200,
		CreatedAt: time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC),
	}
	m := metaToMap(orig)
	got := mapToMeta(m, 42, "etag-x")
	if got.ID != orig.ID || got.Format != orig.Format {
		t.Fatalf("id/format mismatch")
	}
	if got.Width != orig.Width || got.Height != orig.Height {
		t.Fatalf("dimensions mismatch: %dx%d vs %dx%d", got.Width, got.Height, orig.Width, orig.Height)
	}
	if got.MaxAge != orig.MaxAge || got.SMaxAge != orig.SMaxAge {
		t.Fatalf("cache ages mismatch")
	}
	if got.Size != 42 || got.ETag != "etag-x" {
		t.Fatalf("size/etag not injected")
	}
	if !got.CreatedAt.Equal(orig.CreatedAt) {
		t.Fatalf("createdAt mismatch: %v vs %v", got.CreatedAt, orig.CreatedAt)
	}
	if got.OriginalName != orig.OriginalName {
		t.Fatalf("OriginalName mismatch: %q vs %q", got.OriginalName, orig.OriginalName)
	}
	if got.ContentType != orig.ContentType {
		t.Fatalf("ContentType mismatch: %q vs %q", got.ContentType, orig.ContentType)
	}
}

func TestJayStorage_Retrieve_Success(t *testing.T) {
	fc := &fakeJayClient{
		getFn: func(ctx context.Context, bucket, key string) (*jayclient.GetResult, error) {
			return &jayclient.GetResult{
				ContentType: "image/png", Size: 123, ETag: "etag-r",
				Metadata: metaToMap(&ImageMetadata{
					ID: "r-1", Format: "png", Width: 10, Height: 20,
					ContentType: "image/png", CreatedAt: time.Now().UTC(),
				}),
				Body: io.NopCloser(bytes.NewReader([]byte("data"))),
			}, nil
		},
	}
	js := newJayStorageWithClient(fc, "falco-images")
	rc, meta, err := js.Retrieve(context.Background(), "r-1")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	defer rc.Close()
	if meta.Format != "png" || meta.Width != 10 || meta.Size != 123 {
		t.Fatalf("bad meta: %+v", meta)
	}
}

func TestJayStorage_Retrieve_NotFound(t *testing.T) {
	fc := &fakeJayClient{
		getFn: func(ctx context.Context, bucket, key string) (*jayclient.GetResult, error) {
			return nil, &jayclient.Error{Code: "NoSuchKey", Message: "not found"}
		},
	}
	js := newJayStorageWithClient(fc, "falco-images")
	_, _, err := js.Retrieve(context.Background(), "nope")
	if !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("expected ErrImageNotFound, got %v", err)
	}
}

func TestJayStorage_Exists_TrueFalse(t *testing.T) {
	exists := &fakeJayClient{
		headFn: func(ctx context.Context, bucket, key string) (*jayclient.ObjectInfo, error) {
			return &jayclient.ObjectInfo{Size: 1}, nil
		},
	}
	missing := &fakeJayClient{
		headFn: func(ctx context.Context, bucket, key string) (*jayclient.ObjectInfo, error) {
			return nil, &jayclient.Error{Code: "NoSuchKey"}
		},
	}

	if ok, _ := newJayStorageWithClient(exists, "bk").Exists(context.Background(), "k"); !ok {
		t.Fatal("expected exists=true")
	}
	ok, err := newJayStorageWithClient(missing, "bk").Exists(context.Background(), "k")
	if err != nil {
		t.Fatalf("unexpected err on missing: %v", err)
	}
	if ok {
		t.Fatal("expected exists=false")
	}
}

func TestJayStorage_Delete(t *testing.T) {
	called := false
	fc := &fakeJayClient{delFn: func(ctx context.Context, bucket, key string) error { called = true; return nil }}
	if err := newJayStorageWithClient(fc, "bk").Delete(context.Background(), "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !called {
		t.Fatal("DeleteObject not called")
	}

	missing := &fakeJayClient{delFn: func(ctx context.Context, bucket, key string) error {
		return &jayclient.Error{Code: "NoSuchKey"}
	}}
	if err := newJayStorageWithClient(missing, "bk").Delete(context.Background(), "k"); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("expected ErrImageNotFound on missing delete, got %v", err)
	}
}

func TestJayStorage_List(t *testing.T) {
	fc := &fakeJayClient{
		listFn: func(ctx context.Context, bucket string, opts *jayclient.ListOptions) (*jayclient.ListResult, error) {
			if opts.Prefix != "pfx/" {
				t.Fatalf("wrong prefix: %s", opts.Prefix)
			}
			return &jayclient.ListResult{
				Objects: []jayclient.ListEntry{
					{Key: "pfx/a", Size: 10, LastModified: time.Now().UTC().Format(time.RFC3339)},
					{Key: "pfx/b", Size: 20, LastModified: time.Now().UTC().Format(time.RFC3339)},
				},
			}, nil
		},
	}
	out, err := newJayStorageWithClient(fc, "bk").List(context.Background(), "pfx/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 2 || out[0].Key != "pfx/a" || out[1].Size != 20 {
		t.Fatalf("bad list: %+v", out)
	}
}

func TestJayStorage_Health(t *testing.T) {
	ok := &fakeJayClient{hBucket: func(ctx context.Context, name string) (*jayclient.BucketInfo, error) {
		return &jayclient.BucketInfo{Name: name}, nil
	}}
	if err := newJayStorageWithClient(ok, "bk").Health(context.Background()); err != nil {
		t.Fatalf("expected healthy, got %v", err)
	}

	bad := &fakeJayClient{hBucket: func(ctx context.Context, name string) (*jayclient.BucketInfo, error) {
		return nil, errors.New("dial tcp: connection refused")
	}}
	if err := newJayStorageWithClient(bad, "bk").Health(context.Background()); err == nil {
		t.Fatal("expected unhealthy")
	}
}

// TestJayStorage_ContextReachesClient pins the reason jay was bumped to a
// client that takes a context: before it, the caller's ctx stopped at this
// layer and the 30 s timeout around Retrieve in the delivery handler cancelled
// nothing. Every operation must hand the exact ctx it received to the client.
func TestJayStorage_ContextReachesClient(t *testing.T) {
	type ctxKey struct{}
	want := context.WithValue(context.Background(), ctxKey{}, "marker")
	seen := func(t *testing.T, op string, got context.Context) {
		t.Helper()
		if got == nil || got.Value(ctxKey{}) != "marker" {
			t.Fatalf("%s: client did not receive the caller's context", op)
		}
	}
	fc := &fakeJayClient{
		putFn: func(ctx context.Context, _, _ string, data io.Reader, _ int64, _ *jayclient.PutOptions) (*jayclient.PutResult, error) {
			seen(t, "PutObject", ctx)
			_, _ = io.Copy(io.Discard, data)
			return &jayclient.PutResult{}, nil
		},
		getFn: func(ctx context.Context, _, _ string) (*jayclient.GetResult, error) {
			seen(t, "GetObject", ctx)
			return &jayclient.GetResult{Body: io.NopCloser(bytes.NewReader(nil)), Metadata: map[string]string{}}, nil
		},
		headFn: func(ctx context.Context, _, _ string) (*jayclient.ObjectInfo, error) {
			seen(t, "HeadObject", ctx)
			return &jayclient.ObjectInfo{}, nil
		},
		delFn: func(ctx context.Context, _, _ string) error {
			seen(t, "DeleteObject", ctx)
			return nil
		},
		listFn: func(ctx context.Context, _ string, _ *jayclient.ListOptions) (*jayclient.ListResult, error) {
			seen(t, "ListObjects", ctx)
			return &jayclient.ListResult{}, nil
		},
		hBucket: func(ctx context.Context, _ string) (*jayclient.BucketInfo, error) {
			seen(t, "HeadBucket", ctx)
			return &jayclient.BucketInfo{}, nil
		},
	}
	js := newJayStorageWithClient(fc, "bk")

	if err := js.Store(want, "k", bytes.NewReader([]byte("x")), &ImageMetadata{Size: 1}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, _, err := js.Retrieve(want, "k"); err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if _, err := js.Exists(want, "k"); err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if err := js.Delete(want, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := js.List(want, "pfx"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := js.Health(want); err != nil {
		t.Fatalf("Health: %v", err)
	}
}

func TestJayStorage_GetStats_HTTP(t *testing.T) {
	// NewTestServer (Go 1.27): limpieza automática y el test falla si el
	// handler paniquea. Start() lo deja en loopback porque GetStats arma su
	// propio http.Client contra adminAddr y no podría hablar con la red
	// in-memory.
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_stats/falco-images" {
			http.Error(w, "wrong path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tid-1:secret-1" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bucket": "falco-images", "object_count": 7, "total_size_bytes": 1024,
		})
	}))
	srv.Start()

	js := &JayStorage{
		bucket:    "falco-images",
		adminAddr: srv.URL, // httptest URL like http://127.0.0.1:XXXX
		tokenID:   "tid-1",
		tokenSec:  "secret-1",
	}
	stats, err := js.GetStats(context.Background())
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.TotalImages != 7 || stats.TotalSize != 1024 {
		t.Fatalf("bad stats: %+v", stats)
	}
}
