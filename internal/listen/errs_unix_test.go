//go:build !windows

package listen

import "syscall"

var errAddrInUse, errAccess, errAddrNotAvail error = syscall.EADDRINUSE, syscall.EACCES, syscall.EADDRNOTAVAIL
