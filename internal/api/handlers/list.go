package handlers

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/birdple/falco/internal/api/types"
	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/storage"
)

// HandleList handles listing files in a bucket/directory
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parsed once for the whole handler (see HandleDelivery).
	query := r.URL.Query()

	storageName := query.Get("storage")
	bucket := utils.QueryParam(query, "b", "bucket")
	prefix := utils.QueryParam(query, "p", "prefix", "d", "dir", "directory")

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

	files, directories := groupListing(results, prefix)

	response := types.ListResponse{
		Success:     true,
		Prefix:      prefix,
		Count:       len(results),
		Files:       files,
		Directories: directories,
	}

	writeJSON(w, http.StatusOK, response)
}

// groupListing turns a flat key listing into the one level of files and
// directories below prefix.
//
// Object stores have no directories: "a/b/c.jpg" is one key, not a tree. What
// this does is synthesise that one level — every key with a slash left in it
// after the prefix is stripped contributes to a directory entry instead of
// appearing as a file, and its FileCount counts everything beneath it, however
// deep.
//
// Both slices come back sorted by name so the listing is stable between calls;
// the backend makes no ordering promise.
func groupListing(results []storage.ListResult, prefix string) ([]types.ListItem, []types.DirectoryInfo) {
	prefixPath := prefix
	if prefixPath != "" && !strings.HasSuffix(prefixPath, "/") {
		prefixPath += "/"
	}

	var files []types.ListItem
	directoryMap := make(map[string]*types.DirectoryInfo)

	for _, result := range results {
		key := strings.TrimPrefix(result.Key, prefixPath)

		dirName, _, isNested := strings.Cut(key, "/")
		if !isNested {
			files = append(files, types.ListItem{
				Key:      key,
				Size:     result.Size,
				Modified: result.Modified,
			})
			continue
		}

		if _, exists := directoryMap[dirName]; !exists {
			directoryMap[dirName] = &types.DirectoryInfo{
				Name: dirName,
				Path: strings.TrimSuffix(prefixPath+dirName, "/"),
			}
		}
		directoryMap[dirName].FileCount++
	}

	directories := make([]types.DirectoryInfo, 0, len(directoryMap))
	for _, dir := range directoryMap {
		directories = append(directories, *dir)
	}

	sort.Slice(directories, func(i, j int) bool { return directories[i].Name < directories[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Key < files[j].Key })

	return files, directories
}
