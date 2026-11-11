//go:build !windows

package index

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// geteuid is a variable so tests can exercise the root path.
var geteuid = os.Geteuid

// prepareFiles checks the index files before SQLite opens them.
//
// As root it refuses a data dir owned by another user (ErrRunAsOwner): that user
// could plant a symlink or hard link named index.db, index.db-wal or index.db-shm,
// and root's open, chmod or SQLite's own open would then act on the link's target
// (/etc/shadow, say). Running as the data dir's owner needs no privileged file
// operation at all, so that's the only supported way (`sudo -u woodpile woodpile
// reindex`).
//
// For everyone it creates a missing index.db with O_EXCL|O_NOFOLLOW at 0640, opens
// existing files with O_NOFOLLOW (without O_CREATE), refuses anything that isn't a
// regular file with a single link, and tightens a too-open mode through the fd.
func prepareFiles(path string) error {
	dir := filepath.Dir(path)
	if geteuid() == 0 {
		st, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Uid != 0 {
			return fmt.Errorf("%w: %s belongs to uid %d; run this as that user (for example `sudo -u woodpile woodpile reindex`)",
				ErrRunAsOwner, dir, sys.Uid)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o640)
	if err == nil {
		f.Close()
	} else if !errors.Is(err, fs.ErrExist) {
		return err
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := checkFile(p); err != nil {
			return err
		}
	}
	return nil
}

// checkFile opens an existing index file without following links (and without
// blocking on a FIFO), and refuses anything but a regular file with one link. A
// missing -wal or -shm is fine.
func checkFile(p string) error {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) {
		return fmt.Errorf("%w: %s is a symbolic link", ErrUnsafeFile, p)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file (%v)", ErrUnsafeFile, p, st.Mode().Type())
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Nlink > 1 {
		return fmt.Errorf("%w: %s has %d hard links", ErrUnsafeFile, p, sys.Nlink)
	}
	if st.Mode().Perm()&^0o640 != 0 {
		return f.Chmod(st.Mode().Perm() & 0o640)
	}
	return nil
}
