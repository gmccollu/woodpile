package listen

import "time"

// holderTimeout bounds a holder lookup: it runs while a bind failure is reported.
const holderTimeout = 2 * time.Second

// PortHolder names the process holding a UDP or TCP port, such as
// "syslogd (pid 312)", best effort: "" when the OS doesn't say (for example a
// process of another user when we aren't privileged). It never fails.
//
//   - macOS: lsof.
//   - Linux: /proc/net/{tcp,udp}{,6} for the socket inode, then /proc/*/fd.
//   - Windows: GetExtendedTcpTable / GetExtendedUdpTable for the PID, then the
//     process image name.
func PortHolder(proto string, port int) string {
	if port <= 0 || (proto != "udp" && proto != "tcp") {
		return ""
	}
	return portHolder(proto, port)
}
