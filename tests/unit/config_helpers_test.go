package unit

import "github.com/birdple/falco/internal/config"

// testConfig arma el Config mínimo que necesitan los handlers en los tests:
// límites de dimensión y formato por default. Existe porque estas tres líneas
// estaban repetidas en seis sitios, cada una a un asignación por campo.
//
// Es un literal y no una serie de asignaciones porque MaxDimensions ya es un
// tipo con nombre (antes era un struct anónimo, y por eso era innombrable).
func testConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Processing.MaxDimensions = config.MaxDimensions{Width: 4000, Height: 4000}
	cfg.Processing.DefaultFormat = "webp"
	return cfg
}
