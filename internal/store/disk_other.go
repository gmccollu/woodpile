//go:build !linux && !darwin && !windows

package store

import "errors"

// DiskUsage is not implemented on this OS; the low-disk guard stays off.
func DiskUsage(path string) (free, total uint64, err error) {
	return 0, 0, errors.New("disk usage is not supported on this OS")
}
