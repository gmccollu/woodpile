package listen

import (
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// kernelDropsSupported: Linux shows each UDP socket's drop counter in /proc/net/udp.
const kernelDropsSupported = true

func socketInode(c *net.UDPConn) uint64 {
	rc, err := c.SyscallConn()
	if err != nil {
		return 0
	}
	var ino uint64
	rc.Control(func(fd uintptr) {
		var st unix.Stat_t
		if unix.Fstat(int(fd), &st) == nil {
			ino = st.Ino
		}
	})
	return ino
}

// readSocketDrops returns the drops column of our socket's row in /proc/net/udp or
// /proc/net/udp6 (a dual-stack socket is listed in udp6).
func readSocketDrops(inode uint64) (int64, bool) {
	if inode == 0 {
		return 0, false
	}
	for _, p := range []string{"/proc/net/udp6", "/proc/net/udp"} {
		f, err := os.Open(p)
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
