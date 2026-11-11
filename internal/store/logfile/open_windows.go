//go:build windows

package logfile

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

func openShared(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if err == windows.ERROR_FILE_NOT_FOUND || err == windows.ERROR_PATH_NOT_FOUND {
			err = os.ErrNotExist
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

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
