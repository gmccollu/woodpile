//go:build windows

package logfile

import (
	"io/fs"
	"os"

	"github.com/gmccollu/woodshed/internal/atomicfile"
)

// openShared opens with FILE_SHARE_DELETE (atomicfile.Open).
func openShared(path string) (*os.File, error) { return atomicfile.Open(path) }

// entrySize returns a listed file's size. On Windows the size in a directory listing
// comes from the directory's index entry, which NTFS updates lazily for a file that
// is open for writing (it can lag by everything written since the open). The file's
// own metadata, read through a handle, is exact.
func entrySize(path string, _ fs.DirEntry) (int64, error) {
	f, err := openShared(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}
