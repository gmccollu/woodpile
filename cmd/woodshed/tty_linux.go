package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// The termios get/set requests (secret_unix.go).
const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
)

// stdinIsTerminal reports whether stdin is a terminal (/dev/null, a pipe or a
// file manager's launch is not).
func stdinIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return err == nil
}
