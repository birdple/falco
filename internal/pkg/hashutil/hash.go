// Package hashutil derives image IDs from content.
//
// Content-addressing is what makes uploads idempotent: the same bytes always
// land on the same key, so re-uploading is a no-op at the storage layer instead
// of a duplicate.
package hashutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
)

// GenerateSHA256 generates a SHA256 hash from the given data
func GenerateSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// GenerateSHA256FromReader generates a SHA256 hash from an io.Reader
func GenerateSHA256FromReader(reader io.Reader) (string, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, reader); err != nil {
		return "", fmt.Errorf("failed to hash data: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// imageIDLength is how many characters of the hash make up an image ID.
const imageIDLength = 16

// GenerateImageID generates a unique image ID from hash.
// Format: <first16chars_of_hash>
//
// A hash shorter than imageIDLength is returned whole rather than panicking.
// Today the only caller is GenerateSHA256, which is always 64 characters, but
// this function is exported and a bare `hash[:16]` panics on any short string an
// outside caller hands it.
func GenerateImageID(hash string) string {
	if len(hash) < imageIDLength {
		return hash
	}
	return hash[:imageIDLength]
}

// GenerateImageIDFromData generates an image ID from data
func GenerateImageIDFromData(data []byte) string {
	hash := GenerateSHA256(data)
	return GenerateImageID(hash)
}
