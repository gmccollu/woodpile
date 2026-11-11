//go:build !windows

package logfile

import (
	"io/fs"
	"os"
)

func openShared(path string) (*os.File, error) { return os.Open(path) }

// entrySize returns a listed file's size. On Unix the directory entry's stat is exact.
func entrySize(path string, e fs.DirEntry) (int64, error) {
	info, err := e.Info()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
