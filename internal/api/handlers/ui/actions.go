package ui

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/birdple/falco/internal/pkg/logger"
)

// Panel actions do not reimplement anything. They rewrite the browser's request
// into the shape the API expects and hand it to the real API handler, with the
// session's scope already in the context. Ownership rules, size limits, format
// rejection and cache invalidation therefore apply exactly as they would to an
// API client — and the panel cannot do anything the API would refuse.

// deleteRequest is what the browser sends to /ui/objects/delete.
type deleteRequest struct {
	Bucket string   `json:"bucket"`
	Keys   []string `json:"keys"`
}

// DeleteObjects deletes one or more objects.
//
// The old panel issued `hx-delete=/api/v1/delete?id=…&b=…`, but that handler
// reads a strict JSON body and ignores the query string entirely, so every
// single click answered 400 INVALID_JSON — and the client only handled 401, so
// nothing was shown. The user confirmed a permanent delete and absolutely
// nothing happened.
func (h *Handler) DeleteObjects(w http.ResponseWriter, r *http.Request) {
	sess := h.sessionFrom(r)

	var req deleteRequest
	if err := jsonv2.UnmarshalRead(r.Body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Invalid request"})
		return
	}
	if len(req.Keys) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Nothing selected"})
		return
	}

	bucket, err := h.resolveBucket(sess.Scope, req.Bucket)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	body, err := jsonv2.Marshal(map[string]any{"bucket": bucket, "keys": req.Keys})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Could not build the request"})
		return
	}

	inner := httptest.NewRecorder()
	apiReq := r.Clone(r.Context())
	apiReq.Method = http.MethodDelete
	apiReq.URL = &url.URL{Path: "/api/v1/delete"}
	apiReq.Body = io.NopCloser(bytes.NewReader(body))
	apiReq.ContentLength = int64(len(body))
	apiReq.Header = http.Header{"Content-Type": []string{"application/json"}}

	h.api.HandleDelete(inner, apiReq)

	// The API answers 207 when a batch partly fails. That distinction is the
	// whole point and it is passed through untouched: the panel reports what
	// did NOT get deleted instead of a blanket success.
	relayJSON(w, inner)
}

// Upload forwards a multipart upload to the API handler.
//
// The browser cannot send the API key (it never has it), so the panel carries
// the caller's authority in the request context instead.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	sess := h.sessionFrom(r)
	query := r.URL.Query()

	bucket, err := h.resolveBucket(sess.Scope, query.Get("bucket"))
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	apiQuery := url.Values{}
	apiQuery.Set("b", bucket)
	if dir := strings.TrimSpace(query.Get("prefix")); dir != "" {
		apiQuery.Set("d", dir)
	}

	inner := httptest.NewRecorder()
	apiReq := r.Clone(r.Context())
	apiReq.Method = http.MethodPost
	apiReq.URL = &url.URL{Path: "/api/v1/upload", RawQuery: apiQuery.Encode()}

	h.api.HandleUpload(inner, apiReq)
	relayJSON(w, inner)
}

// Purge clears the transform cache.
func (h *Handler) Purge(w http.ResponseWriter, r *http.Request) {
	inner := httptest.NewRecorder()
	apiReq := r.Clone(r.Context())
	apiReq.Method = http.MethodDelete
	apiReq.URL = &url.URL{Path: "/api/v1/cache", RawQuery: r.URL.RawQuery}

	h.api.HandleCachePurge(inner, apiReq)
	relayJSON(w, inner)
}

// SignURL signs a path on behalf of the panel.
func (h *Handler) SignURL(w http.ResponseWriter, r *http.Request) {
	inner := httptest.NewRecorder()
	apiReq := r.Clone(r.Context())
	apiReq.Method = http.MethodPost
	apiReq.URL = &url.URL{Path: "/api/v1/sign"}

	h.api.HandleSignURL(inner, apiReq)
	relayJSON(w, inner)
}

// relayJSON copies the inner API response out to the browser verbatim.
//
// Status included: a 207 has to stay a 207, or the partial failure it encodes
// turns into an unqualified success on the way out.
func relayJSON(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for key, values := range rec.Header() {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(rec.Code)
	if _, err := w.Write(rec.Body.Bytes()); err != nil {
		logger.Error().Err(err).Msg("Failed to relay panel action response")
	}
}
