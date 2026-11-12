//go:build !windows

package peer

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestServiceQueryUsesTheServiceNames(t *testing.T) {
	var got string
	o := &Options{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = name + " " + strings.Join(args, " ")
		if name == "launchctl" {
			return []byte(launchctlRunning), nil
		}
		return []byte("MainPID=812\nActiveState=active\n"), nil
	}}
	running, pid := serviceQuery(context.Background(), o)
	switch runtime.GOOS {
	case "darwin":
		if got != "launchctl print system/com.woodshed.daemon" || !running || pid != 4210 {
			t.Errorf("%q -> %v %d", got, running, pid)
		}
	case "linux":
		if got != "systemctl show woodshed.service -p MainPID,ActiveState" || !running || pid != 812 {
			t.Errorf("%q -> %v %d", got, running, pid)
		}
	}
	if !processAlive(os.Getpid()) || processAlive(1<<30) {
		t.Error("processAlive")
	}
}
