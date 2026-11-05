package unit

import (
	"github.com/stretchr/testify/mock"

	"github.com/birdple/falco/tests/mocks"
)

// newProcessorMock returns a processor mock that tolerates cache invalidation.
// Upload, update and delete all invalidate the transform cache as a side
// effect; tests that care about it assert the call explicitly.
func newProcessorMock() *mocks.MockImageProcessor {
	p := new(mocks.MockImageProcessor)
	p.On("InvalidateCache", mock.Anything).Return(0).Maybe()
	return p
}
