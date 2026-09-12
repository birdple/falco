package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	views "github.com/birdple/falco/internal/api/views/templ"
	"github.com/birdple/falco/internal/storage"
	"golang.org/x/sync/errgroup"
)

const (
	// explorerPageSize is how many objects one page of the grid holds.
	explorerPageSize = 60

	// thumbTTL is how long a thumbnail's signature stays valid. Short on
	// purpose: these URLs are minted per render and only have to outlive the
	// page that shows them.
	thumbTTL = 30 * time.Minute

	// thumbWidth is the rendered width of a grid thumbnail. Asking for a
	// transformation is also what makes the response cacheable — untransformed
	// delivery streams straight from storage and is never cached.
	thumbWidth = 480

	// statsTimeout bounds each bucket's stats call so a dead backend cannot
	// hold the page.
	statsTimeout = 3 * time.Second
)

// Dashboard renders the explorer as a full page.
func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	data, err := h.buildExplorer(r)
	if err != nil {
		h.deny(w, r, http.StatusForbidden, err.Error())
		return
	}
	h.render(w, r, "explorer", views.ExplorerPage(data))
}

// Explorer renders the explorer body for an HTMX swap.
func (h *Handler) Explorer(w http.ResponseWriter, r *http.Request) {
	data, err := h.buildExplorer(r)
	if err != nil {
		h.deny(w, r, http.StatusForbidden, err.Error())
		return
	}
	h.render(w, r, "explorer-partial", views.Explorer(data))
}

// buildExplorer assembles the explorer view for the current request.
func (h *Handler) buildExplorer(r *http.Request) (views.ExplorerData, error) {
	sess := h.sessionFrom(r)
	query := r.URL.Query()

	bucket, err := h.resolveBucket(sess.Scope, query.Get("bucket"))
	if err != nil {
		return views.ExplorerData{}, err
	}

	prefix := normalizePrefix(query.Get("prefix"))
	cursor := query.Get("cursor")
	stack := query.Get("stack")

	names := h.accessibleBuckets(sess.Scope)
	data := views.ExplorerData{
		Page:        h.pageData(r, sess, "Storage", "explorer", h.bucketItems(r.Context(), names, bucket)),
		Bucket:      bucket,
		BucketType:  h.bucketType(bucket),
		Prefix:      prefix,
		Crumbs:      breadcrumbs(prefix),
		CursorStack: stack,
		HasPrev:     stack != "",
		Upload: views.UploadConfig{
			Buckets:       names,
			Bucket:        bucket,
			Prefix:        prefix,
			MaxFileSizeMB: h.cfg.Processing.MaxFileSizeMB,
			AcceptAttr:    acceptAttr,
			AcceptLabel:   h.acceptLabel(),
		},
	}

	backend, err := h.registry.Get(bucket)
	if err != nil {
		data.Error = "Bucket not found: " + bucket
		return data, nil
	}

	data.Stats = h.bucketStats(r.Context(), backend, bucket)

	pager, ok := backend.(storage.PagedLister)
	if !ok {
		// Said out loud. A backend that cannot page is a real limitation of
		// the deployment, not something to paper over with a fake page.
		data.Paginated = false
		objects, err := backend.List(r.Context(), prefix)
		if err != nil {
			data.Error = err.Error()
			return data, nil
		}
		folders, items := splitListing(objects, prefix)
		data.Folders = folders
		data.Objects = h.decorate(items, bucket)
		return data, nil
	}

	data.Paginated = true
	page, err := pager.ListPage(r.Context(), storage.ListOptions{
		Prefix:    prefix,
		Delimiter: "/",
		Cursor:    cursor,
		MaxKeys:   explorerPageSize,
	})
	if err != nil {
		data.Error = err.Error()
		return data, nil
	}

	data.NextCursor = ""
	if page.IsTruncated {
		data.NextCursor = page.NextCursor
	}

	for _, cp := range page.CommonPrefixes {
		name := strings.TrimSuffix(strings.TrimPrefix(cp, prefix), "/")
		if name == "" {
			continue
		}
		data.Folders = append(data.Folders, views.FolderItem{Name: name, Prefix: cp})
	}

	items := make([]storage.ListResult, 0, len(page.Objects))
	items = append(items, page.Objects...)
	data.Objects = h.decorate(items, bucket)

	return data, nil
}

// decorate turns storage results into grid items, signing each thumbnail.
func (h *Handler) decorate(items []storage.ListResult, bucket string) []views.ObjectItem {
	out := make([]views.ObjectItem, 0, len(items))
	for _, item := range items {
		name := item.Key
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}

		out = append(out, views.ObjectItem{
			Key:           item.Key,
			Name:          name,
			DOMID:         domID(item.Key),
			ThumbURL:      h.signedThumb(bucket, item.Key, thumbWidth),
			FullURL:       h.signedThumb(bucket, item.Key, 0),
			Size:          item.Size,
			SizeHuman:     views.HumanizeBytes(item.Size),
			Modified:      item.Modified,
			ModifiedHuman: views.HumanizeTime(item.Modified),
			ContentType:   item.ContentType,
			Format:        formatLabel(item.ContentType),
		})
	}
	return out
}

// bucketItems builds the sidebar, asking every bucket for its stats
// concurrently.
//
// The old sidebar did this in series, with jay's own 5 s client timeout
// underneath: N unreachable buckets cost N×5 s on every single page view,
// against a 30 s request budget.
func (h *Handler) bucketItems(ctx context.Context, names []string, current string) []views.BucketItem {
	items := make([]views.BucketItem, len(names))
	defaultName := h.cfg.GetDefaultBucketName()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)

	for i, name := range names {
		g.Go(func() error {
			item := views.BucketItem{
				Name:      name,
				Type:      h.bucketType(name),
				IsDefault: name == defaultName,
			}
			if cfg, err := h.cfg.GetBucketConfig(name); err == nil {
				for _, bk := range cfg.Backups {
					item.Backups = append(item.Backups, views.BackupItem{Target: bk.Target, Mode: bk.Mode})
				}
			}

			backend, err := h.registry.Get(name)
			if err != nil {
				item.Error = err.Error()
				items[i] = item
				return nil
			}

			callCtx, cancel := context.WithTimeout(gctx, statsTimeout)
			defer cancel()

			stats, err := backend.GetStats(callCtx)
			if err != nil {
				// Left as nil, which renders as a dash. A zero here would
				// claim the bucket is empty, which is a different fact.
				item.Error = err.Error()
				items[i] = item
				return nil
			}
			count := stats.TotalImages
			item.Objects = &count
			item.SizeHuman = views.HumanizeBytes(stats.TotalSize)
			items[i] = item
			return nil
		})
	}
	_ = g.Wait()

	return items
}

// bucketStats summarises the current bucket for the header.
func (h *Handler) bucketStats(ctx context.Context, backend storage.StorageBackend, bucket string) *views.BucketStats {
	out := &views.BucketStats{}
	if cfg, err := h.cfg.GetBucketConfig(bucket); err == nil {
		for _, bk := range cfg.Backups {
			out.Backups = append(out.Backups, views.BackupItem{Target: bk.Target, Mode: bk.Mode})
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, statsTimeout)
	defer cancel()

	stats, err := backend.GetStats(callCtx)
	if err != nil {
		out.StatsError = err.Error()
		return out
	}
	out.Objects = stats.TotalImages
	out.SizeHuman = views.HumanizeBytes(stats.TotalSize)
	if stats.FreeSpace > 0 {
		out.HasFree = true
		out.FreeHuman = views.HumanizeBytes(stats.FreeSpace)
	}
	return out
}

// splitListing derives one level of folders and objects from a flat listing,
// for backends that cannot roll up prefixes themselves.
func splitListing(results []storage.ListResult, prefix string) ([]views.FolderItem, []storage.ListResult) {
	var objects []storage.ListResult
	folders := map[string]*views.FolderItem{}
	var order []string

	for _, res := range results {
		rest := strings.TrimPrefix(res.Key, prefix)
		name, _, nested := strings.Cut(rest, "/")
		if !nested {
			objects = append(objects, res)
			continue
		}
		if _, seen := folders[name]; !seen {
			count := 0
			folders[name] = &views.FolderItem{Name: name, Prefix: prefix + name + "/", Objects: &count}
			order = append(order, name)
		}
		*folders[name].Objects++
	}

	out := make([]views.FolderItem, 0, len(order))
	for _, name := range order {
		out = append(out, *folders[name])
	}
	return out, objects
}

// breadcrumbs turns a prefix into navigable steps, at any depth.
//
// The old dashboard only ever looked at the first path segment and discarded
// everything below it, so "avatars/2024/x.webp" had no folder to click and no
// row in the grid: it was simply unreachable.
func breadcrumbs(prefix string) []views.Crumb {
	trimmed := strings.Trim(prefix, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	out := make([]views.Crumb, 0, len(parts))
	acc := ""
	for i, part := range parts {
		acc += part + "/"
		out = append(out, views.Crumb{
			Name:   part,
			Prefix: acc,
			IsLast: i == len(parts)-1,
		})
	}
	return out
}

// normalizePrefix makes a prefix safe and directory-shaped.
func normalizePrefix(raw string) string {
	p := strings.TrimSpace(raw)
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return ""
	}
	// A prefix is data, not a path to walk: anything that could climb out of
	// the bucket is refused outright rather than cleaned up into something
	// that still resolves somewhere.
	if strings.Contains(p, "..") {
		return ""
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// domID hashes a storage key into something usable as an HTML id.
//
// The key itself cannot be used: it contains slashes and dots, so an id like
// "image-avatars/a1b2" produces "#image-avatars/a1b2", which is not a valid CSS
// selector and throws inside querySelector — which is exactly what happened to
// the delete button inside any folder.
func domID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// formatLabel turns a content type into a short badge.
//
// It reads the content type, never the key. falco stores content-hashed keys
// with no extension, so the old badge — which switched on the file extension —
// could only ever fall through to "IMG", for every object, forever.
func formatLabel(contentType string) string {
	if contentType == "" {
		return "—"
	}
	ct := contentType
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = ct[:idx]
	}
	_, sub, ok := strings.Cut(strings.TrimSpace(ct), "/")
	if !ok || sub == "" {
		return "—"
	}
	sub = strings.TrimPrefix(sub, "x-")
	if sub == "jpeg" {
		return "JPG"
	}
	return strings.ToUpper(sub)
}

// signedThumb builds a delivery URL for the panel to display.
//
// Signing happens here, on the server, so the HMAC key never reaches the
// browser. Without it every thumbnail is a 403 in any deployment running with
// HMAC_REQUIRED=true — which is how the panel looked in the stack it actually
// ships in.
func (h *Handler) signedThumb(bucket, key string, width int) string {
	q := url.Values{}
	q.Set("b", bucket)
	if width > 0 {
		q.Set("w", itoa(width))
		// A format keeps the response inside the transform cache and off the
		// raw streaming path.
		q.Set("f", "webp")
	}

	path := "/api/v1/images/" + key
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	signed, err := h.signPath(path, thumbTTL)
	if err != nil {
		// Signing is unavailable (no HMAC key). The unsigned path still works
		// wherever HMAC is not required, and where it is, the image slot shows
		// as broken rather than the whole page failing.
		return path
	}
	return signed
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// acceptAttr is the accept attribute of the upload input.
const acceptAttr = "image/*"

// acceptLabel describes the real upload limits, read from configuration rather
// than written into the markup.
func (h *Handler) acceptLabel() string {
	return "Images up to " + itoa(h.cfg.Processing.MaxFileSizeMB) + " MB · SVG, HTML and XML are rejected"
}
