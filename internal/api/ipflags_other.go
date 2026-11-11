//go:build !linux

package api

import "net/netip"

// ipv6Flags is nil where the OS's address flags aren't read; identityFrom then
// keeps only EUI-64 IPv6 addresses.
func ipv6Flags() map[netip.Addr]uint32 { return nil }
