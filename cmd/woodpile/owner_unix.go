//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// ownership is a file's owner and mode, to put back after rewriting it.
type ownership struct {
	uid, gid int
	mode     os.FileMode
	ok       bool
}

// fileOwnership reads path's owner and mode; it refuses a symlink or a file with
// more than one link (it's about to be replaced and chowned as root).
func fileOwnership(path string) (ownership, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return ownership{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() || (ok && st.Nlink != 1) {
		return ownership{}, fmt.Errorf("%s is a link or not a regular file; refusing to rewrite it", path)
	}
	if !ok {
		return ownership{mode: fi.Mode().Perm()}, nil
	}
	return ownership{uid: int(st.Uid), gid: int(st.Gid), mode: fi.Mode().Perm(), ok: true}, nil
}

func (o ownership) restore(path string) error {
	if err := os.Chmod(path, o.mode); err != nil {
		return err
	}
	if o.ok && os.Geteuid() == 0 {
		return os.Lchown(path, o.uid, o.gid)
	}
	return nil
}
