//go:build unix

package listen

import (
	"runtime"

	"golang.org/x/sys/unix"
)

func sockRcvbuf(fd uintptr) int {
	n, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF)
	if err != nil {
		return 0
	}
	if runtime.GOOS == "linux" {
		n /= 2 // Linux reports double what was set (socket(7)); report the usable size
	}
	return n
}
