//go:build windows

package store

import "golang.org/x/sys/windows"

var procSetThreadPriority = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadPriority")

// lowerThreadPriority puts the calling OS thread in background mode (low CPU and I/O
// priority). The caller must have locked the goroutine to its thread.
func lowerThreadPriority() error {
	const threadModeBackgroundBegin = 0x00010000
	h, err := windows.GetCurrentThread()
	if err != nil {
		return err
	}
	if r, _, e := procSetThreadPriority.Call(uintptr(h), threadModeBackgroundBegin); r == 0 {
		return e
	}
	return nil
}
