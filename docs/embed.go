package docs

import "embed"

// FS embeds documentation files (openapi.yaml, etc.) into the binary.
//
//go:embed openapi.yaml
var FS embed.FS
