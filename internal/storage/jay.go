package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	jayclient "github.com/ivangsm/jay/proto/client"
)

// JayConfig configures a JayStorage backend.
type JayConfig struct {
	Addr      string // native protocol, e.g. "jay:4012"
	AdminAddr string // HTTP for GetStats (same port as S3 API), e.g. "jay:4010"
	TokenID   string
	Secret    string
	Bucket    string
	PoolSize  int
}

// jayClientIface is the minimal surface of jayclient.Client that JayStorage uses.
// It exists solely so tests can swap in a fake.
type jayClientIface interface {
	PutObject(bucket, key string, data io.Reader, size int64, opts *jayclient.PutOptions) (*jayclient.PutResult, error)
	GetObject(bucket, key string) (*jayclient.GetResult, error)
	HeadObject(bucket, key string) (*jayclient.ObjectInfo, error)
	DeleteObject(bucket, key string) error
	ListObjects(bucket string, opts *jayclient.ListOptions) (*jayclient.ListResult, error)
	HeadBucket(name string) (*jayclient.BucketInfo, error)
	CreateBucket(name string) (*jayclient.BucketInfo, error)
	Close() error
}

// JayStorage is a Falco StorageBackend backed by Jay's native protocol.
type JayStorage struct {
	client    jayClientIface
	bucket    string
	adminAddr string // "http://host:port" for stats calls (normalized in NewJayStorage)
	tokenID   string
	tokenSec  string
}

// NewJayStorage dials Jay, ensures the bucket exists, and returns a backend.
func NewJayStorage(cfg *JayConfig) (*JayStorage, error) {
	if cfg == nil || cfg.Addr == "" || cfg.TokenID == "" || cfg.Secret == "" || cfg.Bucket == "" {
		return nil, fmt.Errorf("%w: jay: addr/token_id/secret/bucket are required", ErrInvalidConfiguration)
	}
	pool := cfg.PoolSize
	if pool <= 0 {
		pool = 4
	}
	c, err := jayclient.Dial(cfg.Addr, cfg.TokenID, cfg.Secret, pool)
	if err != nil {
		return nil, fmt.Errorf("jay: dial %s: %w", cfg.Addr, err)
	}

	// Normalize adminAddr: callers may pass "host:port" or "http://host:port"
	admin := cfg.AdminAddr
	if admin != "" && !strings.HasPrefix(admin, "http://") && !strings.HasPrefix(admin, "https://") {
		admin = "http://" + admin
	}

	js := &JayStorage{
		client:    c,
		bucket:    cfg.Bucket,
		adminAddr: admin,
		tokenID:   cfg.TokenID,
		tokenSec:  cfg.Secret,
	}

	// Ensure the bucket exists (idempotent).
	if _, err := c.CreateBucket(cfg.Bucket); err != nil && !isBucketAlreadyExists(err) {
		// HeadBucket fallback — if CreateBucket says it exists we're fine.
		if _, headErr := c.HeadBucket(cfg.Bucket); headErr != nil {
			_ = c.Close()
			return nil, fmt.Errorf("jay: ensure bucket %q: create: %w; head: %v", cfg.Bucket, err, headErr)
		}
	}
	return js, nil
}

func isBucketAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	var je *jayclient.Error
	if errors.As(err, &je) {
		return je.Code == "BucketAlreadyExists" || je.Code == "BucketAlreadyOwnedByYou"
	}
	return false
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var je *jayclient.Error
	if errors.As(err, &je) {
		return je.Code == "NoSuchKey" || je.Code == "NoSuchBucket" || je.Code == "NotFound"
	}
	return false
}

// Store uploads an object.
func (s *JayStorage) Store(ctx context.Context, key string, data io.Reader, metadata *ImageMetadata) error {
	if metadata == nil {
		return fmt.Errorf("jay: nil metadata")
	}
	metadata.StorageKey = key
	if metadata.CreatedAt.IsZero() {
		metadata.CreatedAt = time.Now().UTC()
	}
	opts := &jayclient.PutOptions{
		ContentType: metadata.ContentType,
		Metadata:    metaToMap(metadata),
	}
	res, err := s.client.PutObject(s.bucket, key, data, metadata.Size, opts)
	if err != nil {
		return fmt.Errorf("jay: put %s: %w", key, err)
	}
	metadata.ETag = res.ETag
	return nil
}

// Retrieve downloads an object and reconstructs metadata from Jay headers.
func (s *JayStorage) Retrieve(ctx context.Context, key string) (io.ReadCloser, *ImageMetadata, error) {
	res, err := s.client.GetObject(s.bucket, key)
	if err != nil {
		if isNotFound(err) {
			return nil, nil, ErrImageNotFound
		}
		return nil, nil, fmt.Errorf("jay: get %s: %w", key, err)
	}
	meta := mapToMeta(res.Metadata, res.Size, res.ETag)
	meta.StorageKey = key
	if meta.ContentType == "" {
		meta.ContentType = res.ContentType
	}
	return res.Body, meta, nil
}

// Exists checks for presence via HeadObject.
func (s *JayStorage) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(s.bucket, key)
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("jay: head %s: %w", key, err)
}

// Delete removes an object.
func (s *JayStorage) Delete(ctx context.Context, key string) error {
	if err := s.client.DeleteObject(s.bucket, key); err != nil {
		if isNotFound(err) {
			return ErrImageNotFound
		}
		return fmt.Errorf("jay: delete %s: %w", key, err)
	}
	return nil
}

// List paginates objects under prefix.
func (s *JayStorage) List(ctx context.Context, prefix string) ([]ListResult, error) {
	res, err := s.client.ListObjects(s.bucket, &jayclient.ListOptions{Prefix: prefix, MaxKeys: 1000})
	if err != nil {
		return nil, fmt.Errorf("jay: list %s: %w", prefix, err)
	}
	out := make([]ListResult, 0, len(res.Objects))
	for _, o := range res.Objects {
		modified, _ := time.Parse(time.RFC3339, o.LastModified)
		out = append(out, ListResult{Key: o.Key, Size: o.Size, Modified: modified})
	}
	return out, nil
}

// Health pings the bucket via HeadBucket.
func (s *JayStorage) Health(ctx context.Context) error {
	_, err := s.client.HeadBucket(s.bucket)
	if err != nil {
		return fmt.Errorf("jay: unhealthy: %w", err)
	}
	return nil
}

// GetStats calls the Jay admin HTTP endpoint GET /_stats/{name}.
// Authenticates with the same token used for the native protocol.
func (s *JayStorage) GetStats(ctx context.Context) (*StorageStats, error) {
	if s.adminAddr == "" {
		return &StorageStats{}, nil
	}
	url := strings.TrimRight(s.adminAddr, "/") + "/_stats/" + s.bucket
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("jay: stats request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.tokenID+":"+s.tokenSec)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jay: stats call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("jay: stats http %d: %s", resp.StatusCode, string(b))
	}
	var body struct {
		Bucket         string `json:"bucket"`
		ObjectCount    int64  `json:"object_count"`
		TotalSizeBytes int64  `json:"total_size_bytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("jay: stats decode: %w", err)
	}
	return &StorageStats{TotalImages: body.ObjectCount, TotalSize: body.TotalSizeBytes}, nil
}

// --- metadata mapping ---

func metaToMap(m *ImageMetadata) map[string]string {
	out := map[string]string{
		"id":            m.ID,
		"original-name": m.OriginalName,
		"format":        m.Format,
		"width":         strconv.Itoa(m.Width),
		"height":        strconv.Itoa(m.Height),
		"content-type":  m.ContentType,
		"created-at":    m.CreatedAt.Format(time.RFC3339),
	}
	if m.MaxAge > 0 {
		out["maxage"] = strconv.Itoa(m.MaxAge)
	}
	if m.SMaxAge > 0 {
		out["smaxage"] = strconv.Itoa(m.SMaxAge)
	}
	return out
}

func mapToMeta(m map[string]string, size int64, etag string) *ImageMetadata {
	im := &ImageMetadata{
		ID:           m["id"],
		OriginalName: m["original-name"],
		Format:       m["format"],
		ContentType:  m["content-type"],
		Size:         size,
		ETag:         etag,
	}
	if w, err := strconv.Atoi(m["width"]); err == nil {
		im.Width = w
	}
	if h, err := strconv.Atoi(m["height"]); err == nil {
		im.Height = h
	}
	if ma, err := strconv.Atoi(m["maxage"]); err == nil {
		im.MaxAge = ma
	}
	if sm, err := strconv.Atoi(m["smaxage"]); err == nil {
		im.SMaxAge = sm
	}
	if t, err := time.Parse(time.RFC3339, m["created-at"]); err == nil {
		im.CreatedAt = t
	}
	return im
}
