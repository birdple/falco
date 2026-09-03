//go:build linux

package storage

import "syscall"

// statBlockSize returns the filesystem block size from a statfs result.
//
// It exists per platform because the field's type is not portable: Statfs_t.Bsize
// is int64 on Linux and uint32 on Darwin. Writing int64(stat.Bsize) inline
// compiles on both, but the conversion is redundant here and required there, so
// unconvert flags it on Linux while removing it breaks the macOS build — and a
// //nolint would then be reported as unused on macOS, where the linter never
// fires. Two small files are the way out of that.
func statBlockSize(stat *syscall.Statfs_t) int64 {
	return stat.Bsize
}
