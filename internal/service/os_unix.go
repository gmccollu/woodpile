//go:build !windows

package service

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

func requireAdmin() error {
	if os.Geteuid() != 0 {
		return errors.New("installing or removing the service needs root: run it with sudo")
	}
	return nil
}

func installOS(o InstallOptions) (Result, error) {
	dataDir := o.DataDir
	h := realHost(o.Out)
	switch runtime.GOOS {
	case "linux":
		return installSystemd(h, o, dataDir)
	case "darwin":
		return installLaunchd(h, o, dataDir)
	}
	return Result{}, fmt.Errorf("installing a service isn't supported on %s; run `woodshed run`", runtime.GOOS)
}

func uninstallOS(o UninstallOptions) (Result, error) {
	h := realHost(o.Out)
	switch runtime.GOOS {
	case "linux":
		return uninstallSystemd(h, o)
	case "darwin":
		return uninstallLaunchd(h, o)
	}
	return Result{}, fmt.Errorf("services aren't supported on %s", runtime.GOOS)
}

func statusOS(r runner) (State, error) {
	h := &host{runner: r}
	switch runtime.GOOS {
	case "linux":
		return statusSystemd(h)
	case "darwin":
		return statusLaunchd(h)
	}
	return StateUnknown, fmt.Errorf("services aren't supported on %s", runtime.GOOS)
}

func restartOS(r runner) error {
	h := &host{runner: r}
	switch runtime.GOOS {
	case "linux":
		return restartSystemd(h)
	case "darwin":
		return restartLaunchd(h)
	}
	return fmt.Errorf("services aren't supported on %s", runtime.GOOS)
}

func windowsServiceState(string, string) (bool, bool, error) { return false, false, nil }
