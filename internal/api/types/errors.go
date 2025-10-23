package types

// APIError represents API error responses
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
