package listen

import (
	"path/filepath"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = iphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	tcpTableOwnerPIDListener = 3 // TCP_TABLE_OWNER_PID_LISTENER
	udpTableOwnerPID         = 1 // UDP_TABLE_OWNER_PID
	afInet                   = 2
	afInet6                  = 23
)

func portHolder(proto string, port int) string {
	for _, v6 := range []bool{false, true} {
		buf := extendedTable(proto, v6)
		if buf == nil {
			continue
		}
		rowSize, portOff, pidOff, stateOff := winRowLayout(proto, v6)
		if pid, ok := winTablePID(buf, rowSize, portOff, pidOff, stateOff, port); ok {
			return holderText(processName(pid), strconv.FormatUint(uint64(pid), 10))
		}
	}
	return ""
}

func extendedTable(proto string, v6 bool) []byte {
	proc, class := procGetExtendedUdpTable, uintptr(udpTableOwnerPID)
	if proto == "tcp" {
		proc, class = procGetExtendedTcpTable, tcpTableOwnerPIDListener
	}
	if proc.Find() != nil {
		return nil
	}
	af := uintptr(afInet)
	if v6 {
		af = afInet6
	}
	size := uint32(16 << 10)
	for range 4 {
		buf := make([]byte, size)
		r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, af, class, 0)
		switch windows.Errno(r) {
		case 0:
			return buf[:min(int(size), len(buf))]
		case windows.ERROR_INSUFFICIENT_BUFFER:
			size += 4 << 10 // the table can grow between calls
			continue
		default:
			return nil
		}
	}
	return nil
}

func processName(pid uint32) string {
	switch pid {
	case 0:
		return "System Idle Process"
	case 4:
		return "System" // http.sys and other kernel-mode listeners
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}
