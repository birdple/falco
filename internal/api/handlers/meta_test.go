package handlers

import (
	jsonv2 "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// metaKeys writes an ObjectMeta through the handler's own JSON writer and
// returns the keys a client would see, so what is asserted is the wire body,
// not what a different encoder would produce.
func metaKeys(t *testing.T, meta ObjectMeta) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, meta)
	require.Equal(t, http.StatusOK, rec.Code)

	var got map[string]any
	require.NoError(t, jsonv2.Unmarshal(rec.Body.Bytes(), &got), rec.Body.String())
	return got
}

// Under encoding/json/v2 `omitempty` keeps a zero int, so a non-image object
// (which has no dimensions) would be described as 0x0 instead of "unknown".
// The tag is `omitzero`, and this pins it.
func TestObjectMeta_OmitsUnknownDimensions(t *testing.T) {
	got := metaKeys(t, ObjectMeta{Success: true, ID: "abc", Size: 12})

	assert.NotContains(t, got, "width", "width must be absent, not 0")
	assert.NotContains(t, got, "height", "height must be absent, not 0")
	assert.Equal(t, float64(12), got["size_bytes"], "size_bytes is never omitted")
}

func TestObjectMeta_KeepsKnownDimensions(t *testing.T) {
	got := metaKeys(t, ObjectMeta{Success: true, ID: "abc", Width: 640, Height: 480})

	assert.Equal(t, float64(640), got["width"])
	assert.Equal(t, float64(480), got["height"])
}
