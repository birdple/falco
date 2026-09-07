package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
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

	// Paginated mode is opt-in: a request with neither limit nor cursor keeps
	// the original behaviour of returning the whole prefix, so existing
	// consumers see no change.
	limit, limErr := parseListLimit(query.Get("limit"))
	if limErr != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_LIMIT", limErr.Error())
		return
	}
	cursor := query.Get("cursor")

	if limit > 0 || cursor != "" {
		h.listPage(w, r, storageBackend, prefix, cursor, limit)
		return
	}

	results, err := storageBackend.List(ctx, prefix)
	if err != nil {
		if storage.IsListingTooLarge(err) {
			// The prefix outgrew the whole-listing mode. Saying which knob
			// answers it beats a 500 that reads like a broken backend — and
			// beats the alternative this endpoint used to ship, a page of
			// results wearing `success: true`.
			logger.Warn().Err(err).Str("prefix", prefix).Msg("Prefix too large to list at once")
			h.sendError(w, http.StatusBadRequest, "LISTING_TOO_LARGE",
				fmt.Sprintf("More than %d objects under this prefix; page through it with ?limit= and ?cursor=", storage.MaxFullListingObjects))
			return
		}
		logger.Error().Err(err).Msg("Failed to list files")
		h.sendError(w, http.StatusInternalServerError, "LIST_ERROR", "Failed to list files")
		return
	}

	files, directories := groupListing(results, prefix)

	writeJSON(w, http.StatusOK, types.ListResponse{
		Success:     true,
		Prefix:      prefix,
		Count:       len(results),
		Files:       files,
		Directories: directories,
		// The full listing walked every page, so there is nothing left over.
		Truncated: false,
	})
}

// parseListLimit validates ?limit=. Zero means "not requested".
func parseListLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, errors.New("limit must be a positive integer")
	}
	if n > storage.MaxListPageSize {
		return 0, fmt.Errorf("limit must not exceed %d", storage.MaxListPageSize)
	}
	return n, nil
}

// listPage answers one page, using the backend's own pagination.
//
// Directories here come from the backend's delimiter roll-up rather than from
// walking every key, so they carry no file count: counting would mean listing
// the whole prefix, which is the exact thing pagination exists to avoid.
func (h *Handler) listPage(w http.ResponseWriter, r *http.Request, backend storage.StorageBackend, prefix, cursor string, limit int) {
	pager, ok := backend.(storage.PagedLister)
	if !ok {
		// Said out loud rather than quietly falling back to a full listing:
		// the caller asked for a page and would otherwise be handed the whole
		// bucket believing it was one.
		h.sendError(w, http.StatusNotImplemented, "PAGINATION_UNSUPPORTED",
			"This storage backend cannot list by page")
		return
	}

	// The prefix is matched verbatim by the backends, so directory semantics
	// are asked for explicitly here.
	listPrefix := prefix
	if listPrefix != "" && !strings.HasSuffix(listPrefix, "/") {
		listPrefix += "/"
	}

	page, err := pager.ListPage(r.Context(), storage.ListOptions{
		Prefix:    listPrefix,
		Delimiter: "/",
		Cursor:    cursor,
		MaxKeys:   limit,
	})
	if err != nil {
		logger.Error().Err(err).Msg("Failed to list files")
		h.sendError(w, http.StatusInternalServerError, "LIST_ERROR", "Failed to list files")
		return
	}

	files := make([]types.ListItem, 0, len(page.Objects))
	for _, obj := range page.Objects {
		files = append(files, types.ListItem{
			Key:         strings.TrimPrefix(obj.Key, listPrefix),
			Size:        obj.Size,
			Modified:    obj.Modified,
			ContentType: obj.ContentType,
			ETag:        obj.ETag,
		})
	}

	directories := make([]types.DirectoryInfo, 0, len(page.CommonPrefixes))
	for _, cp := range page.CommonPrefixes {
		name := strings.TrimSuffix(strings.TrimPrefix(cp, listPrefix), "/")
		if name == "" {
			continue
		}
		directories = append(directories, types.DirectoryInfo{
			Name: name,
			Path: strings.TrimSuffix(cp, "/"),
		})
	}

	writeJSON(w, http.StatusOK, types.ListResponse{
		Success:     true,
		Prefix:      prefix,
		Count:       len(files),
		Files:       files,
		Directories: directories,
		Truncated:   page.IsTruncated,
		NextCursor:  page.NextCursor,
	})
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
			count := 0
			directoryMap[dirName] = &types.DirectoryInfo{
				Name:      dirName,
				Path:      strings.TrimSuffix(prefixPath+dirName, "/"),
				FileCount: &count,
			}
		}
		*directoryMap[dirName].FileCount++
	}

	directories := make([]types.DirectoryInfo, 0, len(directoryMap))
	for _, dir := range directoryMap {
		directories = append(directories, *dir)
	}

	sort.Slice(directories, func(i, j int) bool { return directories[i].Name < directories[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Key < files[j].Key })

	return files, directories
}
