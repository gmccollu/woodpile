//go:build darwin

package store

import (
	"os"

	"golang.org/x/sys/unix"
)

// syncFile flushes a file to the device with fsync(2). Go's File.Sync on macOS uses
// F_FULLFSYNC, which also flushes the drive's cache and costs tens of milliseconds
// per call; with thousands of device files every few seconds that would dominate the
// disk. fsync(2) gives the same guarantee as on Linux: the data survives a process or
// OS crash.
func syncFile(f *os.File) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) {
		for {
			serr = unix.Fsync(int(fd))
			if serr != unix.EINTR {
				return
			}
		}
	}); err != nil {
		return err
	}
	return serr
}
