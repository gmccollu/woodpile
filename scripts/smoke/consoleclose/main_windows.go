//go:build windows

// Command consoleclose closes another process's console window, the way a user
// clicking its X does, so Windows sends that process CTRL_CLOSE_EVENT. Used by the
// portable-mode smoke test.
//
//	consoleclose <pid>
//
// Exit status 3 means the console is a pseudo console (ConPTY), whose window
// ignores close requests, so a close can't be simulated in this session.
package main

import (
	"fmt"
	"os"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procFreeConsole  = kernel32.NewProc("FreeConsole")
	procAttach       = kernel32.NewProc("AttachConsole")
	procConsoleWnd   = kernel32.NewProc("GetConsoleWindow")
	user32           = windows.NewLazySystemDLL("user32.dll")
	procPostMessageW = user32.NewProc("PostMessageW")
	procClassName    = user32.NewProc("GetClassNameW")
)

const (
	wmClose  = 0x0010
	wmSysCmd = 0x0112
	scClose  = 0xF060
)

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
	var cls [256]uint16
	procClassName.Call(hwnd, uintptr(unsafe.Pointer(&cls[0])), uintptr(len(cls)))
	class := windows.UTF16ToString(cls[:])
	// Detach first so the close event doesn't take this process down too.
	procFreeConsole.Call()
	// A pseudo console (ConPTY) window is a stand-in that ignores close requests:
	// say so, so the caller can tell "couldn't simulate" from "didn't stop".
	fmt.Printf("window class %s\n", class)
	if class == "PseudoConsoleWindow" {
		os.Exit(3)
	}
	// The X button sends WM_SYSCOMMAND/SC_CLOSE; WM_CLOSE as well, for good measure.
	r1, _, err1 := procPostMessageW.Call(hwnd, wmSysCmd, scClose, 0)
	r2, _, err2 := procPostMessageW.Call(hwnd, wmClose, 0, 0)
	if r1 == 0 && r2 == 0 {
		fmt.Fprintln(os.Stderr, "consoleclose: PostMessage:", err1, err2)
		os.Exit(1)
	}
}
