package api

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
)

const isWindows = runtime.GOOS == "windows"

// ContentSecurityPolicy is sent on every response. Scripts, styles, fonts and
// connections are same-origin only; nothing inline may run, and no page may
// frame the UI.
const ContentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// securityHeaders adds the security headers to every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

// loopbackHost reports whether a Host header names this machine's loopback
// interface on port: localhost, 127.0.0.1 or [::1], with the port. It is the
// DNS-rebinding guard for localhost mode: a page on another site that rebinds
// its own name to 127.0.0.1 still sends its own name as the Host.
func loopbackHost(host string, port int) bool {
	h, p, err := net.SplitHostPort(host)
	if err != nil || p != strconv.Itoa(port) {
		return false
	}
	switch strings.ToLower(h) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// trustedHost reports whether the Host header matches a ui.trusted_hosts entry:
// the same host (IPs compared as addresses) and, when the entry has one, the
// same port.
func (s *Server) trustedHost(hostHeader string) bool {
	p := s.trustedHosts.Load()
	if p == nil || len(*p) == 0 || hostHeader == "" {
		return false
	}
	h, port := hostHeader, 0
	if hh, ps, err := net.SplitHostPort(hostHeader); err == nil {
		n, err := strconv.Atoi(ps)
		if err != nil {
			return false
		}
		h, port = hh, n
	}
	h = strings.TrimSuffix(strings.ToLower(strings.Trim(h, "[]")), ".")
	if a, err := netip.ParseAddr(h); err == nil {
		if a.Zone() != "" {
			return false
		}
		h = a.Unmap().String()
	}
	for _, t := range *p {
		if t.Host == h && (t.Port == 0 || t.Port == port) {
			return true
		}
	}
	return false
}

// quoteHost shortens a Host header for an error message.
func quoteHost(h string) string {
	if len(h) > 100 {
		h = h[:100] + "…"
	}
	return h
}

// hostGuard rejects requests whose Host isn't loopback while the server is in
// localhost mode.
func (s *Server) hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.lanActive() && !loopbackHost(r.Host, s.boundPort()) && !s.trustedHost(r.Host) {
			writeError(w, http.StatusForbidden, fmt.Sprintf("Host %q isn't allowed: open the UI at http://localhost:%d/ instead. "+
				"If a reverse proxy uses this name, add it to ui.trusted_hosts in Settings or the config file.", quoteHost(r.Host), s.boundPort()))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// crossOriginGuard rejects cross-origin state-changing requests, using
// Sec-Fetch-Site when the browser sends it and comparing Origin with Host when
// it doesn't. Requests with neither header (curl, scripts on this machine) pass;
// browsers always send one of them on a cross-origin POST.
func crossOriginGuard(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusForbidden, "Cross-origin request refused.")
	}))
	return cop.Handler(next)
}
