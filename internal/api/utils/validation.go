package utils

import (
	"net/url"
)

// IsValidImageID validates that an image ID contains only safe characters
func IsValidImageID(id string) bool {
	if id == "" || len(id) > 100 {
		return false
	}

	for _, c := range id {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}

	return true
}

// ExtractBucketAndDirFromSignPath inspects a caller-supplied delivery path
// (the target of a POST /sign call) and returns the bucket and directory it
// targets. The bucket may come from the "b" or "bucket" query parameter and
// the directory from "d"/"dir"/"directory" (matching delivery.go conventions).
//
// Returns an error if the path cannot be parsed at all. Both bucket and
// directory may be empty strings — the caller is responsible for interpreting
// empty-bucket semantics (usually "default bucket").
func ExtractBucketAndDirFromSignPath(rawPath string) (bucket, directory string, err error) {
	u, parseErr := url.Parse(rawPath)
	if parseErr != nil {
		return "", "", parseErr
	}
	q := u.Query()
	bucket = firstNonEmpty(q.Get("b"), q.Get("bucket"))
	directory = firstNonEmpty(q.Get("d"), q.Get("dir"), q.Get("directory"))
	return bucket, directory, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
