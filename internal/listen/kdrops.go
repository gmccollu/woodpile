package listen

import "time"

// kernelDrops tracks the kernel's drop counter for our UDP socket (Linux only; see
// kernelDropsSupported). The counter belongs to the socket, so a rebind starts a
// new one; base keeps what earlier sockets counted. The Server's lock guards it.
type kernelDrops struct {
	base  int64  // drops counted by sockets we've closed
	last  int64  // last reading of the current socket
	inode uint64 // the current socket's inode, 0 if none
	h     hours
}

func (k *kernelDrops) attach(u *udpListener) {
	k.inode, k.last = socketInode(u.conn), 0
	if v, ok := readSocketDrops(k.inode); ok {
		k.last = v // a fresh socket starts at 0, but don't count anything twice
	}
}

func (k *kernelDrops) detach(u *udpListener) {
	k.sample(u, time.Now())
	k.base += k.last
	k.last, k.inode = 0, 0
}

func (k *kernelDrops) sample(u *udpListener, now time.Time) {
	if u == nil || k.inode == 0 {
		return
	}
	v, ok := readSocketDrops(k.inode)
	if !ok || v < k.last {
		return
	}
	if d := v - k.last; d > 0 {
		k.h.add(now, d)
	}
	k.last = v
}

func (k *kernelDrops) total(u *udpListener) int64 {
	k.sample(u, time.Now())
	return k.base + k.last
}

func (k *kernelDrops) sum24h(now time.Time) int64 { return k.h.sum(now) }
