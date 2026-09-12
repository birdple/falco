package storage

// Compile-time proof that every backend falco ships can list by page.
// A backend that loses this silently would make the panel fall back to the
// unpaginated path without anyone noticing.
var (
	_ PagedLister = (*JayStorage)(nil)
	_ PagedLister = (*S3Storage)(nil)
	_ PagedLister = (*R2Storage)(nil)
	_ PagedLister = (*FilesystemStorage)(nil)
	_ PagedLister = (*ReplicatedStorage)(nil)
)
