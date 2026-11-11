package loadgen

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isNoBufs: the OS send queue is full (WSAENOBUFS, 10055, or WSAEWOULDBLOCK).
func isNoBufs(err error) bool {
	return errors.Is(err, windows.WSAENOBUFS) || errors.Is(err, windows.WSAEWOULDBLOCK)
}

// isConnRefused: WSAECONNREFUSED, or WSAECONNRESET, which is how Windows reports an
// ICMP port unreachable on a UDP socket.
func isConnRefused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED) || errors.Is(err, windows.WSAECONNRESET)
}
