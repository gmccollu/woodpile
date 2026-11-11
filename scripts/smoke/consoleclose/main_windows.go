//go:build windows

// Command consoleclose closes another process's console window, the way a user
// clicking its X does, so Windows sends that process CTRL_CLOSE_EVENT. Used by the
// portable-mode smoke test.
//
//	consoleclose <pid>
package main

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procFreeConsole  = kernel32.NewProc("FreeConsole")
	procAttach       = kernel32.NewProc("AttachConsole")
	procConsoleWnd   = kernel32.NewProc("GetConsoleWindow")
	procPostMessageW = windows.NewLazySystemDLL("user32.dll").NewProc("PostMessageW")
)

const wmClose = 0x0010

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: consoleclose <pid>")
		os.Exit(2)
	}
	pid, err := strconv.Atoi(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "consoleclose:", err)
		os.Exit(2)
	}
	procFreeConsole.Call()
	if r, _, err := procAttach.Call(uintptr(pid)); r == 0 {
		fmt.Fprintf(os.Stderr, "consoleclose: AttachConsole(%d): %v\n", pid, err)
		os.Exit(1)
	}
	hwnd, _, _ := procConsoleWnd.Call()
	if hwnd == 0 {
		fmt.Fprintln(os.Stderr, "consoleclose: the console has no window")
		os.Exit(1)
	}
	// Detach first so the close event doesn't take this process down too.
	procFreeConsole.Call()
	if r, _, err := procPostMessageW.Call(hwnd, wmClose, 0, 0); r == 0 {
		fmt.Fprintln(os.Stderr, "consoleclose: PostMessage:", err)
		os.Exit(1)
	}
}
