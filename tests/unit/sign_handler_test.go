package unit

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/birdple/falco/internal/api/handlers"
	"github.com/birdple/falco/tests/mocks"
)

func signHandler(t *testing.T) *handlers.Handler {
	t.Helper()
	cfg := testConfig()
	// The key is hex and the signature size is the real default (32); with
	// either one wrong, SignURL returns 500 and the test would prove nothing.
	cfg.Security.HMACKey = "6465616462656566303132333435363738396162636465663031323334353637"
	cfg.Security.HMACSignatureSize = 32
	return handlers.NewHandler(cfg, new(mocks.MockStorageBackend), newProcessorMock(), time.Now())
}

func postSign(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sign", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	signHandler(t).HandleSignURL(rec, req)
	return rec
}

// A decode error and a missing path are different causes and have to be
// reported differently: json/v2 is case-sensitive and rejects unknown fields,
// so a caller that did send path can get a decode error.
func TestHandleSignURL_DecodeErrorIsNotDisguisedAsMissingPath(t *testing.T) {
	cases := []struct {
		name string
		body string
		code string
		why  string
	}{
		{
			name: "unknown field",
			body: `{"path":"/api/v1/images/abc","expires_in":600,"made_up_field":1}`,
			code: "INVALID_JSON",
			why:  "jsonx.Strict rejects the whole document; path was present",
		},
		{
			name: "capitalization that does not match the tag",
			body: `{"Path":"/api/v1/images/abc","expires_in":600}`,
			code: "INVALID_JSON",
			why:  "json/v2 is case-sensitive where v1 was not",
		},
		{
			name: "malformed json",
			body: `{"path":`,
			code: "INVALID_JSON",
			why:  "not a document",
		},
		{
			name: "path truly missing",
			body: `{"expires_in":600}`,
			code: "INVALID_REQUEST",
			why:  "this is the case 'path is required' actually describes",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := postSign(t, c.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), c.code, c.why)
		})
	}
}

func TestHandleSignURL_ValidBodyReturnsSignedURL(t *testing.T) {
	rec := postSign(t, `{"path":"/api/v1/images/abc","expires_in":600}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"signed_url"`)
	assert.Contains(t, rec.Body.String(), "sig=")
}
