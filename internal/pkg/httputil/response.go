package httputil

import (
	"encoding/json"
	"net/http"
)

// JSONResponse represents a standard JSON API response
type JSONResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   *ErrorInfo  `json:"error,omitempty"`
}

// ErrorInfo represents error information in API responses
type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON writes a JSON response to the http.ResponseWriter
func WriteJSON(w http.ResponseWriter, statusCode int, data interface{}) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	return json.NewEncoder(w).Encode(data)
}

// WriteSuccess writes a successful JSON response
func WriteSuccess(w http.ResponseWriter, statusCode int, data interface{}) error {
	response := JSONResponse{
		Success: true,
		Data:    data,
	}
	return WriteJSON(w, statusCode, response)
}

// WriteError writes an error JSON response
func WriteError(w http.ResponseWriter, statusCode int, code, message string) error {
	response := JSONResponse{
		Success: false,
		Error: &ErrorInfo{
			Code:    code,
			Message: message,
		},
	}
	return WriteJSON(w, statusCode, response)
}
