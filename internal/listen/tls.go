package listen

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gmccollu/woodshed/internal/config"
)

// The TLS syslog listener (SPEC §10.3, RFC 5425). After the handshake a
// connection is exactly a TCP one: the same framing, limits and pool.
//
// Handshakes don't take connection slots (§10.3.5): a connection joins the pool
// only once its handshake completes. Until then it counts against its own limits,
// MaxHandshakes at once and MaxHandshakesPerAddr per sender address; past them it
// is closed at once (reason busy). Each handshake has HandshakeTimeout.

// Handshake limits (defaults for Options).
const (
	DefaultMaxHandshakes        = 256
	DefaultMaxHandshakesPerAddr = 8
	DefaultHandshakeTimeout     = 10 * time.Second
)

// TLS handshake failure reasons (SPEC §10.3.4).
const (
	TLSCertRejected = "cert_rejected" // the sender sent an alert about the certificate
	TLSVersion      = "version"       // no TLS version in common
	TLSCipher       = "cipher"        // no cipher suite (or curve) in common
	TLSNotTLS       = "not_tls"       // the first bytes weren't a TLS handshake
	TLSTimeout      = "timeout"       // no complete handshake within HandshakeTimeout
	TLSClosed       = "closed"        // the sender closed during the handshake, without an alert
	TLSBusy         = "busy"          // over the handshake limits
	TLSOther        = "other"
)

// tlsSuites12 are the TLS 1.2 suites (§10.3.2): ECDHE with AES-GCM or
// ChaCha20-Poly1305, RSA and ECDSA. TLS 1.3 uses Go's fixed set.
var tlsSuites12 = []uint16{
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
}

// tlsSuitesLegacy are added with allow_legacy_tls: TLS 1.0 and 1.1 can't use
// AEAD suites.
var tlsSuitesLegacy = []uint16{
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
}

// tlsCurves: X25519, P-256 and P-384, plus Go's default hybrid post-quantum group
// for TLS 1.3.
var tlsCurves = []tls.CurveID{tls.X25519MLKEM768, tls.X25519, tls.CurveP256, tls.CurveP384}

// ServerTLSConfig is the listener's TLS configuration (SPEC §10.3.2). cert
// supplies the certificate on each handshake, so a renewal applies to new
// connections at once. No client certificate is requested, offered ALPN is
// ignored, SNI doesn't matter, and there are no session tickets.
func ServerTLSConfig(legacy bool, cert func() (*tls.Certificate, error)) *tls.Config {
	c := &tls.Config{
		MinVersion:             tls.VersionTLS12,
		MaxVersion:             tls.VersionTLS13,
		CipherSuites:           append([]uint16(nil), tlsSuites12...),
		CurvePreferences:       tlsCurves,
		SessionTicketsDisabled: true,
		ClientAuth:             tls.NoClientCert,
		GetCertificate:         func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert() },
	}
	if legacy {
		c.MinVersion = tls.VersionTLS10
		c.CipherSuites = append(c.CipherSuites, tlsSuitesLegacy...)
	}
	return c
}

// tlsState is the TLS listener's handshake side.
type tlsState struct {
	cfg *tls.Config

	mu       sync.Mutex
	inflight map[net.Conn]netip.Addr
	perAddr  map[netip.Addr]int
	closed   bool
}

func newTLSState(cfg *tls.Config) *tlsState {
	return &tlsState{cfg: cfg, inflight: map[net.Conn]netip.Addr{}, perAddr: map[netip.Addr]int{}}
}

// closeHandshakes closes every handshake in progress and refuses new ones.
func (ts *tlsState) closeHandshakes() {
	ts.mu.Lock()
	ts.closed = true
	for c := range ts.inflight {
		c.Close()
	}
	ts.mu.Unlock()
}

func (ts *tlsState) inProgress() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.inflight)
}

// handshake runs one connection's handshake in its own goroutine, outside the
// pool, then serves it like a TCP connection.
func (t *tcpListener) handshake(c net.Conn, from netip.Addr) {
	s, ts := t.srv, t.tls
	a := from.Unmap().WithZone("")
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		c.Close()
		return
	}
	if len(ts.inflight) >= s.opts.MaxHandshakes || ts.perAddr[a] >= s.opts.MaxHandshakesPerAddr {
		ts.mu.Unlock()
		c.Close()
		s.tlsBusy.Add(1)
		s.tlsStats.fail(a, s.opts.Clock().UTC(), TLSBusy, "too many TLS handshakes in progress")
		return
	}
	ts.inflight[c] = a
	ts.perAddr[a]++
	t.wg.Add(1)
	ts.mu.Unlock()

	go func() {
		defer t.wg.Done()
		release := func() {
			ts.mu.Lock()
			delete(ts.inflight, c)
			if ts.perAddr[a]--; ts.perAddr[a] <= 0 {
				delete(ts.perAddr, a)
			}
			ts.mu.Unlock()
		}
		tc := tls.Server(c, ts.cfg)
		_ = c.SetDeadline(time.Now().Add(s.opts.HandshakeTimeout))
		err := tc.Handshake()
		release()
		if err != nil {
			c.Close() // the raw connection: nothing more is written
			ts.mu.Lock()
			shutdown := ts.closed
			ts.mu.Unlock()
			if !shutdown {
				reason, detail := classifyHandshake(err)
				s.tlsFailed.Add(1)
				s.tlsStats.fail(a, s.opts.Clock().UTC(), reason, detail)
			}
			return
		}
		_ = c.SetDeadline(time.Time{}) // from here the frame reader sets the read deadlines
		cs := tc.ConnectionState()
		s.tlsStats.connected(a, s.opts.Clock().UTC(), tls.VersionName(cs.Version), tls.CipherSuiteName(cs.CipherSuite))
		conn := newTconn(tc, from, time.Now())
		conn.raw = c
		if !t.track(conn) {
			c.Close()
			return
		}
		t.serve(conn)
	}()
}

// classifyHandshake turns a server handshake error into a reason and the text
// kept with it (the TLS error or the alert; nothing the sender sent as data).
func classifyHandshake(err error) (reason, detail string) {
	text := err.Error()
	var rhe tls.RecordHeaderError
	var op *net.OpError
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return TLSTimeout, ""
	case errors.As(err, &rhe):
		return TLSNotTLS, "the first bytes weren't a TLS handshake (plain syslog sent to the TLS port?)"
	case errors.As(err, &op) && op.Op == "remote error":
		switch alertCode(op.Err) {
		case 42, 43, 44, 45, 46, 48: // bad, unsupported, revoked, expired, unknown certificate; unknown CA
			return TLSCertRejected, text
		case 70: // protocol_version
			return TLSVersion, text
		case 40, 71: // handshake_failure, insufficient_security: usually no suite in common
			return TLSCipher, text
		}
		return TLSOther, text
	case strings.Contains(text, "unsupported versions") || strings.Contains(text, "protocol version"):
		return TLSVersion, text
	case strings.Contains(text, "no cipher suite supported") || strings.Contains(text, "no ECDHE curve") ||
		strings.Contains(text, "no mutually supported") || strings.Contains(text, "no supported elliptic curves"):
		return TLSCipher, text
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, net.ErrClosed) || strings.Contains(text, "connection reset") ||
		strings.Contains(text, "forcibly closed"):
		return TLSClosed, ""
	}
	return TLSOther, text
}

// alertCode is the number of a received TLS alert (crypto/tls's alert type is
// unexported; it's a uint8), or -1.
func alertCode(e error) int {
	if e == nil {
		return -1
	}
	if a, ok := errors.AsType[tls.AlertError](e); ok {
		return int(a)
	}
	v := reflect.ValueOf(e)
	if v.Kind() == reflect.Uint8 {
		return int(v.Uint())
	}
	return -1
}

// ---- per-sender TLS stats (SPEC §10.3.4) ----

// maxTLSSenders caps the senders tracked; the least recently seen is evicted.
const maxTLSSenders = 10000

// TLSFailure is the latest failed handshake of a sender.
type TLSFailure struct {
	Time   time.Time `json:"time"`
	Reason string    `json:"reason"`
	Detail string    `json:"detail"`
}

// TLSSender is one sender's TLS counts (the tls object on /api/devices).
type TLSSender struct {
	IP             string
	Connections24h int64
	Version        string // of the last successful connection ("TLS 1.3")
	Cipher         string // its cipher suite (Go's name, e.g. TLS_AES_128_GCM_SHA256)
	LastConnected  time.Time
	Failures24h    int64
	LastFailure    *TLSFailure
	LastSeen       time.Time
	Delivered      bool // a message has arrived over TLS from it since start
}

type tlsEntry struct {
	ip         netip.Addr
	conns      hours
	fails      hours
	version    string
	cipher     string
	connected  time.Time
	failure    *TLSFailure
	last       time.Time
	delivered  bool
	prev, next *tlsEntry
}

type tlsStats struct {
	mu         sync.Mutex
	max        int
	ips        map[netip.Addr]*tlsEntry
	head, tail *tlsEntry // most and least recently seen
}

func newTLSStats(max int) *tlsStats {
	return &tlsStats{max: max, ips: map[netip.Addr]*tlsEntry{}}
}

// touch returns ip's entry, creating it (evicting the least recently seen when
// full), and makes it the most recent (mu held).
func (t *tlsStats) touch(ip netip.Addr, now time.Time) *tlsEntry {
	e := t.ips[ip]
	if e == nil {
		if len(t.ips) >= t.max && t.tail != nil {
			old := t.tail
			t.unlink(old)
			delete(t.ips, old.ip)
		}
		e = &tlsEntry{ip: ip}
		t.ips[ip] = e
	} else {
		t.unlink(e)
	}
	e.next = t.head
	if t.head != nil {
		t.head.prev = e
	}
	t.head = e
	if t.tail == nil {
		t.tail = e
	}
	if now.After(e.last) {
		e.last = now
	}
	return e
}

func (t *tlsStats) unlink(e *tlsEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		t.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		t.tail = e.prev
	}
	e.prev, e.next = nil, nil
}

func (t *tlsStats) fail(ip netip.Addr, now time.Time, reason, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.touch(ip, now)
	e.fails.add(now, 1)
	e.failure = &TLSFailure{Time: now, Reason: reason, Detail: detail}
}

func (t *tlsStats) connected(ip netip.Addr, now time.Time, version, cipher string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.touch(ip, now)
	e.conns.add(now, 1)
	e.version, e.cipher, e.connected = version, cipher, now
}

// delivered marks ip as having got a message through over TLS (a map lookup on
// the message path; the entry exists since its handshake).
func (t *tlsStats) delivered(ip netip.Addr) {
	t.mu.Lock()
	if e := t.ips[ip]; e != nil {
		e.delivered = true
	}
	t.mu.Unlock()
}

func (e *tlsEntry) sender(now time.Time) TLSSender {
	s := TLSSender{
		IP: e.ip.String(), Connections24h: e.conns.sum(now), Version: e.version, Cipher: e.cipher,
		LastConnected: e.connected, Failures24h: e.fails.sum(now), LastSeen: e.last, Delivered: e.delivered,
	}
	if e.failure != nil {
		f := *e.failure
		s.LastFailure = &f
	}
	return s
}

// get returns ip's stats, if it has tried TLS since start.
func (t *tlsStats) get(ip netip.Addr, now time.Time) (TLSSender, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.ips[ip.Unmap().WithZone("")]
	if e == nil {
		return TLSSender{}, false
	}
	return e.sender(now), true
}

// failures returns the senders with failed handshakes in the last 24 h, most
// failures first (then most recent), at most limit.
func (t *tlsStats) failures(now time.Time, limit int) []TLSSender {
	t.mu.Lock()
	var out []TLSSender
	for e := t.head; e != nil; e = e.next {
		if n := e.fails.sum(now); n > 0 {
			out = append(out, e.sender(now))
		}
	}
	t.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Failures24h != out[j].Failures24h {
			return out[i].Failures24h > out[j].Failures24h
		}
		return out[i].LastFailure.Time.After(out[j].LastFailure.Time)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// MaxTLSFailuresList is the most entries TLSFailures returns (docs/api.md).
const MaxTLSFailuresList = 200

// TLSFailures returns the senders whose TLS handshakes failed in the last 24 h
// (GET /api/tls/failures), most failures first, at most MaxTLSFailuresList.
func (s *Server) TLSFailures() []TLSSender {
	return s.tlsStats.failures(s.opts.Clock(), MaxTLSFailuresList)
}

// TLSSenderStats returns one sender's TLS stats, and false if it hasn't tried
// TLS since start (the device's tls is then null).
func (s *Server) TLSSenderStats(ip netip.Addr) (TLSSender, bool) {
	return s.tlsStats.get(ip, s.opts.Clock())
}

// TLSAddr returns the bound TLS address, or false if TLS isn't running.
func (s *Server) TLSAddr() (netip.AddrPort, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tls == nil {
		return netip.AddrPort{}, false
	}
	return s.tls.bound, true
}

// ---- lifecycle (callers hold s.mu) ----

// errNoTLSCert is the TLS listener's status while there's no certificate to
// serve (it can't have happened through the API, which makes one first).
var errNoTLSCert = errors.New("no TLS certificate: turn TLS on in Settings, or set listeners.tls.cert_file and key_file")

func (s *Server) startTLS(ev *events) {
	s.tlsErr = nil
	if !s.cfg.TLS.Enabled || s.closed {
		return
	}
	ln := s.cfg.TLS.Listener()
	if s.opts.TLSCertificate == nil {
		st := ListenerStatus{Proto: "tls", Addr: ln.Addr, Port: ln.Port, Error: errNoTLSCert.Error()}
		s.tlsErr = &st
		ev.bind(st)
		return
	}
	if _, err := s.opts.TLSCertificate(); err != nil {
		st := ListenerStatus{Proto: "tls", Addr: ln.Addr, Port: ln.Port, Error: "certificate: " + err.Error()}
		s.tlsErr = &st
		ev.bind(st)
		return
	}
	cfg := ServerTLSConfig(s.cfg.TLS.AllowLegacyTLS, s.opts.TLSCertificate)
	t, err := listenStream(s, ln, "tls", newTLSState(cfg))
	if err != nil {
		st := s.bindFailure("tcp", ln, err)
		st.Proto = "tls"
		s.tlsErr = &st
		ev.bind(st)
		return
	}
	s.tls = t
	ev.add("tls", LevelInfo, KindStart, "TLS listener started on "+hostPort(ln.Addr, int(t.bound.Port())))
	s.log.Info("listen: TLS listening", "addr", t.bound, "legacy", s.cfg.TLS.AllowLegacyTLS)
}

func (s *Server) stopTLS(ev *events) {
	s.tlsErr = nil
	if s.tls == nil {
		return
	}
	s.tls.close()
	ev.add("tls", LevelInfo, KindStop, "TLS listener on "+hostPort(s.tls.addr, int(s.tls.bound.Port()))+" stopped")
	s.tls = nil
}

// tlsChanged reports whether the TLS listener must be rebound: its switch,
// address, port or version policy changed. A new certificate needs no rebind
// (it's read per handshake).
func tlsChanged(a, b config.TLSListener) bool {
	return a.Enabled != b.Enabled || a.Addr != b.Addr || a.Port != b.Port || a.AllowLegacyTLS != b.AllowLegacyTLS
}
