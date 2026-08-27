// Package docs embeds falco's OpenAPI specification into the binary, so /docs
// works from a scratch container with no files alongside it.
package docs

import "embed"

// FS embeds documentation files (openapi.yaml, etc.) into the binary.
//
//go:embed openapi.yaml
var FS embed.FS
