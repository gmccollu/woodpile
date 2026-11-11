//go:build !windows

package logfile

import "os"

func openShared(path string) (*os.File, error) { return os.Open(path) }
