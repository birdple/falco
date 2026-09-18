package unit

import "github.com/birdple/falco/internal/config"

// testConfig builds the minimal Config the handlers need in tests: dimension
// limits and the default format.
func testConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Processing.MaxDimensions = config.MaxDimensions{Width: 4000, Height: 4000}
	cfg.Processing.DefaultFormat = "webp"
	return cfg
}
