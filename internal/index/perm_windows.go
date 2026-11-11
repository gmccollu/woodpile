//go:build windows

package index

// prepareFiles is a no-op on Windows: the data folder's ACL (set by the installer)
// governs access, and files inherit it.
func prepareFiles(path string) error { return nil }
