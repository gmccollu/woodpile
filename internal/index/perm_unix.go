//go:build !windows

package index

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// geteuid is a variable so tests can exercise the root path.
var geteuid = os.Geteuid

// prepareFiles creates the database file 0640 if it's missing, tightens the database
// and its -wal/-shm files to 0640 if they're more open, and, when running as root,
// gives them the data dir's owner. Then `woodpile reindex` run by root on a
// service-owned data dir doesn't leave a root-owned index the service can't write.
// SQLite creates -wal/-shm with the database's mode and, as root, its owner; this
// covers files left by older builds too.
func prepareFiles(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return err
	}
	f.Close()
	var uid, gid = -1, -1
	if geteuid() == 0 {
		if st, err := os.Stat(filepath.Dir(path)); err == nil {
			if sys, ok := st.Sys().(*syscall.Stat_t); ok {
				uid, gid = int(sys.Uid), int(sys.Gid)
			}
		}
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		st, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if st.Mode().Perm()&^0o640 != 0 {
			if err := os.Chmod(p, st.Mode().Perm()&0o640); err != nil {
				return err
			}
		}
		if uid >= 0 {
			if sys, ok := st.Sys().(*syscall.Stat_t); ok && (int(sys.Uid) != uid || int(sys.Gid) != gid) {
				if err := os.Chown(p, uid, gid); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
