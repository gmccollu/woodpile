//go:build !windows

package listen

import (
	"errors"
	"syscall"
)

func isAddrInUse(err error) bool { return errors.Is(err, syscall.EADDRINUSE) }

// commonErrText returns the OS-independent text for the common bind failures, or
// "" for anything else.
func commonErrText(err error) string {
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		return "address already in use"
	case errors.Is(err, syscall.EACCES):
		return "permission denied"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "address not available"
	}
	return ""
}
