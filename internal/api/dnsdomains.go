package api

import (
	"bufio"
	"io"
	"os"
	"strings"

	"github.com/gmccollu/woodshed/internal/config"
)

// maxSearchDomains caps the DNS search domains added to the certificate
// (resolv.conf allows 6).
const maxSearchDomains = 6

// parseResolvConf returns the domains of resolv.conf's "domain" and "search"
// lines, in order, valid DNS names only, without "local" (the mDNS name is
// already covered).
func parseResolvConf(r io.Reader) []string {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 || (f[0] != "search" && f[0] != "domain") {
			continue
		}
		for _, d := range f[1:] {
			if strings.HasPrefix(d, "#") || strings.HasPrefix(d, ";") {
				break
			}
			d = strings.TrimSuffix(strings.ToLower(d), ".")
			if d == "local" || !config.ValidDNSName(d) {
				continue
			}
			out = append(out, d)
		}
	}
	return out
}

// resolvConfDomains reads /etc/resolv.conf (Linux, macOS and other Unix).
func resolvConfDomains() []string {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseResolvConf(io.LimitReader(f, 1<<20))
}
