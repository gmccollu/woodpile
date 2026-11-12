//go:build windows

package api

import (
	"strings"

	"golang.org/x/sys/windows"

	"github.com/gmccollu/woodshed/internal/config"
)

// searchDomains is the machine's primary DNS suffix.
func searchDomains() []string {
	n := uint32(256)
	buf := make([]uint16, n)
	if err := windows.GetComputerNameEx(windows.ComputerNameDnsDomain, &buf[0], &n); err != nil {
		return nil
	}
	d := strings.TrimSuffix(strings.ToLower(windows.UTF16ToString(buf[:n])), ".")
	if d == "" || d == "local" || !config.ValidDNSName(d) {
		return nil
	}
	return []string{d}
}
