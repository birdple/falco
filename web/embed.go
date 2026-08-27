// Package web embeds the admin panel's static assets into the binary, so the UI
// works from a scratch container with no files alongside it.
package web

import "embed"

// StaticFS embeds all files under web/static/ into the binary.
// Access files via StaticFS at paths like "static/js/app.js".
//
//go:embed static
var StaticFS embed.FS
