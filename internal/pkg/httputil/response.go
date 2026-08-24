package httputil

import (
	jsonv2 "encoding/json/v2"
	"net/http"
)

// JSONResponse represents a standard JSON API response
type JSONResponse struct {
	Success bool       `json:"success"`
	Data    any        `json:"data,omitzero"`
	Error   *ErrorInfo `json:"error,omitempty"`
}

// ErrorInfo represents error information in API responses
type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON writes a JSON response to the http.ResponseWriter.
//
// Serializa antes de escribir la cabecera: así un error de marshal se devuelve
// al llamador con el ResponseWriter todavía intacto, en lugar de dejar un 200
// con el cuerpo truncado.
func WriteJSON(w http.ResponseWriter, statusCode int, data any) error {
	body, err := jsonv2.Marshal(data)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, err = w.Write(body)
	return err
}

// WriteSuccess writes a successful JSON response
func WriteSuccess(w http.ResponseWriter, statusCode int, data any) error {
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
