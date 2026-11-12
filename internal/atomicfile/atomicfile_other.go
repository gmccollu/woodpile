//go:build !windows

package atomicfile

import "os"

const isWindows = false

func openShared(path string) (*os.File, error) { return os.Open(path) }

// isTransient: Unix renames don't fail because another process has the file open.
func isTransient(error) bool { return false }
