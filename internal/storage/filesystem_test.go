package storage

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readAll(t *testing.T, fs *FilesystemStorage, key string) string {
	t.Helper()
	rc, _, err := fs.Retrieve(t.Context(), key)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(b)
}

func TestFilesystemStorage_Stat(t *testing.T) {
	fs := newFS(t)
	require.NoError(t, fs.Store(t.Context(), "k", strings.NewReader("12345"),
		&ImageMetadata{ID: "k", OwnerID: "owner", ContentType: "image/webp"}))

	meta, err := fs.Stat(t.Context(), "k")
	require.NoError(t, err)
	assert.Equal(t, "owner", meta.OwnerID)
	assert.Equal(t, "image/webp", meta.ContentType)
	assert.Equal(t, int64(5), meta.Size)
	assert.Equal(t, "k", meta.StorageKey)

	_, err = fs.Stat(t.Context(), "missing")
	assert.ErrorIs(t, err, ErrImageNotFound)

	require.NoError(t, fs.Delete(t.Context(), "k"))
	_, err = fs.Stat(t.Context(), "k")
	assert.ErrorIs(t, err, ErrImageNotFound)
}

// TestFilesystemStorage_FailedOverwriteKeepsOldVersion: Store used to rename
// the new data into place and, if the metadata write then failed, delete the
// data file — on an overwrite that destroyed the previous version too.
func TestFilesystemStorage_FailedOverwriteKeepsOldVersion(t *testing.T) {
	fs := newFS(t)
	require.NoError(t, fs.Store(t.Context(), "k", strings.NewReader("old"), &ImageMetadata{ID: "k", OwnerID: "v1"}))

	orig := createMetadataTemp
	t.Cleanup(func() { createMetadataTemp = orig })
	createMetadataTemp = func(string, string) (*os.File, error) { return nil, errors.New("no space left on device") }

	err := fs.Store(t.Context(), "k", strings.NewReader("new"), &ImageMetadata{ID: "k", OwnerID: "v2"})
	require.Error(t, err)
	createMetadataTemp = orig

	assert.Equal(t, "old", readAll(t, fs, "k"), "the old bytes survive")
	meta, err := fs.Stat(t.Context(), "k")
	require.NoError(t, err)
	assert.Equal(t, "v1", meta.OwnerID, "the old metadata survives")
	assertNoTempFiles(t, fs, "k")
}

// TestFilesystemStorage_MetadataIsWrittenAtomically: metadata goes through a
// temporary file and a rename instead of being truncated in place, and nothing
// is left behind either way.
func TestFilesystemStorage_MetadataIsWrittenAtomically(t *testing.T) {
	fs := newFS(t)
	require.NoError(t, fs.Store(t.Context(), "k", strings.NewReader("one"), &ImageMetadata{ID: "k", OwnerID: "a"}))
	before, err := os.Stat(fs.getMetadataPath("k"))
	require.NoError(t, err)

	require.NoError(t, fs.Store(t.Context(), "k", strings.NewReader("two"), &ImageMetadata{ID: "k", OwnerID: "b"}))
	after, err := os.Stat(fs.getMetadataPath("k"))
	require.NoError(t, err)

	assert.False(t, os.SameFile(before, after), "the metadata file was replaced, not rewritten in place")
	assert.Equal(t, os.FileMode(0644), after.Mode().Perm())
	assert.Equal(t, "two", readAll(t, fs, "k"))
	assertNoTempFiles(t, fs, "k")
}

// TestFilesystemStorage_ListTakesTheStripeLock: List read metadata with no lock
// at all, so it could pair one version's metadata with another's data file.
func TestFilesystemStorage_ListTakesTheStripeLock(t *testing.T) {
	fs := newFS(t)
	require.NoError(t, fs.Store(t.Context(), "k", strings.NewReader("data"), &ImageMetadata{ID: "k"}))

	mu := fs.stripe("k")
	mu.Lock()
	done := make(chan []ListResult)
	go func() {
		res, _ := fs.List(t.Context(), "")
		done <- res
	}()

	select {
	case <-done:
		mu.Unlock()
		t.Fatal("List read an object while a writer held its stripe lock")
	case <-time.After(50 * time.Millisecond):
	}
	mu.Unlock()

	res := <-done
	require.Len(t, res, 1)
	assert.Equal(t, "k", res[0].Key)
}

func assertNoTempFiles(t *testing.T, fs *FilesystemStorage, key string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(fs.getFilePath(key)))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp") || strings.HasPrefix(e.Name(), "temp_"),
			"leftover temporary file %s", e.Name())
	}
}
