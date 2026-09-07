package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	jayclient "github.com/ivangsm/jay/proto/client"
)

// --- pageFromListing: the shared slicer used by backends that cannot page ---

func listing(keys ...string) []ListResult {
	out := make([]ListResult, 0, len(keys))
	for _, k := range keys {
		out = append(out, ListResult{Key: k, Size: 1})
	}
	return out
}

func keysOf(objs []ListResult) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Key)
	}
	return out
}

func eq(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", label, got, want)
		}
	}
}

func TestPageFromListing_PagesWithCursor(t *testing.T) {
	all := listing("a", "b", "c", "d", "e")

	first := pageFromListing(all, ListOptions{MaxKeys: 2})
	eq(t, "first page", keysOf(first.Objects), []string{"a", "b"})
	if !first.IsTruncated {
		t.Fatal("first page must report truncation: there are 5 keys and the page holds 2")
	}
	if first.NextCursor != "b" {
		t.Fatalf("next cursor: got %q, want %q", first.NextCursor, "b")
	}

	second := pageFromListing(all, ListOptions{MaxKeys: 2, Cursor: first.NextCursor})
	eq(t, "second page", keysOf(second.Objects), []string{"c", "d"})

	third := pageFromListing(all, ListOptions{MaxKeys: 2, Cursor: second.NextCursor})
	eq(t, "third page", keysOf(third.Objects), []string{"e"})
	if third.IsTruncated {
		t.Fatal("last page must not report truncation")
	}
	if third.NextCursor != "" {
		t.Fatalf("last page must not hand out a cursor, got %q", third.NextCursor)
	}
}

func TestPageFromListing_DelimiterRollsUpNestedFolders(t *testing.T) {
	// The old dashboard split on the first "/" and dropped anything deeper, so
	// avatars/2024/x was unreachable: no folder entry, and not in the listing.
	all := listing("root.webp", "avatars/a.webp", "avatars/2024/deep.webp", "logos/b.webp")

	page := pageFromListing(all, ListOptions{Delimiter: "/"})
	eq(t, "top-level objects", keysOf(page.Objects), []string{"root.webp"})
	eq(t, "top-level folders", page.CommonPrefixes, []string{"avatars/", "logos/"})

	// Descending one level must expose the nested folder, not swallow it.
	nested := pageFromListing(all, ListOptions{Prefix: "avatars/", Delimiter: "/"})
	eq(t, "nested objects", keysOf(nested.Objects), []string{"avatars/a.webp"})
	eq(t, "nested folders", nested.CommonPrefixes, []string{"avatars/2024/"})

	deep := pageFromListing(all, ListOptions{Prefix: "avatars/2024/", Delimiter: "/"})
	eq(t, "deep objects", keysOf(deep.Objects), []string{"avatars/2024/deep.webp"})
}

func TestPageFromListing_PrefixIsMatchedVerbatim(t *testing.T) {
	// S3 used to append a trailing slash and jay did not, so the same prefix
	// listed differently depending on the backend.
	all := listing("img.webp", "img/nested.webp")

	page := pageFromListing(all, ListOptions{Prefix: "img"})
	eq(t, "verbatim prefix", keysOf(page.Objects), []string{"img.webp", "img/nested.webp"})
}

// --- filesystem ---

func TestFilesystemStorage_ListPage(t *testing.T) {
	fs, err := NewFilesystemStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStorage: %v", err)
	}
	ctx := context.Background()

	for _, key := range []string{"a.webp", "b.webp", "nested/c.webp"} {
		meta := &ImageMetadata{ID: key, Format: "webp", ContentType: "image/webp", CreatedAt: time.Now().UTC()}
		if err := fs.Store(ctx, key, strings.NewReader("payload"), meta); err != nil {
			t.Fatalf("Store %s: %v", key, err)
		}
	}

	page, err := fs.ListPage(ctx, ListOptions{Delimiter: "/"})
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	eq(t, "objects", keysOf(page.Objects), []string{"a.webp", "b.webp"})
	eq(t, "folders", page.CommonPrefixes, []string{"nested/"})

	one, err := fs.ListPage(ctx, ListOptions{MaxKeys: 1})
	if err != nil {
		t.Fatalf("ListPage(MaxKeys=1): %v", err)
	}
	if len(one.Objects) != 1 || !one.IsTruncated {
		t.Fatalf("expected a truncated single-object page, got %d objects truncated=%v",
			len(one.Objects), one.IsTruncated)
	}
}

// --- jay ---

func TestJayStorage_ListPage_ForwardsCursorAndDelimiter(t *testing.T) {
	var seen jayclient.ListOptions
	fc := &fakeJayClient{
		listFn: func(_ string, opts *jayclient.ListOptions) (*jayclient.ListResult, error) {
			seen = *opts
			return &jayclient.ListResult{
				Objects: []jayclient.ListEntry{{
					Key:          "pfx/a",
					Size:         10,
					ETag:         "etag-a",
					ContentType:  "image/webp",
					LastModified: "2026-01-02T03:04:05Z",
				}},
				CommonPrefixes: []string{"pfx/sub/"},
				IsTruncated:    true,
				NextStartAfter: "pfx/a",
			}, nil
		},
	}

	page, err := newJayStorageWithClient(fc, "bk").ListPage(context.Background(), ListOptions{
		Prefix: "pfx/", Delimiter: "/", Cursor: "pfx/0", MaxKeys: 50,
	})
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}

	if seen.Delimiter != "/" || seen.StartAfter != "pfx/0" || seen.MaxKeys != 50 {
		t.Fatalf("options not forwarded to jay: %+v", seen)
	}
	if !page.IsTruncated || page.NextCursor != "pfx/a" {
		t.Fatalf("truncation dropped: truncated=%v cursor=%q", page.IsTruncated, page.NextCursor)
	}
	eq(t, "common prefixes", page.CommonPrefixes, []string{"pfx/sub/"})

	// jay carries these on the wire and they used to be thrown away.
	if page.Objects[0].ContentType != "image/webp" || page.Objects[0].ETag != "etag-a" {
		t.Fatalf("content type / etag dropped: %+v", page.Objects[0])
	}
	if page.Objects[0].Modified.IsZero() {
		t.Fatal("LastModified was not parsed")
	}
}

func TestJayStorage_List_FollowsPaginationBeyondOnePage(t *testing.T) {
	// The regression this guards: List asked for one page of 1000 and dropped
	// IsTruncated, so a bucket with more than that listed short and said
	// nothing. 2500 keys is the smallest size that needs three pages.
	const total = 2500
	fc := &fakeJayClient{
		listFn: func(_ string, opts *jayclient.ListOptions) (*jayclient.ListResult, error) {
			start := 0
			if opts.StartAfter != "" {
				var n int
				if _, err := fmt.Sscanf(opts.StartAfter, "k%06d", &n); err != nil {
					return nil, fmt.Errorf("bad cursor %q", opts.StartAfter)
				}
				start = n + 1
			}
			end := min(start+opts.MaxKeys, total)

			res := &jayclient.ListResult{}
			for i := start; i < end; i++ {
				res.Objects = append(res.Objects, jayclient.ListEntry{
					Key:          fmt.Sprintf("k%06d", i),
					Size:         1,
					LastModified: "2026-01-02T03:04:05Z",
				})
			}
			if end < total {
				res.IsTruncated = true
				res.NextStartAfter = fmt.Sprintf("k%06d", end-1)
			}
			return res, nil
		},
	}

	out, err := newJayStorageWithClient(fc, "bk").List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != total {
		t.Fatalf("List truncated silently: got %d keys, want %d", len(out), total)
	}
}

func TestJayStorage_List_StopsOnStalledCursor(t *testing.T) {
	// A backend that claims truncation without advancing must not loop forever.
	calls := 0
	fc := &fakeJayClient{
		listFn: func(_ string, _ *jayclient.ListOptions) (*jayclient.ListResult, error) {
			calls++
			if calls > 10 {
				t.Fatal("List looped on a cursor that never advances")
			}
			return &jayclient.ListResult{
				Objects:        []jayclient.ListEntry{{Key: "stuck", LastModified: "2026-01-02T03:04:05Z"}},
				IsTruncated:    true,
				NextStartAfter: "stuck",
			}, nil
		},
	}

	out, err := newJayStorageWithClient(fc, "bk").List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected the stalled cursor to stop after the repeat, got %d", len(out))
	}
}
