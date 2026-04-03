package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/api/utils"
)

// HandleList handles listing files in a bucket/directory
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get bucket and prefix from query parameters
	bucket := r.URL.Query().Get("b")
	if bucket == "" {
		bucket = r.URL.Query().Get("bucket")
	}

	prefix := r.URL.Query().Get("p")
	if prefix == "" {
		prefix = r.URL.Query().Get("prefix")
	}
	if prefix == "" {
		prefix = r.URL.Query().Get("d")
	}
	if prefix == "" {
		prefix = r.URL.Query().Get("dir")
	}
	if prefix == "" {
		prefix = r.URL.Query().Get("directory")
	}

	// Normalize and validate prefix path
	prefix = utils.NormalizeDirectoryPath(prefix)
	if err := utils.ValidateDirectoryPath(prefix); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_PREFIX", fmt.Sprintf("Invalid prefix path: %v", err))
		return
	}

	// Get bucket-aware storage instance
	storageBackend := h.getStorageForBucket(bucket)

	// List files
	results, err := storageBackend.List(ctx, prefix)
	if err != nil {
		h.logger.WithError(err).Error("Failed to list files")
		h.sendError(w, http.StatusInternalServerError, "LIST_ERROR", "Failed to list files")
		return
	}

	// Separate files and directories
	var files []types.ListItem
	directoryMap := make(map[string]*types.DirectoryInfo)

	// Build the prefix path for comparison
	prefixPath := prefix
	if prefixPath != "" && prefixPath[len(prefixPath)-1] != '/' {
		prefixPath = prefixPath + "/"
	}

	for _, result := range results {
		// Remove prefix from key
		key := result.Key
		if prefixPath != "" && strings.HasPrefix(key, prefixPath) {
			key = strings.TrimPrefix(key, prefixPath)
		}

		// Check if this is a direct file or in a subdirectory
		if strings.Contains(key, "/") {
			// File is in a subdirectory
			parts := strings.SplitN(key, "/", 2)
			dirName := parts[0]

			if _, exists := directoryMap[dirName]; !exists {
				fullPath := prefixPath + dirName
				if prefixPath == "" {
					fullPath = dirName
				}
				directoryMap[dirName] = &types.DirectoryInfo{
					Name:      dirName,
					Path:      strings.TrimSuffix(fullPath, "/"),
					FileCount: 0,
				}
			}
			directoryMap[dirName].FileCount++
		} else {
			// Direct file in this directory
			files = append(files, types.ListItem{
				Key:      key,
				Size:     result.Size,
				Modified: result.Modified,
			})
		}
	}

	// Convert directory map to slice
	var directories []types.DirectoryInfo
	for _, dir := range directoryMap {
		directories = append(directories, *dir)
	}

	// Sort directories by name for consistent output
	sort.Slice(directories, func(i, j int) bool {
		return directories[i].Name < directories[j].Name
	})

	// Sort files by key for consistent output
	sort.Slice(files, func(i, j int) bool {
		return files[i].Key < files[j].Key
	})

	response := types.ListResponse{
		Success:     true,
		Prefix:      prefix,
		Count:       len(results),
		Files:       files,
		Directories: directories,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}
