package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	jayclient "github.com/ivangsm/jay/proto/client"
)

// fakeJayClient implements jayClientIface for testing.
type fakeJayClient struct {
	putFn    func(bucket, key string, data io.Reader, size int64, opts *jayclient.PutOptions) (*jayclient.PutResult, error)
	getFn    func(bucket, key string) (*jayclient.GetResult, error)
	headFn   func(bucket, key string) (*jayclient.ObjectInfo, error)
	delFn    func(bucket, key string) error
	listFn   func(bucket string, opts *jayclient.ListOptions) (*jayclient.ListResult, error)
	hBucket  func(name string) (*jayclient.BucketInfo, error)
	cBucket  func(name string) (*jayclient.BucketInfo, error)
	closeErr error
}

func (f *fakeJayClient) PutObject(bucket, key string, data io.Reader, size int64, opts *jayclient.PutOptions) (*jayclient.PutResult, error) {
	return f.putFn(bucket, key, data, size, opts)
}
func (f *fakeJayClient) GetObject(bucket, key string) (*jayclient.GetResult, error) {
	return f.getFn(bucket, key)
}
func (f *fakeJayClient) HeadObject(bucket, key string) (*jayclient.ObjectInfo, error) {
	return f.headFn(bucket, key)
}
func (f *fakeJayClient) DeleteObject(bucket, key string) error { return f.delFn(bucket, key) }
func (f *fakeJayClient) ListObjects(bucket string, opts *jayclient.ListOptions) (*jayclient.ListResult, error) {
	return f.listFn(bucket, opts)
}
func (f *fakeJayClient) HeadBucket(name string) (*jayclient.BucketInfo, error) {
	return f.hBucket(name)
}
func (f *fakeJayClient) CreateBucket(name string) (*jayclient.BucketInfo, error) {
	return f.cBucket(name)
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
		putFn: func(bucket, key string, data io.Reader, size int64, opts *jayclient.PutOptions) (*jayclient.PutResult, error) {
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
		getFn: func(bucket, key string) (*jayclient.GetResult, error) {
			return &jayclient.GetResult{
				ObjectInfo: jayclient.ObjectInfo{
					ContentType: "image/png", Size: 123, ETag: "etag-r",
					Metadata: metaToMap(&ImageMetadata{
						ID: "r-1", Format: "png", Width: 10, Height: 20,
						ContentType: "image/png", CreatedAt: time.Now().UTC(),
					}),
				},
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
		getFn: func(bucket, key string) (*jayclient.GetResult, error) {
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
		headFn: func(bucket, key string) (*jayclient.ObjectInfo, error) {
			return &jayclient.ObjectInfo{Size: 1}, nil
		},
	}
	missing := &fakeJayClient{
		headFn: func(bucket, key string) (*jayclient.ObjectInfo, error) {
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
	fc := &fakeJayClient{delFn: func(bucket, key string) error { called = true; return nil }}
	if err := newJayStorageWithClient(fc, "bk").Delete(context.Background(), "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !called {
		t.Fatal("DeleteObject not called")
	}

	missing := &fakeJayClient{delFn: func(bucket, key string) error {
		return &jayclient.Error{Code: "NoSuchKey"}
	}}
	if err := newJayStorageWithClient(missing, "bk").Delete(context.Background(), "k"); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("expected ErrImageNotFound on missing delete, got %v", err)
	}
}

func TestJayStorage_List(t *testing.T) {
	fc := &fakeJayClient{
		listFn: func(bucket string, opts *jayclient.ListOptions) (*jayclient.ListResult, error) {
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
	ok := &fakeJayClient{hBucket: func(name string) (*jayclient.BucketInfo, error) {
		return &jayclient.BucketInfo{Name: name}, nil
	}}
	if err := newJayStorageWithClient(ok, "bk").Health(context.Background()); err != nil {
		t.Fatalf("expected healthy, got %v", err)
	}

	bad := &fakeJayClient{hBucket: func(name string) (*jayclient.BucketInfo, error) {
		return nil, errors.New("dial tcp: connection refused")
	}}
	if err := newJayStorageWithClient(bad, "bk").Health(context.Background()); err == nil {
		t.Fatal("expected unhealthy")
	}
}

func TestJayStorage_GetStats_HTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_stats/falco-images" {
			http.Error(w, "wrong path: "+r.URL.Path, 404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tid-1:secret-1" {
			http.Error(w, "bad auth", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bucket": "falco-images", "object_count": 7, "total_size_bytes": 1024,
		})
	}))
	defer srv.Close()

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

// This imports package "strconv" elsewhere; keep it referenced to avoid unused.
var _ = strconv.Itoa
