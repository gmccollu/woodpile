//go:build darwin

package store

import "golang.org/x/sys/unix"

// DiskUsage returns the bytes available to this (unprivileged) process and the total
// size of the volume holding path.
func DiskUsage(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return st.Bavail * bs, st.Blocks * bs, nil
}
