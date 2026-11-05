package handlers

import (
	"net/http"
)

// redocHTML is the API reference page.
//
// The ReDoc bundle is pinned to an exact version and carries a Subresource
// Integrity hash, taken from the npm tarball that unpkg serves byte for byte:
// this page is same-origin with the API, so a moved "latest" tag or a
// compromised CDN would otherwise run arbitrary script on falco's origin.
// To upgrade, change the version and recompute the hash from
// https://registry.npmjs.org/redoc/-/redoc-<version>.tgz
// (openssl dgst -sha384 -binary package/bundles/redoc.standalone.js | openssl base64 -A).
//
// No web fonts: ReDoc falls back to system fonts, and the page makes no
// third-party request besides the pinned bundle.
const redocHTML = `<!DOCTYPE html>
<html>
<head>
    <title>Falco API Docs</title>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <style>
        body {
            margin: 0;
            padding: 0;
        }
    </style>
</head>
<body>
    <redoc spec-url='/docs/openapi.yaml'></redoc>
    <script src="` + redocBundleURL + `" integrity="` + redocBundleSRI + `" crossorigin="anonymous"></script>
</body>
</html>
`

const (
	redocBundleURL = "https://unpkg.com/redoc@2.5.4/bundles/redoc.standalone.js"
	redocBundleSRI = "sha384-w447zOpYfw/1Tv/5AK9NfHTlQIqE3RVR6KY62jCyy9zNDgO64cMwGGP1Fj0zJVf5"
)

// HandleDocs serves the ReDoc API documentation.
func (h *Handler) HandleDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(redocHTML))
}
