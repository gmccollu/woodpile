//go:build windows

package e2e

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// The child gets its own process group so a Ctrl-Break reaches only it; Go
// delivers Ctrl-Break as os.Interrupt, which `woodpile run` handles like SIGTERM.
func prepareCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

func terminate(cmd *exec.Cmd) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))
}
