package unit

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/birdple/falco/internal/pkg/httputil"
)

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	data := map[string]string{"message": "test"}

	err := httputil.WriteJSON(w, 200, data)
	assert.NoError(t, err)
	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var result map[string]string
	json.Unmarshal(w.Body.Bytes(), &result)
	assert.Equal(t, "test", result["message"])
}

func TestWriteSuccess(t *testing.T) {
	w := httptest.NewRecorder()
	data := map[string]any{"count": 10}

	err := httputil.WriteSuccess(w, 200, data)
	assert.NoError(t, err)
	assert.Equal(t, 200, w.Code)

	var response httputil.JSONResponse
	json.Unmarshal(w.Body.Bytes(), &response)
	assert.True(t, response.Success)
	assert.NotNil(t, response.Data)
	assert.Nil(t, response.Error)
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()

	err := httputil.WriteError(w, 400, "TEST_ERROR", "This is a test error")
	assert.NoError(t, err)
	assert.Equal(t, 400, w.Code)

	var response httputil.JSONResponse
	json.Unmarshal(w.Body.Bytes(), &response)
	assert.False(t, response.Success)
	assert.Nil(t, response.Data)
	assert.NotNil(t, response.Error)
	assert.Equal(t, "TEST_ERROR", response.Error.Code)
	assert.Equal(t, "This is a test error", response.Error.Message)
}

func TestJSONResponse_Structure(t *testing.T) {
	resp := httputil.JSONResponse{
		Success: true,
		Data:    "test data",
		Error:   nil,
	}

	assert.True(t, resp.Success)
	assert.Equal(t, "test data", resp.Data)
	assert.Nil(t, resp.Error)
}

func TestErrorInfo_Structure(t *testing.T) {
	errInfo := &httputil.ErrorInfo{
		Code:    "NOT_FOUND",
		Message: "Resource not found",
	}

	assert.Equal(t, "NOT_FOUND", errInfo.Code)
	assert.Equal(t, "Resource not found", errInfo.Message)
}
