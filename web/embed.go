package web

import "embed"

// StaticFS embeds all files under web/static/ into the binary.
// Access files via StaticFS at paths like "static/js/app.js".
//
//go:embed static
var StaticFS embed.FS
