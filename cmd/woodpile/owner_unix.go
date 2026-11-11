//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/gmccollu/woodpile/internal/config"
)

// saveConfigKeepingOwner rewrites the config at path with c, keeping its owner and
// mode (security review L3) without acting on any path by name after checking it
// (re-test N10): the old file is opened with O_NOFOLLOW and checked with fstat; the
// new one is a temp file in the same folder, given the owner and mode through its
// descriptor, written, synced, and renamed over the old name (a rename replaces
// whatever is at that name, a planted symlink included, and never follows it).
func saveConfigKeepingOwner(path string, c config.Config) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ELOOP) {
		return fmt.Errorf("%s is a symlink; refusing to rewrite it", path)
	}
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	var st unix.Stat_t
	err = unix.Fstat(fd, &st)
	unix.Close(fd)
	if err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		return fmt.Errorf("%s is a link or not a regular file; refusing to rewrite it", path)
	}
	b, err := config.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+config.FileName+"-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if os.Geteuid() == 0 {
		if err := f.Chown(int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
	}
	if err := f.Chmod(os.FileMode(st.Mode & 0o777)); err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// checkOwnedByMe refuses an existing path that is a symlink, or owned by
// someone other than this user (or, under sudo, the user who ran sudo), before
// portable mode adopts it (re-test N13).
func checkOwnedByMe(path string) error {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; portable mode won't use it", path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	me := os.Geteuid()
	if int(st.Uid) == me {
		return nil
	}
	if me == 0 {
		if sudo := os.Getenv("SUDO_UID"); sudo != "" && sudo == fmt.Sprint(st.Uid) {
			return nil
		}
	}
	return fmt.Errorf("%s belongs to another user (uid %d); portable mode only uses its own folders. "+
		"Use your own copy, or pass --dir with a folder of yours", path, st.Uid)
}
