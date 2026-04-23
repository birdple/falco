package ui

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	views "github.com/birdple/falco/internal/api/views/templ"
	"github.com/birdple/falco/internal/config"
	"github.com/birdple/falco/internal/pkg/logger"
	"github.com/birdple/falco/internal/storage"
)

// Handler serves the UI pages and HTMX partials.
type Handler struct {
	cfg        *config.Config
	registry   *storage.Registry
	cachedKeys map[string]config.KeyScope // computed once at startup
}

// NewHandler creates a new UI handler.
func NewHandler(cfg *config.Config, registry *storage.Registry) *Handler {
	return &Handler{
		cfg:        cfg,
		registry:   registry,
		cachedKeys: cfg.CollectAllKeys(),
	}
}

// uiScope holds the resolved access scope for a UI request.
type uiScope struct {
	IsAdmin bool
	KeyName string
	Buckets map[string]bool
}

// resolveKey validates the provided key and returns the scope.
func (h *Handler) resolveKey(key string) *uiScope {
	if key == "" {
		return nil
	}

	// Check admin key
	if h.cfg.Security.APIKey != "" && subtle.ConstantTimeCompare([]byte(key), []byte(h.cfg.Security.APIKey)) == 1 {
		return &uiScope{IsAdmin: true, KeyName: "admin"}
	}

	// Check scoped keys (cached at startup)
	var matched *uiScope
	for keyVal, scope := range h.cachedKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(keyVal)) == 1 {
			matched = &uiScope{
				KeyName: scope.Name,
				Buckets: scope.Buckets,
			}
		}
	}
	return matched
}

// getKeyFromRequest extracts the API key from cookie or header.
func getKeyFromRequest(r *http.Request) string {
	// Check cookie first (UI sessions)
	if c, err := r.Cookie("falco_key"); err == nil && c.Value != "" {
		return c.Value
	}
	// Check header (HTMX requests)
	if k := r.Header.Get("X-API-Key"); k != "" {
		return k
	}
	return ""
}

// accessibleBuckets returns the list of bucket names the scope can access.
// Filters out the internal "default" alias to avoid duplicates.
func (h *Handler) accessibleBuckets(scope *uiScope) []string {
	allNames := h.registry.Names()
	var filtered []string
	for _, name := range allNames {
		// Skip the internal "default" alias — it duplicates an actual bucket
		if name == "default" {
			continue
		}
		if scope == nil || scope.IsAdmin || scope.Buckets[name] {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

// buildBucketItems builds the sidebar bucket list with backup info.
func (h *Handler) buildBucketItems(ctx context.Context, names []string) []views.BucketItem {
	items := make([]views.BucketItem, 0, len(names))
	defaultName := h.cfg.GetDefaultBucketName()

	for _, name := range names {
		bucketCfg, err := h.cfg.GetBucketConfig(name)
		if err != nil {
			continue
		}

		item := views.BucketItem{
			Name:      name,
			Type:      bucketCfg.Type,
			IsDefault: name == defaultName,
		}

		// Count images via GetStats (avoids loading all objects)
		if backend, err := h.registry.Get(name); err == nil {
			if stats, err := backend.GetStats(ctx); err == nil {
				item.ImageCount = int(stats.TotalImages)
			}
		}

		// Add backup info
		for _, bk := range bucketCfg.Backups {
			item.Backups = append(item.Backups, views.BackupItem{
				Target: bk.Target,
				Mode:   bk.Mode,
			})
		}

		items = append(items, item)
	}

	// Sort: default first, then alphabetically
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDefault != items[j].IsDefault {
			return items[i].IsDefault
		}
		return items[i].Name < items[j].Name
	})

	return items
}

// Login renders the login page.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	// If auth is not required, redirect straight to dashboard
	if !h.cfg.Security.APIKeyRequired {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}

	// If already authenticated, redirect to dashboard
	key := getKeyFromRequest(r)
	if scope := h.resolveKey(key); scope != nil {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	views.LoginPage(views.LoginData{}).Render(r.Context(), w)
}

// AuthPost validates the key and sets a cookie.
func (h *Handler) AuthPost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "Invalid request"})
		return
	}

	scope := h.resolveKey(body.Key)
	if scope == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "Invalid API key"})
		return
	}

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "falco_key",
		Value:    body.Key,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400 * 7, // 7 days
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"name":    scope.KeyName,
		"admin":   scope.IsAdmin,
	})
}

// LogoutPost clears the session cookie. Needed because the cookie is HttpOnly
// and cannot be cleared from JavaScript.
func (h *Handler) LogoutPost(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "falco_key",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

// Dashboard renders the main dashboard page.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	key := getKeyFromRequest(r)
	scope := h.resolveKey(key)

	// If auth required and no valid key, redirect to login
	if h.cfg.Security.APIKeyRequired && scope == nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	// If no auth required, create an admin scope
	if scope == nil {
		scope = &uiScope{IsAdmin: true, KeyName: "local"}
	}

	ctx := r.Context()
	bucketNames := h.accessibleBuckets(scope)
	bucketItems := h.buildBucketItems(ctx, bucketNames)

	// Determine current bucket
	currentBucket := r.URL.Query().Get("bucket")
	if currentBucket == "" {
		currentBucket = h.cfg.GetDefaultBucketName()
	}
	// Verify access
	if !scope.IsAdmin && !scope.Buckets[currentBucket] {
		if len(bucketNames) > 0 {
			currentBucket = bucketNames[0]
		}
	}

	currentPrefix := r.URL.Query().Get("prefix")

	data := h.buildDashboardData(ctx, scope, bucketItems, currentBucket, currentPrefix)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	views.DashboardPage(data).Render(ctx, w)
}

// Content returns the main content area as an HTMX partial.
func (h *Handler) Content(w http.ResponseWriter, r *http.Request) {
	key := getKeyFromRequest(r)
	scope := h.resolveKey(key)

	if h.cfg.Security.APIKeyRequired && scope == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if scope == nil {
		scope = &uiScope{IsAdmin: true, KeyName: "local"}
	}

	ctx := r.Context()
	bucketNames := h.accessibleBuckets(scope)
	bucketItems := h.buildBucketItems(ctx, bucketNames)

	currentBucket := r.URL.Query().Get("bucket")
	if currentBucket == "" {
		currentBucket = h.cfg.GetDefaultBucketName()
	}
	currentPrefix := r.URL.Query().Get("prefix")

	data := h.buildDashboardData(ctx, scope, bucketItems, currentBucket, currentPrefix)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	views.MainContent(data).Render(ctx, w)
}

// buildDashboardData constructs the full DashboardData for a given scope + bucket.
func (h *Handler) buildDashboardData(ctx context.Context, scope *uiScope, bucketItems []views.BucketItem, currentBucket, currentPrefix string) views.DashboardData {
	page := views.PageData{
		Title:       "Dashboard",
		KeyName:     scope.KeyName,
		IsAdmin:     scope.IsAdmin,
		Buckets:     bucketItems,
		CurrentPage: "dashboard",
	}

	data := views.DashboardData{
		Page:          page,
		CurrentBucket: currentBucket,
		CurrentPrefix: currentPrefix,
	}

	// Get storage backend
	backend, err := h.registry.Get(currentBucket)
	if err != nil {
		data.Error = "Bucket not found: " + currentBucket
		return data
	}

	// List all files to build folder tree + image list
	results, err := backend.List(ctx, "")
	if err != nil {
		data.Error = err.Error()
		return data
	}

	matchPrefix := currentPrefix
	if matchPrefix != "" && matchPrefix[len(matchPrefix)-1] != '/' {
		matchPrefix += "/"
	}

	directoryMap := make(map[string]*views.DirectoryInfo)

	for _, res := range results {
		// Extract top-level directories
		if strings.Contains(res.Key, "/") {
			parts := strings.SplitN(res.Key, "/", 2)
			dirName := parts[0]
			if _, exists := directoryMap[dirName]; !exists {
				directoryMap[dirName] = &views.DirectoryInfo{
					Name:      dirName,
					Path:      dirName,
					FileCount: 0,
				}
			}
			directoryMap[dirName].FileCount++
		}

		// Filter images for current prefix
		if matchPrefix == "" || strings.HasPrefix(res.Key, matchPrefix) {
			if matchPrefix == "" && strings.Contains(res.Key, "/") {
				continue
			}

			relKey := res.Key
			if matchPrefix != "" {
				relKey = strings.TrimPrefix(res.Key, matchPrefix)
			}

			if strings.Contains(relKey, "/") {
				continue
			}

			data.Images = append(data.Images, views.ImageInfo{
				ID:          res.Key,
				Filename:    relKey,
				ContentType: inferFormat(relKey),
				Size:        res.Size,
				SizeHuman:   humanizeBytes(res.Size),
				CreatedAt:   res.Modified,
				Bucket:      currentBucket,
			})
		}
	}

	// Build directories
	for _, dir := range directoryMap {
		data.Directories = append(data.Directories, *dir)
	}
	sort.Slice(data.Directories, func(i, j int) bool {
		return strings.ToLower(data.Directories[i].Name) < strings.ToLower(data.Directories[j].Name)
	})

	// Bucket detail
	if bucketCfg, err := h.cfg.GetBucketConfig(currentBucket); err == nil {
		detail := &views.BucketDetail{
			Name: currentBucket,
			Type: bucketCfg.Type,
		}
		for _, bk := range bucketCfg.Backups {
			detail.Backups = append(detail.Backups, views.BackupItem{
				Target: bk.Target,
				Mode:   bk.Mode,
			})
		}

		// Stats via GetStats (avoids recomputing from full list)
		if stats, err := backend.GetStats(ctx); err == nil {
			detail.Stats = &views.BucketStats{
				TotalImages: stats.TotalImages,
				TotalSize:   stats.TotalSize,
				TotalHuman:  humanizeBytes(stats.TotalSize),
			}
		} else {
			logger.Warn().Err(err).Str("bucket", currentBucket).Msg("GetStats failed, showing zeroed stats")
			detail.Stats = &views.BucketStats{}
		}
		data.BucketInfo = detail
	}

	return data
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Error().Err(err).Msg("Failed to write JSON response")
	}
}

// inferFormat guesses the format from the filename.
func inferFormat(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".webp"):
		return "WEBP"
	case strings.HasSuffix(lower, ".png"):
		return "PNG"
	case strings.HasSuffix(lower, ".jpg"), strings.HasSuffix(lower, ".jpeg"):
		return "JPEG"
	case strings.HasSuffix(lower, ".gif"):
		return "GIF"
	case strings.HasSuffix(lower, ".svg"):
		return "SVG"
	case strings.HasSuffix(lower, ".avif"):
		return "AVIF"
	default:
		return "IMG"
	}
}

// humanizeBytes formats bytes into a human-readable string.
func humanizeBytes(s int64) string {
	sizes := []string{"B", "KB", "MB", "GB", "TB"}
	if s < 10 {
		return fmt.Sprintf("%d B", s)
	}

	base := float64(1024)
	e := 0
	f := float64(s)
	for f >= base && e < len(sizes)-1 {
		f /= base
		e++
	}

	return fmt.Sprintf("%.1f %s", f, sizes[e])
}
