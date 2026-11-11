//go:build linux

package store

import "golang.org/x/sys/unix"

// lowerThreadPriority sets the calling OS thread to nice 19 and the idle I/O class.
// The caller must have locked the goroutine to its thread.
func lowerThreadPriority() error {
	tid := unix.Gettid()
	err := unix.Setpriority(unix.PRIO_PROCESS, tid, 19)
	const ioprioWhoProcess, ioprioClassIdle, ioprioClassShift = 1, 3, 13
	if _, _, e := unix.Syscall(unix.SYS_IOPRIO_SET, ioprioWhoProcess, uintptr(tid), ioprioClassIdle<<ioprioClassShift); e != 0 && err == nil {
		err = e
	}
	return err
}
