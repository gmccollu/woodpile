//go:build !linux

package listen

import "net"

// kernelDropsSupported: macOS and Windows don't expose a per-socket UDP drop
// counter, so the UI says drops are "not visible" instead of showing 0.
const kernelDropsSupported = false

func socketInode(*net.UDPConn) uint64 { return 0 }

func readSocketDrops(uint64) (int64, bool) { return 0, false }
