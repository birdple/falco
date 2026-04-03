package ui

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/birdple/falco/internal/api/views"
	"github.com/birdple/falco/internal/storage"
)

type Handler struct {
	renderer *views.Renderer
	storage  storage.StorageBackend
}

func NewHandler(renderer *views.Renderer, storage storage.StorageBackend) *Handler {
	return &Handler{
		renderer: renderer,
		storage:  storage,
	}
}

func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get bucket and prefix parameters
	bucket := r.URL.Query().Get("bucket")
	prefix := r.URL.Query().Get("prefix")
	if prefix == "" {
		prefix = r.URL.Query().Get("dir")
	}

	// Support bucket selection if storage is BucketAware
	storageBackend := h.storage
	if bucket != "" {
		if bucketAware, ok := h.storage.(storage.BucketAware); ok {
			storageBackend = bucketAware.WithBucket(bucket)
		}
	}

	// List ALL files recursively to build folder tree
	results, err := storageBackend.List(ctx, "")
	if err != nil {
		h.renderer.Render(w, "index.html", map[string]interface{}{"Error": err.Error()})
		return
	}

	type ImageInfo struct {
		ID          string
		Filename    string
		ContentType string
		Size        int64
		CreatedAt   time.Time
	}

	type DirectoryInfo struct {
		Name      string
		Path      string
		FileCount int
	}

	var images []ImageInfo
	directoryMap := make(map[string]*DirectoryInfo)

	// Ensure prefix has trailing slash for exact matching if not empty
	matchPrefix := prefix
	if matchPrefix != "" && matchPrefix[len(matchPrefix)-1] != '/' {
		matchPrefix += "/"
	}

	for _, res := range results {
		// Extract top-level directories
		if strings.Contains(res.Key, "/") {
			parts := strings.SplitN(res.Key, "/", 2)
			dirName := parts[0]
			if _, exists := directoryMap[dirName]; !exists {
				directoryMap[dirName] = &DirectoryInfo{
					Name:      dirName,
					Path:      dirName,
					FileCount: 0,
				}
			}
			directoryMap[dirName].FileCount++
		}

		// Filter images for the current prefix
		if matchPrefix == "" || strings.HasPrefix(res.Key, matchPrefix) {

			// For root view (matchPrefix == ""), only show images in the root directory
			// If matchPrefix is "", we want images without slashes
			if matchPrefix == "" && strings.Contains(res.Key, "/") {
				continue
			}

			// For folder view, we show ALL images under that folder recursively
			// Or we only show images in that exact folder?
			// Let's show images inside that folder, excluding subfolders for a cleaner view
			// (i.e. removing the prefix shouldn't contain another slash)
			relKey := res.Key
			if matchPrefix != "" {
				relKey = strings.TrimPrefix(res.Key, matchPrefix)
			}

			if strings.Contains(relKey, "/") {
				continue // Skip files in sub-directories of current view
			}

			images = append(images, ImageInfo{
				ID:          res.Key,
				Filename:    relKey,
				ContentType: "WEBP", // Default or inferred
				Size:        res.Size,
				CreatedAt:   res.Modified,
			})
		}
	}

	// Convert directory map to sorted slice
	var directories []DirectoryInfo
	for _, dir := range directoryMap {
		directories = append(directories, *dir)
	}

	// Sort directories alphabetically by Name
	sort.Slice(directories, func(i, j int) bool {
		return strings.ToLower(directories[i].Name) < strings.ToLower(directories[j].Name)
	})

	data := map[string]interface{}{
		"Bucket":        bucket,
		"CurrentPrefix": prefix,
		"Directories":   directories,
		"Images":        images,
	}

	err = h.renderer.Render(w, "index.html", data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
