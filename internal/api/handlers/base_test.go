package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The CORS middleware adds `Vary: Origin` as its own header line; the
// compression middleware adds `Vary: Accept-Encoding` from WriteHeader.
// dropOriginVary has to drop only the first without disturbing the second.
func TestDropOriginVary(t *testing.T) {
	tests := []struct {
		name string
		vary []string
		want []string
	}{
		{
			name: "removes the lone Origin token",
			vary: []string{"Origin"},
			want: nil,
		},
		{
			name: "keeps Accept-Encoding on a separate line",
			vary: []string{"Origin", "Accept-Encoding"},
			want: []string{"Accept-Encoding"},
		},
		{
			name: "splits a comma-joined value",
			vary: []string{"Origin, Accept-Encoding"},
			want: []string{"Accept-Encoding"},
		},
		{
			name: "matches the token case-insensitively",
			vary: []string{"origin"},
			want: nil,
		},
		{
			name: "keeps the preflight Access-Control tokens",
			vary: []string{"Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers"},
			want: []string{"Access-Control-Request-Method", "Access-Control-Request-Headers"},
		},
		{
			name: "leaves an unrelated Vary alone",
			vary: []string{"Accept-Encoding"},
			want: []string{"Accept-Encoding"},
		},
		{
			name: "is a no-op without a Vary header",
			vary: nil,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			for _, v := range tt.vary {
				w.Header().Add("Vary", v)
			}

			dropOriginVary(w)

			assert.Equal(t, tt.want, valuesOrNil(w.Header(), "Vary"))
		})
	}
}

// A response that never had a Vary header must not gain an empty one.
func TestDropOriginVary_DoesNotCreateEmptyHeader(t *testing.T) {
	w := httptest.NewRecorder()

	dropOriginVary(w)

	_, present := w.Header()["Vary"]
	assert.False(t, present, "Vary should stay absent")
}

func valuesOrNil(h http.Header, key string) []string {
	values := h.Values(key)
	if len(values) == 0 {
		return nil
	}
	return values
}

func TestNotModified(t *testing.T) {
	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	const etag = `"abc-10-0"`
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"no conditional headers", nil, false},
		{"exact match", map[string]string{"If-None-Match": etag}, true},
		{"weak match", map[string]string{"If-None-Match": `W/"abc-10-0"`}, true},
		{"one of a list", map[string]string{"If-None-Match": `"x", "abc-10-0"`}, true},
		{"star", map[string]string{"If-None-Match": "*"}, true},
		{"mismatch", map[string]string{"If-None-Match": `"other"`}, false},
		{"inm wins over ims", map[string]string{"If-None-Match": `"other"`, "If-Modified-Since": modified.Format(http.TimeFormat)}, false},
		{"not modified since", map[string]string{"If-Modified-Since": modified.Format(http.TimeFormat)}, true},
		{"modified since", map[string]string{"If-Modified-Since": modified.Add(-time.Hour).Format(http.TimeFormat)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			assert.Equal(t, tc.want, notModified(r, etag, modified))
		})
	}
	// Cached renders carry the epoch; a date check against it means nothing.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("If-Modified-Since", modified.Format(http.TimeFormat))
	assert.False(t, notModified(r, etag, time.Unix(0, 0)))
}
