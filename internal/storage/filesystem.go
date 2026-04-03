package storage

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const numStripes = 256

// FilesystemStorage implements StorageBackend for local filesystem storage
type FilesystemStorage struct {
	basePath string
	stripes  [numStripes]sync.RWMutex // striped locks keyed by hash prefix
}

// NewFilesystemStorage creates a new filesystem storage backend
func NewFilesystemStorage(basePath string) (*FilesystemStorage, error) {
	// Ensure base path is absolute
	absPath, err := filepath.Abs(basePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	// Create base directory if it doesn't exist
	if err := os.MkdirAll(absPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}

	return &FilesystemStorage{
		basePath: absPath,
	}, nil
}

// stripe returns the lock index for a given key
func (fs *FilesystemStorage) stripe(key string) *sync.RWMutex {
	hash := md5.Sum([]byte(key))
	return &fs.stripes[hash[0]]
}

// Store stores an image with the given key and metadata
func (fs *FilesystemStorage) Store(ctx context.Context, key string, data io.Reader, metadata *ImageMetadata) error {
	mu := fs.stripe(key)
	mu.Lock()
	defer mu.Unlock()

	// Generate file path
	filePath := fs.getFilePath(key)
	metaPath := fs.getMetadataPath(key)

	// Create directory structure
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Create temporary file for atomic write
	tempFile, err := os.CreateTemp(dir, "temp_*_"+filepath.Base(filePath))
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tempFile.Name()) // Clean up temp file on error
	defer tempFile.Close()

	// Copy data to temp file
	size, err := io.Copy(tempFile, data)
	if err != nil {
		return fmt.Errorf("failed to write data: %w", err)
	}

	// Update metadata with actual size
	metadata.Size = size
	metadata.CreatedAt = time.Now()

	// Close temp file before moving
	tempFile.Close()

	// Atomic move to final location
	if err := os.Rename(tempFile.Name(), filePath); err != nil {
		return fmt.Errorf("failed to move file: %w", err)
	}

	// Write metadata
	if err := fs.writeMetadata(metaPath, metadata); err != nil {
		// Try to clean up the file if metadata write fails
		os.Remove(filePath)
		return fmt.Errorf("failed to write metadata: %w", err)
	}

	return nil
}

// Retrieve retrieves an image by key
func (fs *FilesystemStorage) Retrieve(ctx context.Context, key string) (io.ReadCloser, *ImageMetadata, error) {
	mu := fs.stripe(key)
	mu.RLock()
	defer mu.RUnlock()

	filePath := fs.getFilePath(key)
	metaPath := fs.getMetadataPath(key)

	// Read metadata first
	metadata, err := fs.readMetadata(metaPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	// Open file
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrImageNotFound
		}
		return nil, nil, fmt.Errorf("failed to open file: %w", err)
	}

	return file, metadata, nil
}

// Delete deletes an image by key
func (fs *FilesystemStorage) Delete(ctx context.Context, key string) error {
	mu := fs.stripe(key)
	mu.Lock()
	defer mu.Unlock()

	filePath := fs.getFilePath(key)
	metaPath := fs.getMetadataPath(key)

	// Remove files
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete metadata: %w", err)
	}

	return nil
}

// Exists checks if an image exists by key
func (fs *FilesystemStorage) Exists(ctx context.Context, key string) (bool, error) {
	mu := fs.stripe(key)
	mu.RLock()
	defer mu.RUnlock()

	filePath := fs.getFilePath(key)
	_, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check file existence: %w", err)
	}
	return true, nil
}

// Health checks the health of the filesystem storage
func (fs *FilesystemStorage) Health(ctx context.Context) error {
	// Check if base directory exists and is writable
	info, err := os.Stat(fs.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrStorageUnavailable
		}
		return fmt.Errorf("failed to stat base directory: %w", err)
	}

	if !info.IsDir() {
		return ErrStorageUnavailable
	}

	// Try to create a test file
	testFile := filepath.Join(fs.basePath, ".health_check")
	if err := os.WriteFile(testFile, []byte("health check"), 0644); err != nil {
		return fmt.Errorf("failed to write test file: %w", err)
	}
	defer os.Remove(testFile)

	return nil
}

// GetStats returns storage statistics
func (fs *FilesystemStorage) GetStats(ctx context.Context) (*StorageStats, error) {
	stats := &StorageStats{}

	// Count files and calculate total size
	err := filepath.Walk(fs.basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and metadata files
		if info.IsDir() || strings.HasSuffix(path, ".meta.json") {
			return nil
		}

		stats.TotalImages++
		stats.TotalSize += info.Size()
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}

	// Get disk usage information
	var stat syscall.Statfs_t
	if err := syscall.Statfs(fs.basePath, &stat); err == nil {
		stats.FreeSpace = int64(stat.Bavail) * int64(stat.Bsize)
	}

	return stats, nil
}

// List lists objects with the given prefix
func (fs *FilesystemStorage) List(ctx context.Context, prefix string) ([]ListResult, error) {
	var results []ListResult

	// Walk the directory to find files matching the prefix
	err := filepath.Walk(fs.basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and metadata files
		if info.IsDir() || strings.HasSuffix(path, ".meta.json") {
			return nil
		}

		// Get relative path from base path
		relPath, err := filepath.Rel(fs.basePath, path)
		if err != nil {
			return err
		}

		// Check if the relative path starts with the prefix
		if prefix == "" || strings.HasPrefix(relPath, prefix) {
			results = append(results, ListResult{
				Key:      relPath,
				Size:     info.Size(),
				Modified: info.ModTime(),
			})
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}

	return results, nil
}

// getFilePath returns the file path for a given key.
// The result is guaranteed to be contained within basePath (path traversal safe).
func (fs *FilesystemStorage) getFilePath(key string) string {
	// Create hash-based directory structure for better performance
	hash := md5.Sum([]byte(key))
	hashStr := hex.EncodeToString(hash[:])

	// Use first 2 characters as directory, rest as filename
	dir := hashStr[:2]
	filename := hashStr[2:]

	result := filepath.Join(fs.basePath, dir, filename)

	// SECURITY: Ensure path is always within basePath to prevent path traversal
	result = filepath.Clean(result)
	if !strings.HasPrefix(result, fs.basePath) {
		// Fallback to a safe path derived from the hash only
		return filepath.Join(fs.basePath, hashStr)
	}

	return result
}

// getMetadataPath returns the metadata path for a given key
func (fs *FilesystemStorage) getMetadataPath(key string) string {
	filePath := fs.getFilePath(key)
	return filePath + ".meta.json"
}

// writeMetadata writes metadata to a file
func (fs *FilesystemStorage) writeMetadata(path string, metadata *ImageMetadata) error {
	// Create directory if it doesn't exist
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create metadata directory: %w", err)
	}

	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write metadata file: %w", err)
	}

	return nil
}

// readMetadata reads metadata from a file
func (fs *FilesystemStorage) readMetadata(path string) (*ImageMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrImageNotFound
		}
		return nil, fmt.Errorf("failed to read metadata file: %w", err)
	}

	var metadata ImageMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	return &metadata, nil
}
