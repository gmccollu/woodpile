package config

import (
	"fmt"
	"net/netip"
	"strings"
)

// Blocklist helpers. An entry is an IP (meaning /32 or /128) or a CIDR, parsed
// like the allowlist.

func trimList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

// checkBlocklist reports bad entries as listeners.blocklist[i], i indexing the
// list as sent.
func checkBlocklist(e Errors, l Listeners) {
	for i, s := range l.Blocklist {
		if _, err := parseAllow(s); err != nil {
			e[fmt.Sprintf("listeners.blocklist[%d]", i)] = "must be an IP address or a CIDR (e.g. 198.51.100.0/24)"
		}
	}
}

// BlockPrefixes parses the blocklist (see Prefixes). Validate first; it fails on
// the first bad entry.
func (l Listeners) BlockPrefixes() ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(l.Blocklist))
	for _, s := range l.Blocklist {
		p, err := parseAllow(s)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// BlockedBy reports whether ip is on the blocklist, and the entry that blocks it,
// as written in the list: the most specific matching entry (the longest prefix;
// the first such entry on a tie). Entries that don't parse are skipped.
func (l Listeners) BlockedBy(ip netip.Addr) (entry string, blocked bool) {
	ip = ip.Unmap().WithZone("")
	best := -1
	for _, s := range l.Blocklist {
		p, err := parseAllow(s)
		if err != nil || !p.Contains(ip) {
			continue
		}
		if p.Bits() > best {
			best, entry = p.Bits(), strings.TrimSpace(s)
		}
	}
	return entry, best >= 0
}

// Block adds ip's exact address (a bare IP, meaning /32 or /128) to the blocklist,
// unless an entry for exactly that address is already there. It reports whether
// it added one.
func (l *Listeners) Block(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if l.hasExact(ip) {
		return false
	}
	l.Blocklist = append(l.Blocklist, ip.String())
	return true
}

// Unblock removes every entry for exactly ip's address ("198.51.100.7" or
// "198.51.100.7/32"), and returns how many it removed. Broader CIDRs that also
// cover ip stay (see BlockedBy).
func (l *Listeners) Unblock(ip netip.Addr) int {
	ip = ip.Unmap().WithZone("")
	exact := netip.PrefixFrom(ip, ip.BitLen())
	out := l.Blocklist[:0:0]
	removed := 0
	for _, s := range l.Blocklist {
		if p, err := parseAllow(s); err == nil && p == exact {
			removed++
			continue
		}
		out = append(out, s)
	}
	l.Blocklist = out
	return removed
}

func (l Listeners) hasExact(ip netip.Addr) bool {
	exact := netip.PrefixFrom(ip, ip.BitLen())
	for _, s := range l.Blocklist {
		if p, err := parseAllow(s); err == nil && p == exact {
			return true
		}
	}
	return false
}
