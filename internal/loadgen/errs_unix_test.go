//go:build !windows

package loadgen

import "syscall"

var errNoBufs, errConnRefused error = syscall.ENOBUFS, syscall.ECONNREFUSED
