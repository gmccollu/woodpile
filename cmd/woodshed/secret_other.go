//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package main

import (
	"errors"
	"os"
)

func echoOff(*os.File) (func(), error) {
	return nil, errors.New("can't turn off echo on this system")
}
