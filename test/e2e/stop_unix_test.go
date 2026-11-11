//go:build !windows

package e2e

import (
	"os/exec"
	"syscall"
)

func prepareCmd(*exec.Cmd) {}

func terminate(cmd *exec.Cmd) error { return cmd.Process.Signal(syscall.SIGTERM) }
