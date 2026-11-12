package config

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// MaxUIHostEntries caps ui.extra_hostnames and ui.trusted_hosts.
const MaxUIHostEntries = 64

// normalizeHost trims, lower-cases and drops a trailing dot (the form names
// are compared in).
func normalizeHost(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

func normalizeHosts(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, normalizeHost(s))
	}
	return out
}

// ValidDNSName reports whether s is a DNS host name: dot-separated labels of
// 1–63 letters, digits and hyphens (not starting or ending with a hyphen), at
// most 253 characters, not all digits (that would be an IP). No wildcards.
func ValidDNSName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	allDigits := true
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '-':
				allDigits = false
			case c >= '0' && c <= '9':
			default:
				return false
			}
		}
	}
	return !allDigits
}

// ParseExtraHostname parses a ui.extra_hostnames entry: a DNS name or an IP.
// Exactly one of name and ip is set.
func ParseExtraHostname(s string) (name string, ip netip.Addr, ok bool) {
	s = normalizeHost(s)
	if a, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil && a.Zone() == "" {
		return "", a.Unmap(), true
	}
	if ValidDNSName(s) {
		return s, netip.Addr{}, true
	}
	return "", netip.Addr{}, false
}

// TrustedHost is a parsed ui.trusted_hosts entry. Port 0 matches any port (and
// a Host header without one).
type TrustedHost struct {
	Host string // lower-case DNS name, or an IP in netip's form (no brackets)
	Port int
}

// ParseTrustedHost parses "host", "host:port", "IP", "[IPv6]" or "[IPv6]:port".
func ParseTrustedHost(s string) (TrustedHost, bool) {
	s = normalizeHost(s)
	host, port := s, 0
	bracketed := strings.HasPrefix(s, "[")
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return TrustedHost{}, false
		}
		host, port = h, n
	} else if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		host = s[1 : len(s)-1]
	} else if strings.Count(s, ":") == 1 {
		return TrustedHost{}, false // "host:" or a bad port
	}
	if a, err := netip.ParseAddr(host); err == nil && a.Zone() == "" {
		return TrustedHost{Host: a.Unmap().String(), Port: port}, true
	}
	if bracketed || strings.Contains(host, ":") || !ValidDNSName(host) {
		return TrustedHost{}, false
	}
	return TrustedHost{Host: host, Port: port}, true
}

// TrustedHostList parses ui.trusted_hosts, skipping bad entries (Validate reports
// them).
func (u UI) TrustedHostList() []TrustedHost {
	out := make([]TrustedHost, 0, len(u.TrustedHosts))
	for _, s := range u.TrustedHosts {
		if th, ok := ParseTrustedHost(s); ok {
			out = append(out, th)
		}
	}
	return out
}

func checkUIHosts(e Errors, u UI) {
	if len(u.ExtraHostnames) > MaxUIHostEntries {
		e["ui.extra_hostnames"] = fmt.Sprintf("at most %d names", MaxUIHostEntries)
	}
	for i, s := range u.ExtraHostnames {
		if _, _, ok := ParseExtraHostname(s); !ok {
			e[fmt.Sprintf("ui.extra_hostnames[%d]", i)] = "must be a DNS name (e.g. logs.example.net) or an IP address"
		}
	}
	if len(u.TrustedHosts) > MaxUIHostEntries {
		e["ui.trusted_hosts"] = fmt.Sprintf("at most %d hosts", MaxUIHostEntries)
	}
	for i, s := range u.TrustedHosts {
		if _, ok := ParseTrustedHost(s); !ok {
			e[fmt.Sprintf("ui.trusted_hosts[%d]", i)] = "must be a host name or IP, optionally with a port (e.g. logs.example.net or logs.example.net:443)"
		}
	}
}
