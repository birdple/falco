package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/birdple/falco/internal/api/utils"
	"github.com/birdple/falco/internal/storage"
)

// ObjectMeta is the body of GET /api/v1/meta/*.
type ObjectMeta struct {
	Success bool `json:"success"`

	ID         string `json:"id"`
	StorageKey string `json:"storage_key"`
	Bucket     string `json:"bucket,omitempty"`

	// Format and ContentType are what the object actually IS, read from stored
	// metadata. They are not guessed from the key: falco stores content-hashed
	// keys with no extension, so guessing from the name can only ever answer
	// "unknown".
	Format      string `json:"format,omitempty"`
	ContentType string `json:"content_type,omitempty"`

	Size   int64 `json:"size_bytes"`
	Width  int   `json:"width,omitempty"`
	Height int   `json:"height,omitempty"`

	OriginalName string    `json:"original_name,omitempty"`
	OwnerID      string    `json:"owner_id,omitempty"`
	ETag         string    `json:"etag,omitempty"`
	CreatedAt    time.Time `json:"created_at,omitzero"`
	MaxAge       int       `json:"maxage,omitzero"`
	SMaxAge      int       `json:"smaxage,omitzero"`
}

// HandleObjectMeta returns an object's stored metadata without transferring it.
//
// It lives at /api/v1/meta/* rather than under /images/ because delivery owns
// that whole subtree with a wildcard, and it sits inside the authenticated
// group: dimensions and owner are not part of what a signed delivery URL
// grants.
//
// The path after the prefix is read exactly like a delivery path, so the same
// URL that serves an image describes it by swapping one segment.
func (h *Handler) HandleObjectMeta(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	imageID, _, idErr := resolveImageID(r)
	if idErr != nil {
		h.sendError(w, http.StatusBadRequest, idErr.code, idErr.message)
		return
	}

	storageName := query.Get("storage")
	bucket := utils.QueryParam(query, "b", "bucket")
	directory := utils.NormalizeDirectoryPath(utils.QueryParam(query, "d", "dir", "directory"))
	if err := utils.ValidateDirectoryPath(directory); err != nil {
		h.sendError(w, http.StatusBadRequest, "INVALID_DIRECTORY", fmt.Sprintf("Invalid directory path: %v", err))
		return
	}
	storageKey := utils.BuildStorageKey(directory, imageID)

	backend, err := h.getStorageBackendScoped(r, storageName, bucket)
	if err != nil {
		h.sendStorageBackendError(w, err)
		return
	}

	// Retrieve is the only call in the StorageBackend interface that returns
	// stored metadata, so the body is opened and closed unread. Backends stream
	// on demand, so nothing is transferred beyond the response header.
	reader, meta, err := backend.Retrieve(r.Context(), storageKey)
	if err != nil {
		if storage.IsNotFound(err) {
			h.sendError(w, http.StatusNotFound, "IMAGE_NOT_FOUND", "Image not found")
			return
		}
		h.sendError(w, http.StatusInternalServerError, "RETRIEVAL_ERROR", "Failed to read image metadata")
		return
	}
	_ = reader.Close()

	if meta == nil {
		h.sendError(w, http.StatusNotFound, "METADATA_NOT_FOUND", "The object exists but carries no metadata")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, ObjectMeta{
		Success:      true,
		ID:           imageID,
		StorageKey:   storageKey,
		Bucket:       bucket,
		Format:       meta.Format,
		ContentType:  meta.ContentType,
		Size:         meta.Size,
		Width:        meta.Width,
		Height:       meta.Height,
		OriginalName: meta.OriginalName,
		OwnerID:      meta.OwnerID,
		ETag:         meta.ETag,
		CreatedAt:    meta.CreatedAt,
		MaxAge:       meta.MaxAge,
		SMaxAge:      meta.SMaxAge,
	})
}
