package storage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
)

// TestS3Retrieve_BodyOutlivesTheCall guards the bug where Retrieve cancelled
// its request context on return: net/http reads the body under that context,
// so every object larger than what was already buffered came back truncated.
func TestS3Retrieve_BodyOutlivesTheCall(t *testing.T) {
	payload := bytes.Repeat([]byte("falco"), 1<<20) // 5 MiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)
		// Flushed in pieces so most of the body is still on the wire when
		// Retrieve returns.
		for chunk := range slices.Chunk(payload, 64<<10) {
			_, _ = w.Write(chunk)
			w.(http.Flusher).Flush()
		}
	}))
	defer srv.Close()
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	s, err := NewS3Storage(&S3Config{Bucket: "b", Region: "us-east-1", Endpoint: srv.URL, AccessKey: "a", SecretKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	rc, meta, err := s.Retrieve(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, rc)
	if cerr := rc.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	if err != nil || n != int64(len(payload)) {
		t.Fatalf("read %d of %d bytes: %v", n, len(payload), err)
	}
	if meta.Size != int64(len(payload)) {
		t.Fatalf("meta.Size = %d, want %d", meta.Size, len(payload))
	}
}
