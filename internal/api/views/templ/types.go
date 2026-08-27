// Package views holds the view models for falco's admin panel, plus the
// templ-generated rendering code.
//
// The *_templ.go files here are GENERATED from .templ sources — edit those, then
// re-run templ.
package views

import "time"

// PageData holds the common data passed to all pages.
type PageData struct {
	Title       string
	KeyName     string
	IsAdmin     bool
	Buckets     []BucketItem
	CurrentPage string
}

// BucketItem represents a bucket in the sidebar.
type BucketItem struct {
	Name       string
	Type       string
	IsDefault  bool
	ImageCount int
	Backups    []BackupItem
}

// BackupItem shows backup info for a bucket.
type BackupItem struct {
	Target string
	Mode   string
}

// DashboardData holds data for the main dashboard view.
type DashboardData struct {
	Page          PageData
	CurrentBucket string
	CurrentPrefix string
	Directories   []DirectoryInfo
	Images        []ImageInfo
	BucketInfo    *BucketDetail
	Error         string
}

// DirectoryInfo represents a folder in the file tree.
type DirectoryInfo struct {
	Name      string
	Path      string
	FileCount int
}

// ImageInfo represents an image in the grid.
type ImageInfo struct {
	ID          string
	Filename    string
	ContentType string
	Size        int64
	SizeHuman   string
	CreatedAt   time.Time
	Bucket      string
}

// BucketDetail holds detailed information about a bucket.
type BucketDetail struct {
	Name    string
	Type    string
	Backups []BackupItem
	Stats   *BucketStats
}

// BucketStats holds statistics for a bucket.
type BucketStats struct {
	TotalImages int64
	TotalSize   int64
	TotalHuman  string
}

// LoginData holds data for the login page.
type LoginData struct {
	Error string
}
