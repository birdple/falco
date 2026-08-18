package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
