package loadgen

import "golang.org/x/sys/windows"

var errNoBufs, errConnRefused error = windows.WSAENOBUFS, windows.WSAECONNREFUSED
