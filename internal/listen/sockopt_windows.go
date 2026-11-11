package listen

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func sockRcvbuf(fd uintptr) int {
	var n int32
	l := int32(unsafe.Sizeof(n))
	if err := windows.Getsockopt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_RCVBUF, (*byte)(unsafe.Pointer(&n)), &l); err != nil {
		return 0
	}
	return int(n)
}
