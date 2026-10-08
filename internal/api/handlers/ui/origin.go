package ui

import (
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// sameOrigin reports whether a state-changing request comes from the panel's
// own pages.
//
// Browsers send Origin on every cross-origin POST, and Sec-Fetch-Site on every
// request; either is enough to tell a cross-site form apart. A request that
// carries neither is not from a browser (curl, a test), and there is no
// ambient cookie to abuse in that case, so it is allowed.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		return origin == ""
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// isJSONRequest reports whether the body is declared as JSON.
func isJSONRequest(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}
