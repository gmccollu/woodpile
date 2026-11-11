package listen

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Parsers for the OS tables the holder lookup and the kernel drop counter read.
// They're kept free of build tags so every parser is tested on every OS.

// procNetUDPDrops finds the row for inode in a /proc/net/udp or udp6 table and
// returns its drops column (the last one).
func procNetUDPDrops(r io.Reader, inode uint64) (int64, bool) {
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	want := strconv.FormatUint(inode, 10)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		// sl local rem st tx:rx tr:when retrnsmt uid timeout inode ref pointer drops
		if len(f) < 13 || f[9] != want {
			continue
		}
		v, err := strconv.ParseInt(f[len(f)-1], 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

// procNetInodes returns the inodes of the sockets bound to port in a
// /proc/net/{tcp,tcp6,udp,udp6} table. With listenOnly, only TCP sockets in the
// LISTEN state (0A) count.
func procNetInodes(r io.Reader, port int, listenOnly bool) []uint64 {
	sc := bufio.NewScanner(r)
	sc.Scan()
	var out []uint64
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		i := strings.LastIndexByte(f[1], ':')
		if i < 0 {
			continue
		}
		p, err := strconv.ParseUint(f[1][i+1:], 16, 16)
		if err != nil || int(p) != port {
			continue
		}
		if listenOnly && f[3] != "0A" {
			continue
		}
		ino, err := strconv.ParseUint(f[9], 10, 64)
		if err != nil || ino == 0 {
			continue
		}
		out = append(out, ino)
	}
	return out
}

// parseSocketLink returns the inode of a /proc/<pid>/fd link target
// "socket:[12345]".
func parseSocketLink(target string) (uint64, bool) {
	s, ok := strings.CutPrefix(target, "socket:[")
	if !ok {
		return 0, false
	}
	s, ok = strings.CutSuffix(s, "]")
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

// parseLsof reads `lsof -F pc` output ("p312\ncsyslogd\n...") and returns the
// first process as "syslogd (pid 312)".
func parseLsof(out []byte) string {
	var pid, cmd string
	for _, ln := range bytes.Split(out, []byte("\n")) {
		if len(ln) < 2 {
			continue
		}
		switch ln[0] {
		case 'p':
			if pid != "" {
				return holderText(cmd, pid)
			}
			pid = string(ln[1:])
		case 'c':
			if pid != "" && cmd == "" {
				cmd = string(ln[1:])
			}
		}
	}
	if pid == "" {
		return ""
	}
	return holderText(cmd, pid)
}

func holderText(name, pid string) string {
	if name == "" {
		return "pid " + pid
	}
	return fmt.Sprintf("%s (pid %s)", name, pid)
}

// Windows IP Helper tables (GetExtendedTcpTable / GetExtendedUdpTable with the
// OWNER_PID classes): a DWORD row count, then fixed-size rows. Ports are DWORDs
// holding the port in network byte order in their first two bytes.
const (
	winTCP4Row = 24 // MIB_TCPROW_OWNER_PID: state, laddr, lport, raddr, rport, pid
	winTCP6Row = 56 // MIB_TCP6ROW_OWNER_PID: laddr[16], scope, lport, raddr[16], scope, rport, state, pid
	winUDP4Row = 12 // MIB_UDPROW_OWNER_PID: laddr, lport, pid
	winUDP6Row = 28 // MIB_UDP6ROW_OWNER_PID: laddr[16], scope, lport, pid

	winTCPListen = 2 // MIB_TCP_STATE_LISTEN
)

// winTablePID returns the owning PID of the first row bound to port in a table
// buffer, given the row size and the offsets of the port, the PID and (TCP only,
// -1 otherwise) the state within a row.
func winTablePID(buf []byte, rowSize, portOff, pidOff, stateOff, port int) (uint32, bool) {
	if len(buf) < 4 {
		return 0, false
	}
	n := int(binary.LittleEndian.Uint32(buf))
	for i := 0; i < n; i++ {
		row := 4 + i*rowSize
		if row+rowSize > len(buf) {
			break
		}
		r := buf[row : row+rowSize]
		if int(r[portOff])<<8|int(r[portOff+1]) != port {
			continue
		}
		if stateOff >= 0 && binary.LittleEndian.Uint32(r[stateOff:]) != winTCPListen {
			continue
		}
		return binary.LittleEndian.Uint32(r[pidOff:]), true
	}
	return 0, false
}

// winRowLayout returns the row size and the port, PID and state offsets for a
// table (proto "tcp" or "udp", v6 for the IPv6 table).
func winRowLayout(proto string, v6 bool) (rowSize, portOff, pidOff, stateOff int) {
	switch {
	case proto == "tcp" && !v6:
		return winTCP4Row, 8, 20, 0
	case proto == "tcp" && v6:
		return winTCP6Row, 20, 52, 48
	case !v6:
		return winUDP4Row, 4, 8, -1
	default:
		return winUDP6Row, 20, 24, -1
	}
}
