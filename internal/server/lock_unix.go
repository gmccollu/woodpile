//go:build !windows

package server

import (
	"errors"
	"os"
	"syscall"
)

func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}

func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// openLockFile opens the lock file without following a symlink (O_NOFOLLOW) or
// blocking on a FIFO, creating it if create is set, and refuses anything but a
// regular file with one link.
func openLockFile(path string, create bool) (*os.File, error) {
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0o640)
	if errors.Is(err, syscall.ELOOP) {
		return nil, &os.PathError{Op: "open", Path: path, Err: errNotPlainFile}
	}
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.Mode().IsRegular() || (ok && st.Nlink != 1) {
		f.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: errNotPlainFile}
	}
	return f, nil
}
