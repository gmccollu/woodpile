package api

import (
	"bufio"
	"net/netip"
	"os"
)

// ipv6Flags reads each IPv6 address's IFA_F_* flags from /proc/self/net/if_inet6 (readable under systemd ProcSubset=pid)
// ("<addr hex> <ifindex> <prefixlen> <scope> <flags> <name>"). nil if it can't.
func ipv6Flags() map[netip.Addr]uint32 {
	f, err := os.Open("/proc/self/net/if_inet6")
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseIfInet6(bufio.NewScanner(f))
}
