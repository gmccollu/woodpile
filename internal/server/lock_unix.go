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

// MatchOwner gives the lock file, through its descriptor, the data folder's owner,
// when this process is root and created it there (re-test N10): a `sudo woodpile
// reindex` must not leave a root-owned lock the service can't open.
func (l *DirLock) MatchOwner(dataDir string) error {
	if l == nil || l.f == nil || os.Geteuid() != 0 {
		return nil
	}
	// Followed if it's a symlink: the lock was created in the folder it points at
	// (re-test W5).
	fd, err := syscall.Open(dataDir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	var dir, lock syscall.Stat_t
	err = syscall.Fstat(fd, &dir)
	syscall.Close(fd)
	if err != nil {
		return err
	}
	if err := syscall.Fstat(int(l.f.Fd()), &lock); err != nil {
		return err
	}
	if lock.Uid == dir.Uid && lock.Gid == dir.Gid {
		return nil
	}
	return l.f.Chown(int(dir.Uid), int(dir.Gid))
}
