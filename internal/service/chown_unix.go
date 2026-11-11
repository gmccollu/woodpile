//go:build !windows

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// The installers run as root in folders the service account can write, so they
// never check a path and then act on it by name (re-test N10): every entry is
// opened without following a symlink (O_NOFOLLOW, and openat from its parent's
// descriptor inside the tree), checked with fstat and changed with fchown, all on
// the same descriptor, so nothing can be swapped in between.

const openFlags = unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_NOCTTY | unix.O_CLOEXEC

func (h *host) openNoFollow(path string, dir bool) (*os.File, error) {
	flags := openFlags
	if dir {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Open(h.path(path), flags, 0)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return nil, fmt.Errorf("%s is a link or not a %s", path, map[bool]string{true: "folder", false: "file"}[dir])
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), h.path(path)), nil
}

// chownTree gives dir and everything in it to user:group. Symlinks, special files
// and files with more than one link (a hard link to a file elsewhere) are skipped;
// it returns how many.
func (h *host) chownTree(dir, user, group string) (skipped int, err error) {
	uid, gid, err := h.lookupIDs(user, group)
	if err != nil {
		return 0, err
	}
	f, err := h.openNoFollow(dir, true)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return h.chownDirFD(f, uid, gid)
}

func (h *host) chownDirFD(d *os.File, uid, gid int) (skipped int, err error) {
	if err := h.fchown(d, uid, gid); err != nil {
		return 0, err
	}
	names, err := d.Readdirnames(-1)
	if err != nil {
		return 0, err
	}
	for _, name := range names {
		fd, err := unix.Openat(int(d.Fd()), name, openFlags, 0)
		if err != nil {
			skipped++ // a symlink (ELOOP), a socket, or gone meanwhile
			continue
		}
		f := os.NewFile(uintptr(fd), filepath.Join(d.Name(), name))
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			f.Close()
			return skipped, err
		}
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			n, err := h.chownDirFD(f, uid, gid)
			skipped += n
			if err != nil {
				f.Close()
				return skipped, err
			}
		case unix.S_IFREG:
			if st.Nlink != 1 {
				skipped++
			} else if err := h.fchown(f, uid, gid); err != nil {
				f.Close()
				return skipped, err
			}
		default:
			skipped++
		}
		f.Close()
	}
	return skipped, nil
}

// chownFile gives one regular file with one link to user:group.
func (h *host) chownFile(path, user, group string) error {
	uid, gid, err := h.lookupIDs(user, group)
	if err != nil {
		return err
	}
	f, err := h.openNoFollow(path, false)
	if err != nil {
		return err
	}
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		return fmt.Errorf("%s is a link or not a regular file; not changing its owner", path)
	}
	return h.fchown(f, uid, gid)
}

// chownDir gives one folder (not a symlink) to user:group.
func (h *host) chownDir(path, user, group string) error {
	uid, gid, err := h.lookupIDs(user, group)
	if err != nil {
		return err
	}
	f, err := h.openNoFollow(path, true)
	if err != nil {
		return err
	}
	defer f.Close()
	return h.fchown(f, uid, gid)
}
