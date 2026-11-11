//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package main

// stdinIsTerminal is only used on Unix; Windows looks at the console instead.
func stdinIsTerminal() bool { return true }
