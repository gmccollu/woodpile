// Package listen runs Woodpile's syslog listeners (SPEC §3.1): one UDP socket and
// one TCP listener, each framing messages, applying the sender allowlist, parsing,
// and handing every message to a Sink.
//
// A listener that fails to bind doesn't stop the others. Its status says why (the
// OS error and, where the OS lets us find out, the process holding the port), and
// Retry rebinds it without touching the healthy ones.
package listen

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gmccollu/woodpile/internal/config"
	"github.com/gmccollu/woodpile/internal/model"
	"github.com/gmccollu/woodpile/internal/syslog"
)

// Defaults for Options.
const (
	DefaultMaxConns    = 1000
	DefaultIdleTimeout = 10 * time.Minute
)

// Sink receives every accepted message. It's called concurrently from the UDP
// reader and from each TCP connection's goroutine, so it must be safe for
// concurrent use, and it should be quick: a slow sink makes UDP drop in the
// kernel. The message's Raw is freshly allocated and owned by the sink.
type Sink func(model.Message)

// Event kinds and levels. They have the same values as index.Kind and index.Level,
// so the server can record an Event with index.AddEvent unchanged.
const (
	KindStart = "start"
	KindStop  = "stop"
	KindBind  = "bind"

	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Event is a listener start, stop or bind failure.
type Event struct {
	Time  time.Time
	Level string // LevelInfo, LevelWarn or LevelError
	Kind  string // KindStart, KindStop or KindBind
	Text  string
}

// Options configures a Server. Only Sink is required.
type Options struct {
	Sink Sink
	// Clock gives each message its Received time (one call per message). Default time.Now.
	Clock func() time.Time
	// OnEvent is called for start, stop and bind events, never while the Server's
	// lock is held (it may call Status).
	OnEvent func(Event)
	Logger  *slog.Logger // default slog.Default()

	MaxConns    int           // TCP connections at once (default 1000); more are refused
	IdleTimeout time.Duration // close a TCP connection after this long without data (default 10 min)

	// Holder names the process holding proto ("udp" or "tcp") port, best effort, or
	// "". Default: the OS lookup (lsof on macOS, /proc on Linux, the IP Helper
	// tables on Windows).
	Holder func(proto string, port int) string
}

// ListenerStatus is one entry of /api/status.listeners.
type ListenerStatus struct {
	Proto  string `json:"proto"` // "udp" or "tcp"
	Addr   string `json:"addr"`
	Port   int    `json:"port"` // the bound port when OK (differs from the config only for port 0)
	OK     bool   `json:"ok"`
	Error  string `json:"error"`
	Holder string `json:"holder,omitempty"`
}

// Status is the listeners' state for /api/status.
type Status struct {
	Listeners []ListenerStatus // enabled listeners, UDP first

	UDPRcvbufBytes          int // receive buffer the OS granted (0 if UDP isn't bound)
	UDPRcvbufRequestedBytes int

	// KernelDropsVisible is true where the OS exposes our socket's drop counter
	// (Linux). KernelDrops is then the count since Start.
	KernelDropsVisible bool
	KernelDrops        int64

	Received  int64 // messages handed to the sink since Start
	Truncated int64 // of those, cut at the max message size
	Rejected  int64 // refused by the allowlist since Start (a refused TCP connection counts once)

	TCPConns        int   // open TCP connections
	TCPConnsRefused int64 // connections closed at once because MaxConns were open
}

// Server owns the listeners. Its methods are safe for concurrent use.
type Server struct {
	opts Options
	log  *slog.Logger

	mu     sync.Mutex // serialises Start/Retry/Reconfigure/Close and guards the fields below
	cfg    config.Listeners
	udp    *udpListener
	tcp    *tcpListener
	udpErr *ListenerStatus // set while the enabled UDP listener is down
	tcpErr *ListenerStatus
	closed bool

	allow   atomic.Pointer[allowlist]
	maxSize atomic.Int64

	received, truncated, connsRefused atomic.Int64
	conns                             atomic.Int64

	rejects *rejects
	kdrops  *kernelDrops

	stop chan struct{}
	wg   sync.WaitGroup
}

// Start binds the enabled listeners in cfg and starts receiving. Bind failures
// don't make it fail: they show in Status and as bind events, and Retry tries
// again. It returns an error only for an invalid cfg (an allowlist entry that isn't
// an IP or CIDR) or a missing Sink. Port 0 binds an ephemeral port (tests).
func Start(cfg config.Listeners, opts Options) (*Server, error) {
	if opts.Sink == nil {
		return nil, errors.New("listen: Sink is required")
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxConns <= 0 {
		opts.MaxConns = DefaultMaxConns
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = DefaultIdleTimeout
	}
	if opts.Holder == nil {
		opts.Holder = PortHolder
	}
	al, err := newAllowlist(cfg)
	if err != nil {
		return nil, err
	}
	s := &Server{
		opts:    opts,
		log:     opts.Logger,
		rejects: newRejects(maxRejectedIPs),
		kdrops:  &kernelDrops{},
		stop:    make(chan struct{}),
	}
	s.allow.Store(al)
	s.maxSize.Store(int64(maxSizeOf(cfg)))

	var ev events
	s.mu.Lock()
	s.cfg = cloneListeners(cfg)
	s.startUDP(&ev)
	s.startTCP(&ev)
	s.mu.Unlock()
	ev.flush(s)

	if kernelDropsSupported {
		s.wg.Add(1)
		go s.pollKernelDrops()
	}
	return s, nil
}

// Retry rebinds every enabled listener that is down, leaving the running ones
// alone, and returns the new status.
func (s *Server) Retry() Status {
	var ev events
	s.mu.Lock()
	if !s.closed {
		if s.cfg.UDP.Enabled && s.udp == nil {
			s.startUDP(&ev)
		}
		if s.cfg.TCP.Enabled && s.tcp == nil {
			s.startTCP(&ev)
		}
	}
	s.mu.Unlock()
	ev.flush(s)
	return s.Status()
}

// Reconfigure applies new listener settings live. A listener whose enabled flag,
// address or port changed is stopped and rebound (a failed rebind shows in Status,
// as at Start); one that didn't change keeps running, so its TCP connections stay
// open. A new UDP buffer size is applied to the running socket. The allowlist
// applies from the next message, on open TCP connections too. A new max message
// size applies from the next UDP datagram and new TCP connection (a smaller one
// also to open connections). It returns an error, and changes nothing, if the
// allowlist is invalid.
func (s *Server) Reconfigure(cfg config.Listeners) error {
	al, err := newAllowlist(cfg)
	if err != nil {
		return err
	}
	var ev events
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		ev.flush(s)
	}()
	if s.closed {
		return errors.New("listen: server closed")
	}
	old := s.cfg
	s.cfg = cloneListeners(cfg)
	s.allow.Store(al)
	s.maxSize.Store(int64(maxSizeOf(cfg)))

	if old.UDP != cfg.UDP {
		s.stopUDP(&ev)
		s.startUDP(&ev)
	} else if s.udp != nil && rcvbufOf(old) != rcvbufOf(cfg) {
		s.udp.requested = rcvbufOf(cfg)
		s.udp.granted = setRcvbuf(s.udp.conn, s.udp.requested)
		s.log.Info("listen: UDP receive buffer changed", "requested", s.udp.requested, "granted", s.udp.granted)
	}
	if old.TCP != cfg.TCP {
		s.stopTCP(&ev)
		s.startTCP(&ev)
	}
	return nil
}

// Close stops every listener and closes every TCP connection, and waits for
// their goroutines. No sink call happens after Close returns.
func (s *Server) Close() error {
	var ev events
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.stopUDP(&ev)
	s.stopTCP(&ev)
	close(s.stop)
	s.mu.Unlock()
	s.wg.Wait()
	ev.flush(s)
	return nil
}

// Status returns the listeners' state.
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{
		UDPRcvbufRequestedBytes: rcvbufOf(s.cfg),
		Received:                s.received.Load(),
		Truncated:               s.truncated.Load(),
		Rejected:                s.rejects.totalCount(),
		TCPConns:                int(s.conns.Load()),
		TCPConnsRefused:         s.connsRefused.Load(),
		KernelDropsVisible:      kernelDropsSupported,
	}
	if s.cfg.UDP.Enabled {
		switch {
		case s.udp != nil:
			st.Listeners = append(st.Listeners, s.udp.status())
			st.UDPRcvbufBytes = s.udp.granted
		case s.udpErr != nil:
			st.Listeners = append(st.Listeners, *s.udpErr)
		}
	}
	if s.cfg.TCP.Enabled {
		switch {
		case s.tcp != nil:
			st.Listeners = append(st.Listeners, s.tcp.status())
		case s.tcpErr != nil:
			st.Listeners = append(st.Listeners, *s.tcpErr)
		}
	}
	if kernelDropsSupported {
		st.KernelDrops = s.kdrops.total(s.udp)
	}
	return st
}

// UDPAddr returns the bound UDP address, or false if UDP isn't running.
func (s *Server) UDPAddr() (netip.AddrPort, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.udp == nil {
		return netip.AddrPort{}, false
	}
	return s.udp.bound, true
}

// TCPAddr returns the bound TCP address, or false if TCP isn't running.
func (s *Server) TCPAddr() (netip.AddrPort, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tcp == nil {
		return netip.AddrPort{}, false
	}
	return s.tcp.bound, true
}

// RejectedSender is one entry of /api/rejected.
type RejectedSender struct {
	IP       string    `json:"ip"`
	Count24h int64     `json:"count_24h"`
	LastSeen time.Time `json:"last_seen"`
}

// MaxRejectedList is the most entries Rejected returns (docs/api.md).
const MaxRejectedList = 200

// Rejected returns the senders refused by the allowlist in the last 24 hours,
// sorted by count descending (then most recent first), at most MaxRejectedList.
// Counts are kept in hourly buckets, so "24 hours" is the current hour plus the 23
// before it. At most 10,000 IPs are tracked; the least recently seen is evicted.
func (s *Server) Rejected() []RejectedSender {
	return s.rejects.list(s.opts.Clock(), MaxRejectedList)
}

// Rejected24h returns the messages refused by the allowlist in the last 24 hours
// (for rejected_24h), including those from evicted IPs.
func (s *Server) Rejected24h() int64 { return s.rejects.total24h(s.opts.Clock()) }

// KernelDropped24h returns the kernel's UDP drops for our socket in the last 24
// hours (hourly buckets, sampled every 10 s), and false where the OS doesn't
// expose them (for kernel_dropped_24h: null).
func (s *Server) KernelDropped24h() (int64, bool) {
	if !kernelDropsSupported {
		return 0, false
	}
	s.mu.Lock()
	s.kdrops.sample(s.udp, s.opts.Clock())
	s.mu.Unlock()
	return s.kdrops.sum24h(s.opts.Clock()), true
}

// deliver applies the allowlist, truncation and parsing to one framed message and
// hands it to the sink. raw is only read; the message gets its own copy.
func (s *Server) deliver(raw []byte, from netip.Addr, truncated bool) {
	now := s.opts.Clock().UTC()
	from = from.Unmap()
	if !s.allow.Load().allowed(from) {
		s.rejects.add(from, now, 1)
		return
	}
	if max := int(s.maxSize.Load()); len(raw) > max {
		raw = raw[:max]
		truncated = true
	}
	m := model.Message{Received: now, Sender: from, Raw: append(make([]byte, 0, len(raw)), raw...), Truncated: truncated}
	syslog.Parse(m.Raw, &m)
	if truncated {
		s.truncated.Add(1)
	}
	s.received.Add(1)
	s.opts.Sink(m)
}

// --- lifecycle (callers hold s.mu) ---

func (s *Server) startUDP(ev *events) {
	s.udpErr = nil
	if !s.cfg.UDP.Enabled || s.closed {
		return
	}
	u, err := listenUDP(s, s.cfg.UDP, rcvbufOf(s.cfg))
	if err != nil {
		st := s.bindFailure("udp", s.cfg.UDP, err)
		s.udpErr = &st
		ev.bind(st)
		return
	}
	s.udp = u
	s.kdrops.attach(u)
	ev.add(LevelInfo, KindStart, fmt.Sprintf("UDP listener started on %s; receive buffer %s granted of %s requested",
		hostPort(s.cfg.UDP.Addr, int(u.bound.Port())), sizeText(u.granted), sizeText(u.requested)))
	s.log.Info("listen: UDP listening", "addr", u.bound, "rcvbuf_granted", u.granted, "rcvbuf_requested", u.requested)
}

func (s *Server) stopUDP(ev *events) {
	s.udpErr = nil
	if s.udp == nil {
		return
	}
	s.kdrops.detach(s.udp)
	s.udp.close()
	ev.add(LevelInfo, KindStop, "UDP listener on "+hostPort(s.udp.addr, int(s.udp.bound.Port()))+" stopped")
	s.udp = nil
}

func (s *Server) startTCP(ev *events) {
	s.tcpErr = nil
	if !s.cfg.TCP.Enabled || s.closed {
		return
	}
	t, err := listenTCP(s, s.cfg.TCP)
	if err != nil {
		st := s.bindFailure("tcp", s.cfg.TCP, err)
		s.tcpErr = &st
		ev.bind(st)
		return
	}
	s.tcp = t
	ev.add(LevelInfo, KindStart, "TCP listener started on "+hostPort(s.cfg.TCP.Addr, int(t.bound.Port())))
	s.log.Info("listen: TCP listening", "addr", t.bound)
}

func (s *Server) stopTCP(ev *events) {
	s.tcpErr = nil
	if s.tcp == nil {
		return
	}
	s.tcp.close()
	ev.add(LevelInfo, KindStop, "TCP listener on "+hostPort(s.tcp.addr, int(s.tcp.bound.Port()))+" stopped")
	s.tcp = nil
}

func (s *Server) bindFailure(proto string, ln config.Listener, err error) ListenerStatus {
	st := ListenerStatus{Proto: proto, Addr: ln.Addr, Port: ln.Port, Error: osError(err)}
	if ln.Port > 0 && isAddrInUse(err) {
		st.Holder = safeHolder(s.opts.Holder, proto, ln.Port)
	}
	s.log.Error("listen: bind failed", "proto", proto, "addr", ln.Addr, "port", ln.Port, "err", err, "holder", st.Holder)
	return st
}

// safeHolder runs a holder lookup, which must never break a bind report.
func safeHolder(f func(string, int) string, proto string, port int) (h string) {
	defer func() {
		if recover() != nil {
			h = ""
		}
	}()
	return f(proto, port)
}

// osError returns the OS part of a listen error ("bind: address already in use"),
// without Go's "listen udp 0.0.0.0:514: " prefix.
//
// The common failures read the same on every OS, matched on the errno (see
// commonErrText): "bind: address already in use", "bind: permission denied" (on
// Windows "permission denied or port reserved by the system") and "bind: address
// not available". Anything else keeps the OS text.
func osError(err error) string {
	call := ""
	var se *os.SyscallError
	if errors.As(err, &se) {
		call = se.Syscall
	}
	if text := commonErrText(err); text != "" {
		if call != "" {
			return call + ": " + text
		}
		return text
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	return err.Error()
}

func (s *Server) pollKernelDrops() {
	defer s.wg.Done()
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.mu.Lock()
			s.kdrops.sample(s.udp, s.opts.Clock())
			s.mu.Unlock()
		}
	}
}

// --- events ---

// events collects events while s.mu is held; flush sends them after it's released.
type events []Event

func (e *events) add(level, kind, text string) {
	*e = append(*e, Event{Time: time.Now().UTC(), Level: level, Kind: kind, Text: text})
}

func (e *events) bind(st ListenerStatus) {
	text := fmt.Sprintf("%s %s: %s", strings.ToUpper(st.Proto), hostPort(st.Addr, st.Port), st.Error)
	if st.Holder != "" {
		text += " (held by " + st.Holder + ")"
	}
	e.add(LevelError, KindBind, text)
}

func (e events) flush(s *Server) {
	if s.opts.OnEvent == nil {
		return
	}
	for _, ev := range e {
		s.opts.OnEvent(ev)
	}
}

// --- helpers ---

func cloneListeners(l config.Listeners) config.Listeners {
	l.Allowlist = append([]string(nil), l.Allowlist...)
	return l
}

func rcvbufOf(l config.Listeners) int {
	if l.UDPRcvbufBytes > 0 {
		return l.UDPRcvbufBytes
	}
	return config.DefaultUDPRcvbufBytes
}

func maxSizeOf(l config.Listeners) int {
	if l.MaxMessageBytes > 0 {
		return l.MaxMessageBytes
	}
	return config.DefaultMaxMessageBytes
}

func bindAddr(ln config.Listener) (netip.AddrPort, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(ln.Addr))
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("listen: bad address %q", ln.Addr)
	}
	if ln.Port < 0 || ln.Port > 65535 {
		return netip.AddrPort{}, fmt.Errorf("listen: bad port %d", ln.Port)
	}
	return netip.AddrPortFrom(a, uint16(ln.Port)), nil
}

func hostPort(addr string, port int) string {
	return net.JoinHostPort(addr, strconv.Itoa(port))
}

func sizeText(n int) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

func addrOf(a net.Addr) netip.Addr {
	switch v := a.(type) {
	case *net.TCPAddr:
		return v.AddrPort().Addr().Unmap()
	case *net.UDPAddr:
		return v.AddrPort().Addr().Unmap()
	}
	return netip.Addr{}
}
