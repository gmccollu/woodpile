//go:build windows

package peer

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/gmccollu/woodpile/internal/service"
)

// serviceQuery asks the SCM about the Woodpile service with the rights any
// user has (connect, query status).
func serviceQuery(ctx context.Context, o *Options) (bool, int) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, 0
	}
	defer windows.CloseServiceHandle(m)
	name, err := windows.UTF16PtrFromString(service.WindowsName)
	if err != nil {
		return false, 0
	}
	s, err := windows.OpenService(m, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false, 0
	}
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS_PROCESS
	var need uint32
	if err := windows.QueryServiceStatusEx(s, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&st)),
		uint32(unsafe.Sizeof(st)), &need); err != nil {
		return false, 0
	}
	return scmRunning(st.CurrentState), int(st.ProcessId)
}

func scmRunning(state uint32) bool {
	return state == windows.SERVICE_RUNNING || state == windows.SERVICE_START_PENDING
}

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err == windows.ERROR_ACCESS_DENIED // exists, just not ours
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return true
	}
	return code == 259 // STILL_ACTIVE
}
