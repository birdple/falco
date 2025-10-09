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

// GenerateImageID generates a unique image ID from hash
// Format: <first16chars_of_hash>
func GenerateImageID(hash string) string {
	return hash[:16]
}

// GenerateImageIDFromData generates an image ID from data
func GenerateImageIDFromData(data []byte) string {
	hash := GenerateSHA256(data)
	return GenerateImageID(hash)
}
