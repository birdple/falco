// Package views holds the view models for falco's admin panel, plus the
// templ-generated rendering code.
//
// The *_templ.go files here are GENERATED from the .templ sources — edit those
// and run `make ui`. CI fails when they drift.
package views

import (
	"fmt"
	"strconv"
	"time"
)

// PageData is what every page needs to draw the shell.
type PageData struct {
	Title string
	// Theme is resolved on the server from a cookie, so the correct colours
	// are in the first byte of HTML. Doing it from an inline script meant the
	// panel's own CSP blocked it and every load flashed white.
	Theme string
	// CSRFToken is published in a <meta> and attached by app.js to every
	// mutating request.
	CSRFToken string

	KeyName string
	IsAdmin bool
	Version string

	// CurrentPage drives the nav highlight: "explorer", "playground",
	// "signer" or "ops".
	CurrentPage string
	Buckets     []BucketItem
}

// IsDark reports whether the dark palette applies.
func (p PageData) IsDark() bool { return p.Theme != "light" }

// BucketItem is one bucket in the sidebar.
type BucketItem struct {
	Name      string
	Type      string
	IsDefault bool
	// Objects is the object count, or nil when the backend could not be asked.
	// A zero would read as "empty bucket", which is a different fact.
	Objects   *int64
	SizeHuman string
	// Healthy is nil when health was not checked on this render.
	Healthy *bool
	Backups []BackupItem
	// Error explains why the numbers are missing, when they are.
	Error string
}

// CountLabel renders the object count, or a dash when it is unknown.
func (b BucketItem) CountLabel() string {
	if b.Objects == nil {
		return "—"
	}
	return strconv.FormatInt(*b.Objects, 10)
}

// BackupItem is a replication target of a bucket.
type BackupItem struct {
	Target string
	Mode   string
}

// Crumb is one step of the folder breadcrumb.
type Crumb struct {
	Name string
	// Prefix is the full path this crumb navigates to; empty is the bucket
	// root.
	Prefix string
	IsLast bool
}

// FolderItem is a sub-folder of the current prefix.
type FolderItem struct {
	Name   string
	Prefix string
	// Objects is the count of everything below, when known. Paginated
	// listings get folders from the backend's delimiter roll-up, which carries
	// no count.
	Objects *int
}

// ObjectItem is one stored object in the grid.
type ObjectItem struct {
	// Key is the full storage key; Name is what is shown.
	Key  string
	Name string
	// DOMID is a hash of the key. The key itself cannot be used: it contains
	// slashes and dots, so "#image-avatars/a1b2" is not a valid CSS selector
	// and throws in querySelector.
	DOMID string

	// ThumbURL and FullURL are signed on the server. The panel never holds the
	// HMAC key in the browser, and without a signature every thumbnail is a
	// 403 in any deployment with HMAC_REQUIRED=true — which is the deployment
	// that matters.
	ThumbURL string
	FullURL  string

	Size          int64
	SizeHuman     string
	Modified      time.Time
	ModifiedHuman string

	// Format is read from stored metadata where available. It is NOT guessed
	// from the key: falco stores content-hashed keys with no extension, so
	// guessing could only ever answer "IMG" — which is exactly what the old
	// badge always said.
	Format      string
	ContentType string
}

// ExplorerData drives the storage browser.
type ExplorerData struct {
	Page PageData

	Bucket     string
	BucketType string
	Prefix     string
	Crumbs     []Crumb

	Folders []FolderItem
	Objects []ObjectItem

	// Paginated is false when the backend cannot page. The UI says so rather
	// than showing controls that would silently return the same first page.
	Paginated  bool
	NextCursor string
	// CursorStack carries the cursors already visited, so "back" works without
	// server-side state.
	CursorStack string
	HasPrev     bool

	Stats  *BucketStats
	Upload UploadConfig

	Error string
}

// BucketStats summarises a bucket for the explorer header.
type BucketStats struct {
	Objects    int64
	SizeHuman  string
	FreeHuman  string
	HasFree    bool
	Backups    []BackupItem
	StatsError string
}

// UploadConfig carries the real limits into the upload dialog. The old panel
// hardcoded "PNG, JPG, WebP up to 10MB" in the markup while the limit was
// configurable and the accepted types were a different list.
type UploadConfig struct {
	Buckets       []string
	Bucket        string
	Prefix        string
	MaxFileSizeMB int
	AcceptAttr    string
	AcceptLabel   string
}

// ObjectDetail is the full view of one object.
type ObjectDetail struct {
	Page PageData

	Bucket string
	Key    string
	Name   string

	FullURL string
	// SignedURL is a ready-to-copy signed delivery URL, with its expiry.
	SignedURL string
	ExpiresAt time.Time
	CanSign   bool
	SignNote  string

	Format       string
	ContentType  string
	SizeHuman    string
	Width        int
	Height       int
	OriginalName string
	OwnerID      string
	ETag         string
	CreatedAt    time.Time

	Error string
}

// ParamField describes one transformation control in the playground.
//
// The controls are data rather than hand-written markup so that the list stays
// next to the parser's contract: every parameter delivery accepts appears here
// exactly once.
type ParamField struct {
	Name  string
	Label string
	// Kind is "number", "text", "select", "toggle" or "color".
	Kind    string
	Options []string
	Min     string
	Max     string
	Step    string
	// Default is shown as the placeholder, so an empty control reads as "the
	// default applies" rather than as zero.
	Default string
	Help    string
	// Strict marks the parameters that answer 400 when malformed, as opposed
	// to the cosmetic ones that fall back to their default.
	Strict bool
	// Disabled and DisabledReason grey out a control that cannot work in this
	// deployment, instead of letting it fail at use time.
	Disabled       bool
	DisabledReason string
}

// ParamGroup is a titled set of controls.
type ParamGroup struct {
	Title  string
	Note   string
	Fields []ParamField
}

// PlaygroundData drives the transformation playground.
type PlaygroundData struct {
	Page PageData

	Bucket  string
	Key     string
	Name    string
	Buckets []string

	// BaseURL is the unsigned delivery path; the browser asks the panel to
	// sign each preview so the HMAC key never leaves the server.
	BaseURL string
	Groups  []ParamGroup

	OriginalSizeHuman string
	OriginalWidth     int
	OriginalHeight    int
	OriginalFormat    string

	Error string
}

// SignerData drives the URL signer.
type SignerData struct {
	Page PageData

	Enabled        bool
	DisabledReason string
	RequireExpiry  bool
	Buckets        []string
	DefaultTTL     int
}

// SignedResult is the answer of a signing request, rendered as a fragment.
type SignedResult struct {
	SignedURL string
	ExpiresAt time.Time
	HasExpiry bool
	Error     string
}

// BackendStatus is one storage backend on the ops screen.
type BackendStatus struct {
	Name    string
	Type    string
	OK      bool
	Error   string
	Breaker string

	Objects   *int64
	SizeHuman string
	FreeHuman string
	HasFree   bool

	// Paginated reports whether this backend can list by page. Stated rather
	// than assumed: it changes what the explorer can do.
	Paginated bool
	Backups   []BackupItem
}

// CacheView is the cache panel on the ops screen.
type CacheView struct {
	Enabled         bool
	Backend         string
	Hits            int64
	Misses          int64
	HitRatio        float64
	ItemCount       int
	SizeHuman       string
	MaxHuman        string
	UsedPercent     float64
	TTLHours        int
	CleanupInterval string
	Note            string
}

// FeatureState is one line of the "what is switched off" list.
type FeatureState struct {
	Name    string
	Enabled bool
	// Reason explains a disabled feature in terms of the variable that would
	// turn it on. A feature that is silently unavailable is worse than one
	// that says why.
	Reason string
}

// OpsData drives the operations screen.
type OpsData struct {
	Page PageData

	Version  string
	Uptime   string
	Status   string
	Backends []BackendStatus
	Cache    CacheView
	Features []FeatureState
	Sessions int

	// MetricsEnabled says whether /metrics is mounted at all.
	MetricsEnabled bool

	Config []ConfigItem
}

// ConfigItem is one effective configuration value. Secrets never appear here:
// they are reported as set/unset only.
type ConfigItem struct {
	Name  string
	Value string
	// Secret marks a value that is shown as "set"/"not set" instead of
	// verbatim.
	Secret bool
}

// LoginData drives the sign-in page.
type LoginData struct {
	Theme string
	Error string
	// Disabled marks the panel as unavailable because nothing is configured to
	// authenticate against. It is a refusal, not a blank form: the old panel
	// answered that situation by granting admin access to anyone.
	Disabled bool
}

// IsDark reports whether the dark palette applies.
func (l LoginData) IsDark() bool { return l.Theme != "light" }

// ActionResult is the outcome of a mutating panel action, rendered as a toast.
//
// Failed is a list and not a flag on purpose: a batch delete or upload can
// partially succeed, and the panel has to name what did not happen rather than
// report a blanket success.
type ActionResult struct {
	Title     string
	OK        bool
	Succeeded []string
	Failed    []ActionFailure
	Detail    string
}

// ActionFailure is one item that did not go through, and why.
type ActionFailure struct {
	Name   string
	Reason string
}

// HumanizeBytes formats a byte count for display.
func HumanizeBytes(s int64) string {
	if s < 0 {
		return "—"
	}
	if s < 1024 {
		return fmt.Sprintf("%d B", s)
	}
	sizes := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	base := float64(1024)
	e := 0
	f := float64(s)
	for f >= base && e < len(sizes)-1 {
		f /= base
		e++
	}
	return fmt.Sprintf("%.1f %s", f, sizes[e])
}

// HumanizeTime renders a timestamp, or a dash when it was never set.
func HumanizeTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2 Jan 2006, 15:04")
}
