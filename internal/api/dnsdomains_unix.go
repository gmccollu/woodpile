//go:build !windows

package api

// searchDomains is the host's DNS search domains.
func searchDomains() []string { return resolvConfDomains() }
