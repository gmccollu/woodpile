//go:build windows

package store

import "golang.org/x/sys/windows"

// DiskUsage returns the bytes available to this process (honoring quotas) and the
// total size of the volume holding path.
func DiskUsage(path string) (free, total uint64, err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var avail, tot, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &tot, &totalFree); err != nil {
		return 0, 0, err
	}
	return avail, tot, nil
}
