package listen

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isAddrInUse: WSAEACCES too, which is what a port held with SO_EXCLUSIVEADDRUSE
// (or reserved by another service) gives.
func isAddrInUse(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE) || errors.Is(err, windows.WSAEACCES)
}
