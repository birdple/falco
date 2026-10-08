package storage

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/birdple/falco/internal/jsonx"
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
	defer func() { _ = os.Remove(tempFile.Name()) }() // Clean up temp file on error
	defer func() { _ = tempFile.Close() }()

	// Copy data to temp file
	size, err := io.Copy(tempFile, data)
	if err != nil {
		return fmt.Errorf("failed to write data: %w", err)
	}

	// Update metadata with actual size and storage key
	metadata.Size = size
	metadata.StorageKey = key
	metadata.CreatedAt = time.Now()

	// Close temp file before moving
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("failed to write data: %w", err)
	}

	// Both files are written to temporaries before either live one is touched,
	// so the likely failures — disk full, permissions — leave a previous
	// version of the object exactly as it was. This used to rename the data
	// into place first and then, if the metadata write failed, delete the
	// data file: on an overwrite that destroyed the old version too.
	metaTemp, err := fs.stageMetadata(metaPath, metadata)
	if err != nil {
		return fmt.Errorf("failed to write metadata: %w", err)
	}
	defer func() { _ = os.Remove(metaTemp) }() // no-op once renamed

	_, statErr := os.Stat(metaPath)
	overwrite := statErr == nil

	// Atomic move to final location
	if err := os.Rename(tempFile.Name(), filePath); err != nil {
		return fmt.Errorf("failed to move file: %w", err)
	}

	// The metadata file is the commit marker: Retrieve, Stat and List all
	// start from it, so the object exists once it is in place.
	if err := os.Rename(metaTemp, metaPath); err != nil {
		// A new object has no marker, so its data file is an orphan. On an
		// overwrite the data file is left alone: removing it would leave the
		// old metadata pointing at nothing, which is worse than at new bytes.
		if !overwrite {
			_ = os.Remove(filePath)
		}
		return fmt.Errorf("failed to commit metadata: %w", err)
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

// Stat returns an object's metadata by reading its .meta.json only; the data
// file is never opened.
func (fs *FilesystemStorage) Stat(ctx context.Context, key string) (*ImageMetadata, error) {
	mu := fs.stripe(key)
	mu.RLock()
	defer mu.RUnlock()

	metadata, err := fs.readMetadata(fs.getMetadataPath(key))
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}
	return metadata, nil
}

// Delete deletes an image by key.
//
// The metadata goes first because it is the commit marker every read starts
// from: if the second removal fails, what is left is an orphaned data file
// nothing can see, not metadata announcing an object whose bytes are gone.
func (fs *FilesystemStorage) Delete(ctx context.Context, key string) error {
	mu := fs.stripe(key)
	mu.Lock()
	defer mu.Unlock()

	filePath := fs.getFilePath(key)
	metaPath := fs.getMetadataPath(key)

	if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete metadata: %w", err)
	}

	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete file: %w", err)
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
	defer func() { _ = os.Remove(testFile) }()

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
		stats.FreeSpace = int64(stat.Bavail) * statBlockSize(&stat)
	}

	return stats, nil
}

// List lists every object with the given prefix.
// It reads each .meta.json to recover the original storage key,
// since file paths on disk are MD5-hashed and not human-readable.
func (fs *FilesystemStorage) List(ctx context.Context, prefix string) ([]ListResult, error) {
	var results []ListResult

	err := filepath.Walk(fs.basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// We only care about metadata files
		if info.IsDir() || !strings.HasSuffix(path, ".meta.json") {
			return nil
		}

		meta, imgInfo, readErr := fs.readListingEntry(path)
		if readErr != nil {
			return nil // skip unreadable metadata
		}

		// Determine the original storage key
		key := meta.StorageKey
		if key == "" {
			// Fallback for old files without StorageKey: use the ID directly
			key = meta.ID
		}

		if prefix == "" || strings.HasPrefix(key, prefix) {
			// Prefer the actual image file (not the metadata file) for size
			// and modification time.
			size := meta.Size
			modified := meta.CreatedAt
			if imgInfo != nil {
				size = imgInfo.Size()
				modified = imgInfo.ModTime()
			}

			results = append(results, ListResult{
				Key:      key,
				Size:     size,
				Modified: modified,
				// The metadata file was read anyway to recover the key, so the
				// content type comes for free. Without it the UI can only show
				// "unknown" for every object on this backend.
				ContentType: meta.ContentType,
				ETag:        meta.ETag,
			})
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}

	return results, nil
}

// readListingEntry reads one object's metadata and stats its data file under
// that object's stripe lock, so a listing never pairs a half-written Store's
// metadata with the other version's data file.
//
// The walk only has the path, not the key, but it does not need the key:
// getFilePath names the shard directory after the hex of the first byte of the
// key's MD5, which is the very byte stripe() indexes by. A path outside that
// layout (getFilePath's traversal fallback) has no stripe and is read unlocked.
func (fs *FilesystemStorage) readListingEntry(metaPath string) (*ImageMetadata, os.FileInfo, error) {
	if b, err := hex.DecodeString(filepath.Base(filepath.Dir(metaPath))); err == nil && len(b) == 1 {
		mu := &fs.stripes[b[0]]
		mu.RLock()
		defer mu.RUnlock()
	}

	meta, err := fs.readMetadata(metaPath)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(strings.TrimSuffix(metaPath, ".meta.json"))
	if err != nil {
		info = nil
	}
	return meta, info, nil
}

// ListPage returns one page of objects, honouring prefix, delimiter and cursor.
//
// Unlike S3 and jay, there is no server to page against: keys live in a
// hash-sharded tree, so key order cannot be walked directly and the whole
// listing is built and then sliced. The cost is the same walk List already
// does; what this buys is a bounded response and an honest IsTruncated.
func (fs *FilesystemStorage) ListPage(ctx context.Context, opts ListOptions) (*ListPage, error) {
	all, err := fs.List(ctx, opts.Prefix)
	if err != nil {
		return nil, err
	}
	return pageFromListing(all, opts), nil
}

// pageFromListing slices a complete listing into one page, applying the
// delimiter roll-up. Shared by every backend that can only list in full.
func pageFromListing(all []ListResult, opts ListOptions) *ListPage {
	sort.Slice(all, func(i, j int) bool { return all[i].Key < all[j].Key })

	limit := NormalizeMaxKeys(opts.MaxKeys)
	out := &ListPage{}
	seenPrefix := make(map[string]bool)

	for _, item := range all {
		// Filter by prefix here too. The caller usually did it already, but a
		// helper that silently ignores a field of its own options is a trap:
		// pass a full listing and it would answer with keys outside the prefix.
		if opts.Prefix != "" && !strings.HasPrefix(item.Key, opts.Prefix) {
			continue
		}

		// Resume: the cursor is the last key of the previous page.
		if opts.Cursor != "" && item.Key <= opts.Cursor {
			continue
		}

		if opts.Delimiter != "" {
			rest := strings.TrimPrefix(item.Key, opts.Prefix)
			if idx := strings.Index(rest, opts.Delimiter); idx >= 0 {
				group := opts.Prefix + rest[:idx+len(opts.Delimiter)]
				if seenPrefix[group] {
					// Covered by the prefix already on this page, so the
					// cursor moves past it. Pointing at the group's FIRST key
					// instead made the next page start inside the same folder
					// and roll it up again: a/1 a/2 a/3 b at one per page
					// listed a/ three times before reaching b. Keys sharing a
					// prefix are contiguous once sorted, so by the time a key
					// outside the group shows up the cursor is on its last one.
					out.NextCursor = item.Key
					continue
				}
				if len(out.Objects)+len(out.CommonPrefixes) >= limit {
					out.IsTruncated = true
					return out
				}
				seenPrefix[group] = true
				out.CommonPrefixes = append(out.CommonPrefixes, group)
				out.NextCursor = item.Key
				continue
			}
		}

		if len(out.Objects)+len(out.CommonPrefixes) >= limit {
			out.IsTruncated = true
			return out
		}
		out.Objects = append(out.Objects, item)
		out.NextCursor = item.Key
	}

	// Everything fit: there is no next page to point at.
	out.NextCursor = ""
	return out
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

// createMetadataTemp is os.CreateTemp, held in a variable so a test can make
// staging the metadata fail the way a full disk would.
var createMetadataTemp = os.CreateTemp

// stageMetadata writes metadata to a temporary file next to path and returns
// its name; the caller renames it into place.
//
// It replaced an os.WriteFile straight onto the live file, which truncates
// first and writes second: a failure or crash in between left an empty or
// half-written .meta.json, and with it an object that no longer decodes.
//
// The temporary name ends in ".tmp", never ".meta.json", so a listing walking
// the tree concurrently cannot mistake it for an object.
func (fs *FilesystemStorage) stageMetadata(path string, metadata *ImageMetadata) (string, error) {
	// Create directory if it doesn't exist
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create metadata directory: %w", err)
	}

	// Wire: the on-disk .meta.json is a persisted data format and has to keep
	// coming out byte-for-byte as encoding/json v1 wrote it.
	data, err := jsonv2.Marshal(metadata, jsonx.Wire, jsontext.WithIndent("  "))
	if err != nil {
		return "", fmt.Errorf("failed to marshal metadata: %w", err)
	}

	f, err := createMetadataTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create metadata temp file: %w", err)
	}
	// CreateTemp opens 0600; the metadata file has always been 0644.
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0644)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("failed to write metadata file: %w", err)
	}

	return f.Name(), nil
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

	// Lenient on read: a .meta.json written by an older version can carry broken
	// UTF-8 in the original filename, and it must not become unreadable.
	var metadata ImageMetadata
	if err := jsonv2.Unmarshal(data, &metadata, jsonx.Lenient); err != nil {
		return nil, fmt.Errorf("failed to unmarshal metadata: %w", err)
	}

	return &metadata, nil
}
