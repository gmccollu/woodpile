//go:build windows

package peer

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestSCMRunning(t *testing.T) {
	for st, want := range map[uint32]bool{windows.SERVICE_RUNNING: true, windows.SERVICE_START_PENDING: true,
		windows.SERVICE_STOPPED: false, windows.SERVICE_STOP_PENDING: false} {
		if scmRunning(st) != want {
			t.Errorf("state %d", st)
		}
	}
}
