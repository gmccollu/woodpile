//go:build windows

package store

// syncDir is a no-op on Windows, where NTFS journals renames and directories can't be
// fsynced.
func syncDir(dir string) {}
