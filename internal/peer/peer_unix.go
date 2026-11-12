//go:build !windows

package peer

import (
	"context"
	"errors"
	"runtime"
	"syscall"

	"github.com/gmccollu/woodshed/internal/service"
)

// serviceQuery asks launchd or systemd whether the Woodshed service runs.
func serviceQuery(ctx context.Context, o *Options) (bool, int) {
	switch runtime.GOOS {
	case "darwin":
		out, err := o.run(ctx, "launchctl", "print", "system/"+service.DarwinName)
		if err != nil {
			return false, 0
		}
		return parseLaunchctl(out)
	case "linux":
		out, err := o.run(ctx, "systemctl", "show", service.LinuxName+".service", "-p", "MainPID,ActiveState")
		if err != nil {
			return false, 0
		}
		return parseSystemctl(out)
	}
	return false, 0
}

// processAlive: signal 0 succeeds for our own processes and fails with EPERM
// for another user's, which is alive too.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
