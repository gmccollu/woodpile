//go:build !windows

package loadgen

import (
	"errors"
	"syscall"
)

// isNoBufs: the OS send queue is full (macOS returns ENOBUFS for UDP rather than
// blocking).
func isNoBufs(err error) bool {
	return errors.Is(err, syscall.ENOBUFS) || errors.Is(err, syscall.EAGAIN)
}

func isConnRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
