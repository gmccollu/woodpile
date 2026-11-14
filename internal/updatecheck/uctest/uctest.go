// Package uctest is test support for the update check: a fake clock, a local
// TLS server standing in for the releases host (with a certificate for
// updatecheck.Host from a throwaway CA), a CONNECT proxy, and the dial hook that
// points the checker at them. Only tests import it (a module-wide test checks
// that), and nothing in it ever reaches the real host.
package uctest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gmccollu/woodshed/internal/updatecheck"
)

// ---- fake clock ----

// FakeClock is an updatecheck.Clock that moves only when told to.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeTimer
	changed chan struct{} // closed and replaced when timers change
}

type fakeTimer struct {
	c        *FakeClock
	deadline time.Time
	ch       chan time.Time
	done     bool
}

// NewFakeClock starts a clock at t.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{now: t, changed: make(chan struct{})}
}

// Now is the clock's time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer makes a timer that fires when the clock reaches now+d.
func (c *FakeClock) NewTimer(d time.Duration) updatecheck.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, deadline: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.done = true
		t.ch <- c.now
	} else {
		c.timers = append(c.timers, t)
	}
	c.signal()
	return t
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	if t.done {
		return false
	}
	t.done = true
	t.c.timers = slices.DeleteFunc(t.c.timers, func(x *fakeTimer) bool { return x == t })
	t.c.signal()
	return true
}

func (c *FakeClock) signal() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// Advance moves the clock by d, firing every timer that comes due.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.timers[:0]
	for _, t := range c.timers {
		if !t.deadline.After(c.now) {
			t.done = true
			t.ch <- c.now
		} else {
			keep = append(keep, t)
		}
	}
	c.timers = keep
	c.signal()
}

// Pending reports the earliest pending timer's deadline.
func (c *FakeClock) Pending() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.timers) == 0 {
		return time.Time{}, false
	}
	d := c.timers[0].deadline
	for _, t := range c.timers[1:] {
		if t.deadline.Before(d) {
			d = t.deadline
		}
	}
	return d, true
}

// WaitTimer waits (in real time, up to timeout) until a timer is pending, and
// returns its deadline.
func (c *FakeClock) WaitTimer(timeout time.Duration) (time.Time, bool) {
	deadline := time.After(timeout)
	for {
		c.mu.Lock()
		ch := c.changed
		c.mu.Unlock()
		if d, ok := c.Pending(); ok {
			return d, true
		}
		select {
		case <-ch:
		case <-deadline:
			return time.Time{}, false
		}
	}
}

// ---- the releases host ----

// Request is one request the fake releases host received.
type Request struct {
	At     time.Time // the fake clock's time
	Method string
	Host   string
	Path   string
	Query  string
	Header http.Header
}

// Server is a TLS server with a certificate for updatecheck.Host. The checker
// reaches it through Hooks' dialer; its own URL never changes.
type Server struct {
	*httptest.Server
	Roots *x509.CertPool
	clock *FakeClock

	mu       sync.Mutex
	reqs     []Request
	dials    []string
	handler  http.HandlerFunc
	extra    map[string]bool // addresses the dialer may reach as they are (a proxy)
	requests chan Request
}

// NewServer starts the fake releases host. Requests are timed on clock (which
// may be nil: then real time).
func NewServer(t testing.TB, clock *FakeClock) *Server {
	t.Helper()
	ca, caKey := newCA(t)
	cert := leaf(t, ca, caKey, updatecheck.Host)
	s := &Server{Roots: x509.NewCertPool(), clock: clock, extra: map[string]bool{}, requests: make(chan Request, 100)}
	s.Roots.AddCert(ca)
	s.handler = s.Releases("v1.2.0", "")
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		at := time.Now()
		if s.clock != nil {
			at = s.clock.Now()
		}
		req := Request{At: at, Method: r.Method, Host: r.Host, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone()}
		s.mu.Lock()
		s.reqs = append(s.reqs, req)
		h := s.handler
		s.mu.Unlock()
		select {
		case s.requests <- req:
		default:
		}
		h(w, r)
	}))
	s.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

// Releases answers with a releases.json listing stable and prerelease tags ("" for null).
func (s *Server) Releases(stable, pre string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, ReleasesJSON(stable, pre))
	}
}

// ReleasesJSON is a releases.json in site/tools/releases.mjs's shape.
func ReleasesJSON(stable, pre string) string {
	entry := func(tag string) string {
		if tag == "" {
			return "null"
		}
		return fmt.Sprintf(`{"tag_name":%q,"name":%q,"html_url":"https://github.com/gmccollu/woodshed/releases/tag/%s",`+
			`"published_at":"2026-11-12T10:00:00Z","prerelease":%t,"assets":[{"name":"woodshed.msi","size":1,"browser_download_url":"https://example.invalid/x"}]}`,
			tag, tag, tag, pre != "" && tag == pre)
	}
	return fmt.Sprintf(`{"generated_at":"2026-11-13T00:00:00Z","stable":%s,"prerelease":%s}`, entry(stable), entry(pre))
}

// Handle replaces the response.
func (s *Server) Handle(h http.HandlerFunc) {
	s.mu.Lock()
	s.handler = h
	s.mu.Unlock()
}

// Requests returns what the server has received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reqs)
}

// Dials returns every address the checker's dialer was asked for.
func (s *Server) Dials() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.dials)
}

// WaitRequest waits (in real time) for the next request.
func (s *Server) WaitRequest(timeout time.Duration) (Request, bool) {
	select {
	case r := <-s.requests:
		return r, true
	case <-time.After(timeout):
		return Request{}, false
	}
}

// Hooks points a checker at this server: Host:443 dials it, addresses added by
// a Proxy dial as they are, anything else fails.
func (s *Server) Hooks(clock *FakeClock) *updatecheck.TestHooks {
	h := &updatecheck.TestHooks{RootCAs: s.Roots, DialContext: s.dial}
	if clock != nil {
		h.Clock = clock
	}
	return h
}

func (s *Server) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	s.mu.Lock()
	s.dials = append(s.dials, addr)
	extra := s.extra[addr]
	s.mu.Unlock()
	var d net.Dialer
	switch {
	case addr == net.JoinHostPort(updatecheck.Host, "443"):
		return d.DialContext(ctx, network, s.Listener.Addr().String())
	case extra:
		return d.DialContext(ctx, network, addr)
	}
	return nil, fmt.Errorf("uctest: dial to %s refused", addr)
}

// ---- a CONNECT proxy ----

// Proxy is an HTTP proxy that tunnels CONNECT updatecheck.Host:443 to a Server.
type Proxy struct {
	*httptest.Server
	mu      sync.Mutex
	connect []string // CONNECT targets seen
	other   int      // anything else
}

// NewProxy starts a proxy in front of s and lets s's dialer reach it.
func NewProxy(t testing.TB, s *Server) *Proxy {
	t.Helper()
	p := &Proxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			p.mu.Lock()
			p.other++
			p.mu.Unlock()
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		p.mu.Lock()
		p.connect = append(p.connect, r.Host)
		p.mu.Unlock()
		if r.Host != net.JoinHostPort(updatecheck.Host, "443") {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		up, err := net.Dial("tcp", s.Listener.Addr().String())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			up.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
		conn, buf, err := hj.Hijack()
		if err != nil {
			up.Close()
			return
		}
		go func() {
			defer up.Close()
			defer conn.Close()
			go func() { _, _ = io.Copy(up, buf) }()
			_, _ = io.Copy(conn, up)
		}()
	}))
	t.Cleanup(p.Close)
	s.mu.Lock()
	s.extra[p.Listener.Addr().String()] = true
	s.mu.Unlock()
	return p
}

// Connects returns the CONNECT targets the proxy saw, and the number of other requests.
func (p *Proxy) Connects() ([]string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.connect), p.other
}

// ---- certificates ----

func newCA(t testing.TB) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "uctest CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return ca, key
}

func leaf(t testing.TB, ca *x509.Certificate, caKey *ecdsa.PrivateKey, host string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
