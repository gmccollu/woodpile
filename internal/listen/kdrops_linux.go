package listen

import (
	"net"

	"golang.org/x/sys/unix"
)

// kernelDropsSupported: Linux shows each UDP socket's drop counter in
// /proc/self/net/udp{,6}.
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

func readSocketDrops(inode uint64) (int64, bool) { return procSocketDrops(procRoot, inode) }
