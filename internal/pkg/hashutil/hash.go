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

// imageIDLength es cuántos caracteres del hash forman el ID de una imagen.
const imageIDLength = 16

// GenerateImageID generates a unique image ID from hash.
// Format: <first16chars_of_hash>
//
// Un hash más corto que imageIDLength se devuelve entero en lugar de paniquear.
// Hoy el único llamador es GenerateSHA256 (64 caracteres siempre), pero la
// función está exportada y `hash[:16]` a secas paniquea con cualquier cadena
// corta que le pase alguien de afuera.
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
