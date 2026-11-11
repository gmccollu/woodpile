package listen

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Parsers for the OS tables the holder lookup and the kernel drop counter read.
// They're kept free of build tags so every parser is tested on every OS.

// procRoot is where Linux's proc file system is mounted.
const procRoot = "/proc"

// procNetFile is a socket table of our own network namespace. It's read through
// /proc/self/net, not /proc/net: systemd's ProcSubset=pid (in our unit) makes
// /proc/net unreadable while /proc/self/net stays readable.
func procNetFile(root, name string) string { return filepath.Join(root, "self", "net", name) }

// procSocketDrops returns the drops counter of the UDP socket with inode, from
// <root>/self/net/udp6 or udp (a dual-stack socket is listed in udp6).
func procSocketDrops(root string, inode uint64) (int64, bool) {
	if inode == 0 {
		return 0, false
	}
	for _, name := range []string{"udp6", "udp"} {
		f, err := os.Open(procNetFile(root, name))
		if err != nil {
			continue
		}
		v, ok := procNetUDPDrops(f, inode)
		f.Close()
		if ok {
			return v, true
		}
	}
	return 0, false
}

// procPortHolder finds the process holding proto port: the socket inodes bound to
// the port from <root>/self/net/<proto>{,6}, then a scan of <root>/<pid>/fd for a
// link to one of them. Best effort, and it never fails: any table or directory
// it can't read is skipped, and the answer is then "". Under systemd's
// ProtectProc=invisible, /proc lists only our own processes, so a holder that is
// another service isn't found (the banner then just omits it). It gives up at
// deadline.
func procPortHolder(root, proto string, port int, deadline time.Time) string {
	inodes := map[uint64]bool{}
	for _, suffix := range []string{"", "6"} {
		f, err := os.Open(procNetFile(root, proto+suffix))
		if err != nil {
			continue
		}
		for _, ino := range procNetInodes(f, port, proto == "tcp") {
			inodes[ino] = true
		}
		f.Close()
	}
	if len(inodes) == 0 {
		return ""
	}
	procs, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, p := range procs {
		pid := p.Name()
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		if time.Now().After(deadline) {
			return ""
		}
		fds, err := os.ReadDir(filepath.Join(root, pid, "fd"))
		if err != nil {
			continue // another user's process, unless we're privileged
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(root, pid, "fd", fd.Name()))
			if err != nil {
				continue
			}
			if ino, ok := parseSocketLink(target); ok && inodes[ino] {
				comm, _ := os.ReadFile(filepath.Join(root, pid, "comm"))
				return holderText(strings.TrimSpace(string(comm)), pid)
			}
		}
	}
	return ""
}

// procNetUDPDrops finds the row for inode in a /proc/self/net/udp or udp6 table and
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
// /proc/self/net/{tcp,tcp6,udp,udp6} table. With listenOnly, only TCP sockets in the
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
