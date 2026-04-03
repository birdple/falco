package hashutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSHA256(t *testing.T) {
	hash := GenerateSHA256([]byte("hello world"))
	assert.Len(t, hash, 64) // SHA256 hex is 64 chars
	assert.Equal(t, "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9", hash)
}

func TestGenerateSHA256_Deterministic(t *testing.T) {
	data := []byte("test data")
	hash1 := GenerateSHA256(data)
	hash2 := GenerateSHA256(data)
	assert.Equal(t, hash1, hash2)
}

func TestGenerateSHA256_DifferentInputs(t *testing.T) {
	hash1 := GenerateSHA256([]byte("hello"))
	hash2 := GenerateSHA256([]byte("world"))
	assert.NotEqual(t, hash1, hash2)
}

func TestGenerateSHA256_EmptyInput(t *testing.T) {
	hash := GenerateSHA256([]byte{})
	assert.Len(t, hash, 64)
	// Known SHA256 of empty string
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", hash)
}

func TestGenerateSHA256FromReader(t *testing.T) {
	reader := strings.NewReader("hello world")
	hash, err := GenerateSHA256FromReader(reader)
	require.NoError(t, err)
	assert.Len(t, hash, 64)
	assert.Equal(t, "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9", hash)
}

func TestGenerateSHA256FromReader_EmptyReader(t *testing.T) {
	reader := strings.NewReader("")
	hash, err := GenerateSHA256FromReader(reader)
	require.NoError(t, err)
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", hash)
}

func TestGenerateImageID(t *testing.T) {
	hash := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	id := GenerateImageID(hash)
	assert.Len(t, id, 16)
	assert.Equal(t, "b94d27b9934d3e08", id)
}

func TestGenerateImageIDFromData(t *testing.T) {
	id := GenerateImageIDFromData([]byte("hello world"))
	assert.Len(t, id, 16)
	assert.Equal(t, "b94d27b9934d3e08", id)
}

func TestGenerateImageIDFromData_Deterministic(t *testing.T) {
	data := []byte("test image data")
	id1 := GenerateImageIDFromData(data)
	id2 := GenerateImageIDFromData(data)
	assert.Equal(t, id1, id2)
}

var smallData = []byte("hello world")
var mediumData = make([]byte, 64*1024)   // 64 KB
var largeData = make([]byte, 4*1024*1024) // 4 MB

func BenchmarkGenerateSHA256_Small(b *testing.B) {
	b.SetBytes(int64(len(smallData)))
	b.ResetTimer()
	for b.Loop() {
		GenerateSHA256(smallData)
	}
}

func BenchmarkGenerateSHA256_Medium(b *testing.B) {
	b.SetBytes(int64(len(mediumData)))
	b.ResetTimer()
	for b.Loop() {
		GenerateSHA256(mediumData)
	}
}

func BenchmarkGenerateSHA256_Large(b *testing.B) {
	b.SetBytes(int64(len(largeData)))
	b.ResetTimer()
	for b.Loop() {
		GenerateSHA256(largeData)
	}
}

func BenchmarkGenerateSHA256FromReader(b *testing.B) {
	b.SetBytes(int64(len(mediumData)))
	b.ResetTimer()
	for b.Loop() {
		r := strings.NewReader(string(mediumData))
		GenerateSHA256FromReader(r)
	}
}

func BenchmarkGenerateImageIDFromData(b *testing.B) {
	b.SetBytes(int64(len(smallData)))
	b.ResetTimer()
	for b.Loop() {
		GenerateImageIDFromData(smallData)
	}
}
