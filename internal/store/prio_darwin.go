//go:build darwin

package store

import "golang.org/x/sys/unix"

// lowerThreadPriority puts the calling OS thread in the background band (low CPU and
// throttled I/O). The caller must have locked the goroutine to its thread.
func lowerThreadPriority() error {
	return unix.Setpriority(unix.PRIO_DARWIN_THREAD, 0, unix.PRIO_DARWIN_BG)
}
