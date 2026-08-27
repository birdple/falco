// Package types holds the request and response shapes of falco's HTTP API.
//
// They are the public contract: a field added here is visible to every client,
// and the strict decoders mean a field removed is a 400 for anyone still sending
// it.
package types

// APIError represents API error responses
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
