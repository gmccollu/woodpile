//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// echoOff turns off echo on the console input f and returns the function that
// turns it back on.
func echoOff(f *os.File) (restore func(), err error) {
	h := windows.Handle(f.Fd())
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return nil, err
	}
	mode := old&^windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT | windows.ENABLE_PROCESSED_INPUT
	if err := windows.SetConsoleMode(h, mode); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(h, old) }, nil
}
