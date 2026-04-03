package storage

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsNotFound(t *testing.T) {
	assert.True(t, IsNotFound(ErrImageNotFound))
	assert.True(t, IsNotFound(fmt.Errorf("wrapped: %w", ErrImageNotFound)))
	assert.False(t, IsNotFound(ErrStorageUnavailable))
	assert.False(t, IsNotFound(errors.New("some other error")))
}

func TestIsAlreadyExists(t *testing.T) {
	assert.True(t, IsAlreadyExists(ErrImageAlreadyExists))
	assert.True(t, IsAlreadyExists(fmt.Errorf("wrapped: %w", ErrImageAlreadyExists)))
	assert.False(t, IsAlreadyExists(ErrImageNotFound))
}

func TestIsUnavailable(t *testing.T) {
	assert.True(t, IsUnavailable(ErrStorageUnavailable))
	assert.True(t, IsUnavailable(fmt.Errorf("wrapped: %w", ErrStorageUnavailable)))
	assert.False(t, IsUnavailable(ErrImageNotFound))
}

func TestIsNetworkError(t *testing.T) {
	assert.True(t, IsNetworkError(ErrNetworkError))
	assert.True(t, IsNetworkError(fmt.Errorf("wrapped: %w", ErrNetworkError)))
	assert.False(t, IsNetworkError(ErrTimeout))
}

func TestIsTimeout(t *testing.T) {
	assert.True(t, IsTimeout(ErrTimeout))
	assert.True(t, IsTimeout(fmt.Errorf("wrapped: %w", ErrTimeout)))
	assert.False(t, IsTimeout(ErrNetworkError))
}

func TestErrorSentinels(t *testing.T) {
	// Verify all sentinel errors are distinct
	sentinels := []error{
		ErrImageNotFound, ErrImageAlreadyExists, ErrInvalidImageFormat,
		ErrStorageUnavailable, ErrUnsupportedStorageType, ErrInvalidConfiguration,
		ErrPermissionDenied, ErrDiskFull, ErrNetworkError, ErrTimeout,
		ErrInvalidKey, ErrCorruptedData,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j {
				assert.False(t, errors.Is(a, b), "%v should not match %v", a, b)
			}
		}
	}
}
