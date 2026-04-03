package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/pkg/logger"
)

// HandleList handles listing files in a bucket/directory
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	storageName := r.URL.Query().Get("storage")

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

	prefix = utils.NormalizeDirectoryPath(prefix)
	if err := utils.ValidateDirectoryPath(prefix); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_PREFIX", fmt.Sprintf("Invalid prefix path: %v", err))
		return
	}

	storageBackend, sbErr := h.getStorageBackendScoped(r, storageName, bucket)
	if sbErr != nil {
		h.sendError(w, http.StatusForbidden, "ACCESS_DENIED", sbErr.Error())
		return
	}

	results, err := storageBackend.List(ctx, prefix)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to list files")
		h.sendError(w, http.StatusInternalServerError, "LIST_ERROR", "Failed to list files")
		return
	}

	var files []types.ListItem
	directoryMap := make(map[string]*types.DirectoryInfo)

	prefixPath := prefix
	if prefixPath != "" && prefixPath[len(prefixPath)-1] != '/' {
		prefixPath = prefixPath + "/"
	}

	for _, result := range results {
		key := result.Key
		if prefixPath != "" && strings.HasPrefix(key, prefixPath) {
			key = strings.TrimPrefix(key, prefixPath)
		}

		if strings.Contains(key, "/") {
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
			files = append(files, types.ListItem{
				Key:      key,
				Size:     result.Size,
				Modified: result.Modified,
			})
		}
	}

	var directories []types.DirectoryInfo
	for _, dir := range directoryMap {
		directories = append(directories, *dir)
	}

	sort.Slice(directories, func(i, j int) bool {
		return directories[i].Name < directories[j].Name
	})

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
