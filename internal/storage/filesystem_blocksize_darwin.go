//go:build darwin

package storage

import "syscall"

// statBlockSize returns the filesystem block size from a statfs result.
// See the Linux variant of this file for why it is split by platform.
func statBlockSize(stat *syscall.Statfs_t) int64 {
	return int64(stat.Bsize)
}
