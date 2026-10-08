package views

import (
	"net/url"
	"strings"
)

// explorerURL builds a link into the storage explorer.
//
// Every parameter goes through url.Values, so a bucket or prefix containing an
// ampersand cannot smuggle a second parameter into the link.
func explorerURL(bucket, prefix, cursor, stack string) string {
	q := url.Values{}
	if bucket != "" {
		q.Set("bucket", bucket)
	}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if stack != "" {
		q.Set("stack", stack)
	}
	if len(q) == 0 {
		return "/ui/explorer"
	}
	return "/ui/explorer?" + q.Encode()
}

// DashboardURL is the full-page equivalent of explorerURL, used for links that
// are real navigations rather than HTMX swaps.
func DashboardURL(bucket, prefix string) string {
	q := url.Values{}
	if bucket != "" {
		q.Set("bucket", bucket)
	}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if len(q) == 0 {
		return "/dashboard"
	}
	return "/dashboard?" + q.Encode()
}

// objectURL links to one object's detail page.
func objectURL(bucket, key string) string {
	q := url.Values{}
	q.Set("bucket", bucket)
	q.Set("key", key)
	return "/object?" + q.Encode()
}

// playgroundURL opens the playground on a given object.
func playgroundURL(bucket, key string) string {
	q := url.Values{}
	if bucket != "" {
		q.Set("bucket", bucket)
	}
	if key != "" {
		q.Set("key", key)
	}
	if len(q) == 0 {
		return "/playground"
	}
	return "/playground?" + q.Encode()
}

// Cursor navigation is stateless: the cursors already visited travel in the URL
// as a comma-separated stack, so "Previous" works without the server keeping
// per-session paging state.
//
// Cursors are backend-opaque and may contain anything, so each one is
// percent-encoded before joining — otherwise a key with a comma in it would
// split into two bogus entries.

// pushCursor appends a cursor to the stack.
func pushCursor(stack, cursor string) string {
	if cursor == "" {
		return stack
	}
	encoded := url.QueryEscape(cursor)
	if stack == "" {
		return encoded
	}
	return stack + "," + encoded
}

// popCursor drops the last entry and returns the stack for the previous page.
func popCursor(stack string) string {
	if stack == "" {
		return ""
	}
	idx := strings.LastIndex(stack, ",")
	if idx < 0 {
		return ""
	}
	return stack[:idx]
}
