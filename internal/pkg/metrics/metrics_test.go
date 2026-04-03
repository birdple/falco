package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefault(t *testing.T) {
	m := Default()
	require.NotNil(t, m)

	// Should be singleton
	m2 := Default()
	assert.Same(t, m, m2)
}

func TestNew(t *testing.T) {
	m := New()
	require.NotNil(t, m)

	// Should return the same singleton as Default
	assert.Same(t, Default(), m)
}

func TestMetrics_HTTPFields(t *testing.T) {
	m := Default()
	assert.NotNil(t, m.HTTPRequestsTotal)
	assert.NotNil(t, m.HTTPRequestDuration)
	assert.NotNil(t, m.HTTPResponseSize)
}

func TestMetrics_ProcessingFields(t *testing.T) {
	m := Default()
	assert.NotNil(t, m.ImageProcessingTotal)
	assert.NotNil(t, m.ImageProcessingDuration)
	assert.NotNil(t, m.ImageProcessingSize)
}

func TestMetrics_CacheFields(t *testing.T) {
	m := Default()
	assert.NotNil(t, m.CacheHits)
	assert.NotNil(t, m.CacheMisses)
	assert.NotNil(t, m.CacheSize)
	assert.NotNil(t, m.CacheItemCount)
	assert.NotNil(t, m.CacheEvictions)
}

func TestMetrics_StorageFields(t *testing.T) {
	m := Default()
	assert.NotNil(t, m.StorageOperationsTotal)
	assert.NotNil(t, m.StorageOperationDuration)
	assert.NotNil(t, m.StorageCircuitBreakerOpen)
}
