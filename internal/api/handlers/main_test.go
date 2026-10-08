package handlers

import (
	"os"
	"testing"

	"github.com/cshum/vipsgen/vips"
)

// TestMain starts libvips once for the tests that run the real processor.
func TestMain(m *testing.M) {
	vips.Startup(&vips.Config{ConcurrencyLevel: 1, VectorEnabled: true})
	code := m.Run()
	vips.Shutdown()
	os.Exit(code)
}
