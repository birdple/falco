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

func TestIsUnavailable(t *testing.T) {
	assert.True(t, IsUnavailable(ErrStorageUnavailable))
	assert.True(t, IsUnavailable(fmt.Errorf("wrapped: %w", ErrStorageUnavailable)))
	assert.False(t, IsUnavailable(ErrImageNotFound))
}

func TestErrorSentinels(t *testing.T) {
	// Verify all sentinel errors are distinct
	sentinels := []error{
		ErrImageNotFound, ErrStorageUnavailable, ErrUnsupportedStorageType,
		ErrInvalidConfiguration, ErrBackendNotFound, ErrBucketNotHonoured,
		ErrUnsupportedOperation, ErrListingTooLarge,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j {
				assert.False(t, errors.Is(a, b), "%v should not match %v", a, b)
			}
		}
	}
}
