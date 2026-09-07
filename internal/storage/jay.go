package storage

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/birdple/falco/internal/pkg/logger"
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
			return nil, fmt.Errorf("jay: ensure bucket %q: create: %w; head: %w", cfg.Bucket, err, headErr)
		}
	}
	return js, nil
}

func isBucketAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	if je, ok := errors.AsType[*jayclient.Error](err); ok {
		return je.Code == "BucketAlreadyExists" || je.Code == "BucketAlreadyOwnedByYou"
	}
	return false
}

// isJayNotFound reports whether a jay client error means "the object is not
// there". Named for its backend to keep it distinct from the package-level
// IsNotFound, which answers the same question for any backend.
func isJayNotFound(err error) bool {
	if err == nil {
		return false
	}
	if je, ok := errors.AsType[*jayclient.Error](err); ok {
		return je.Code == "NoSuchKey" || je.Code == "NoSuchBucket" || je.Code == "NotFound"
	}
	return false
}

// Store uploads an object.
func (s *JayStorage) Store(ctx context.Context, key string, data io.Reader, metadata *ImageMetadata) error {
	if metadata == nil {
		return errors.New("jay: nil metadata")
	}
	metadata.StorageKey = key
	if metadata.CreatedAt.IsZero() {
		metadata.CreatedAt = time.Now().UTC()
	}
	opts := &jayclient.PutOptions{
		ContentType: metadata.ContentType,
		Metadata:    metaToMap(metadata),
		// falco never reads or serves jay's MD5 ETag (see serveImage in
		// internal/api/handlers/base.go, which derives its own HTTP ETag from
		// ID+Size+CreatedAt) and never talks to jay's S3 API for these
		// objects, so the S3-compatibility ETag jay would otherwise compute
		// is pure overhead here — profiling in jay showed it costs roughly
		// twice the CPU of the SHA-256 checksum jay always computes anyway.
		SkipETag: true,
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
		if isJayNotFound(err) {
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
	if isJayNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("jay: head %s: %w", key, err)
}

// Delete removes an object.
func (s *JayStorage) Delete(ctx context.Context, key string) error {
	if err := s.client.DeleteObject(s.bucket, key); err != nil {
		if isJayNotFound(err) {
			return ErrImageNotFound
		}
		return fmt.Errorf("jay: delete %s: %w", key, err)
	}
	return nil
}

// List returns every object under prefix, following jay's pagination.
//
// It used to ask for a single page of 1000 and throw IsTruncated away, so a
// bucket with more than that listed short and said nothing. Callers that want
// one page at a time use ListPage instead.
//
// Two things stop the walk before the prefix runs out, and both return an
// error rather than the keys gathered so far: MaxFullListingObjects, and a
// cursor that stops advancing. Returning a short slice would put the caller
// back exactly where the original bug left it — holding an incomplete listing
// that looks complete.
func (s *JayStorage) List(ctx context.Context, prefix string) ([]ListResult, error) {
	var out []ListResult
	cursor := ""
	for {
		page, err := s.ListPage(ctx, ListOptions{Prefix: prefix, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		out = append(out, page.Objects...)
		if !page.IsTruncated {
			return out, nil
		}
		if len(out) >= MaxFullListingObjects {
			return nil, fmt.Errorf("%w: jay: %q holds more than %d objects; list it a page at a time",
				ErrListingTooLarge, prefix, MaxFullListingObjects)
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			// jay says there is more and hands back no way to reach it. Looping
			// on the same cursor would hang the request forever, and stopping
			// quietly would hide that the listing is incomplete — which jay just
			// said it is.
			return nil, fmt.Errorf("jay: listing %q stalled at cursor %q after %d objects: the backend reports more but does not advance",
				prefix, cursor, len(out))
		}
		cursor = page.NextCursor
	}
}

// ListPage returns one page of objects, honouring prefix, delimiter and cursor.
func (s *JayStorage) ListPage(ctx context.Context, opts ListOptions) (*ListPage, error) {
	res, err := s.client.ListObjects(s.bucket, &jayclient.ListOptions{
		Prefix:     opts.Prefix,
		Delimiter:  opts.Delimiter,
		StartAfter: opts.Cursor,
		MaxKeys:    NormalizeMaxKeys(opts.MaxKeys),
	})
	if err != nil {
		return nil, fmt.Errorf("jay: list %s: %w", opts.Prefix, err)
	}

	out := &ListPage{
		Objects:        make([]ListResult, 0, len(res.Objects)),
		CommonPrefixes: res.CommonPrefixes,
		NextCursor:     res.NextStartAfter,
		IsTruncated:    res.IsTruncated,
	}
	for _, o := range res.Objects {
		out.Objects = append(out.Objects, ListResult{
			Key:         o.Key,
			Size:        o.Size,
			Modified:    parseJayTime(o.LastModified, o.Key),
			ContentType: o.ContentType,
			ETag:        o.ETag,
		})
	}
	return out, nil
}

// parseJayTime turns jay's timestamp into a time.Time.
//
// jay answers RFC3339 in Get/Head but "2006-01-02T15:04:05Z" in listings, and
// both parse as RFC3339. The error used to be discarded, which turned an
// unparseable date into a zero time that renders as year 1 — a wrong date is
// worse than a missing one, so log it instead of swallowing it.
func parseJayTime(v, key string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		logger.Warn().Str("value", v).Str("key", key).Msg("jay: unparseable LastModified in listing")
		return time.Time{}
	}
	return t
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
	reqURL := strings.TrimRight(s.adminAddr, "/") + "/_stats/" + url.PathEscape(s.bucket)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("jay: stats request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.tokenID+":"+s.tokenSec)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jay: stats call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("jay: stats http %d: %s", resp.StatusCode, string(b))
	}
	var body struct {
		Bucket         string `json:"bucket"`
		ObjectCount    int64  `json:"object_count"`
		TotalSizeBytes int64  `json:"total_size_bytes"`
	}
	// Plain v2 defaults on purpose, NOT jsonx.Strict: jay is versioned apart
	// from falco, and a new field in its stats response is an additive change.
	// With RejectUnknownMembers that change would break GetStats and, with it,
	// /health.
	if err := jsonv2.UnmarshalRead(resp.Body, &body); err != nil {
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
	if m.OwnerID != "" {
		out["owner-id"] = m.OwnerID
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
		OwnerID:      m["owner-id"],
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
