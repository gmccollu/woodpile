//go:build windows

package service

import "os"

// linkCount isn't used on Windows (the installer sets ACLs, not owners).
func linkCount(os.FileInfo) uint64 { return 1 }
