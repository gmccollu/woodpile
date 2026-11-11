//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// consoleProcessCount is GetConsoleProcessList's count (0 on error).
func consoleProcessCount() int {
	var pids [4]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return int(n)
}

// windowsFirewallNotes explains the first-run firewall dialog and, when no
// inbound rule allows this program, gives the command an administrator can run.
func windowsFirewallNotes() string {
	var s strings.Builder
	s.WriteString("\n  Windows may ask whether to allow Woodpile through the firewall. Choose Allow so devices can\n")
	s.WriteString("  reach it (it may need an administrator).\n")
	exe, err := os.Executable()
	if err != nil {
		return s.String()
	}
	out, err := exec.Command("netsh", "advfirewall", "firewall", "show", "rule", "name=all", "dir=in", "verbose").Output()
	if err == nil && strings.Contains(strings.ToLower(string(out)), strings.ToLower(exe)) {
		return s.String()
	}
	admin := windows.GetCurrentProcessToken().IsElevated()
	if admin {
		s.WriteString("  No firewall rule lets devices reach this copy yet. To add one now:\n")
	} else {
		s.WriteString("  No firewall rule lets devices reach this copy yet. If they can't, run this in an\n  administrator prompt:\n")
	}
	fmt.Fprintf(&s, "      netsh advfirewall firewall add rule name=\"Woodpile (portable)\" dir=in action=allow program=\"%s\" enable=yes\n", exe)
	return s.String()
}
