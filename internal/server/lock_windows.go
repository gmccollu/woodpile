//go:build windows

package server

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return ErrLocked
	}
	return err
}

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}

// openLockFile opens the lock file, creating it if create is set, and refuses a
// symlink, junction or other reparse point, or anything but a regular file.
func openLockFile(path string, create bool) (*os.File, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || !fi.Mode().IsRegular() {
			return nil, &os.PathError{Op: "open", Path: path, Err: errNotPlainFile}
		}
	}
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0o640)
	if err != nil {
		return nil, err
	}
	// The file opened must be the one checked (no swap in between).
	li, lerr := os.Lstat(path)
	fi, ferr := f.Stat()
	if lerr != nil || ferr != nil || li.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || !os.SameFile(li, fi) {
		f.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: errNotPlainFile}
	}
	return f, nil
}

// MatchOwner is a no-op on Windows (the lock inherits the data folder's ACL).
func (l *DirLock) MatchOwner(string) error { return nil }
