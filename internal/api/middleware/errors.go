package middleware

import (
	jsonv2 "encoding/json/v2"
	"net/http"

	"github.com/birdple/falco/internal/api/types"
)

// writeError answers with the same JSON error envelope the handlers use, so a
// client parses a 401 from the auth middleware the same way it parses a 401
// from delivery. Errors are never cacheable.
func writeError(w http.ResponseWriter, status int, code, message string) {
	body, err := jsonv2.Marshal(types.ErrorResponse{
		Success: false,
		Error:   &types.APIError{Code: code, Message: message},
	})
	if err != nil {
		body = []byte(`{"success":false}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
