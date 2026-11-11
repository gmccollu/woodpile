package listen

import "golang.org/x/sys/windows"

var errAddrInUse, errAccess, errAddrNotAvail error = windows.WSAEADDRINUSE, windows.WSAEACCES, windows.WSAEADDRNOTAVAIL
