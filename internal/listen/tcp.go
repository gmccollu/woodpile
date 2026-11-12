package listen

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gmccollu/woodshed/internal/config"
	"github.com/gmccollu/woodshed/internal/syslog"
)

// TCP abuse limits (security review M6). One host must not be able to take every
// connection slot by opening many connections and dribbling a byte now and then:
//
//   - at most tcp_max_conns_per_source connections per sender address, and at most
//     tcp_max_conns_per_prefix per IPv6 /64 (one host can pick from a whole /64,
//     but a server VLAN shares one too);
//   - once a message starts, all of it must arrive within tcp_frame_timeout, or
//     the connection is closed and the part that arrived is stored, marked
//     truncated;
//   - between messages, the idle timeout is renewed only by complete messages;
//   - when MaxConns are open, a newcomer evicts the connection idle longest (no
//     complete message for more than EvictIdle); if none is, the oldest connection
//     of the biggest source (IPv4 address or IPv6 /64), so busy sources can't
//     pin every slot. A newcomer whose own source is the biggest is refused.

type tcpListener struct {
	srv   *Server
	ln    *net.TCPListener
	addr  string // as configured
	bound netip.AddrPort

	mu       sync.Mutex
	conns    map[*tconn]struct{}
	perAddr  map[netip.Addr]int   // open connections per sender address
	perGroup map[netip.Prefix]int // per source group: the IPv4 address, or the IPv6 /64
	seq      uint64               // last accept number handed out
	closing  bool
	wg       sync.WaitGroup // the accept loop and every connection
}

// tconn is one open connection.
type tconn struct {
	c       net.Conn
	from    netip.Addr
	addr    netip.Addr   // from without a zone, for the per-address count
	group   netip.Prefix // sourceKey(from)
	connAt  time.Time
	seq     uint64       // accept order; "oldest" goes by this, not connAt (Windows' clock can tie)
	last    atomic.Int64 // UnixNano of the connect or the last complete message
	cut     atomic.Bool  // we closed it (eviction or shutdown): a partial message is truncated
	blocked atomic.Bool  // closed because its sender was blocked: nothing more is stored
}

func listenTCP(s *Server, ln config.Listener) (*tcpListener, error) {
	ap, err := bindAddr(ln)
	if err != nil {
		return nil, err
	}
	l, err := net.ListenTCP("tcp", net.TCPAddrFromAddrPort(ap))
	if err != nil {
		return nil, err
	}
	t := &tcpListener{
		srv:      s,
		ln:       l,
		addr:     ln.Addr,
		bound:    l.Addr().(*net.TCPAddr).AddrPort(),
		conns:    map[*tconn]struct{}{},
		perAddr:  map[netip.Addr]int{},
		perGroup: map[netip.Prefix]int{},
	}
	t.wg.Add(1)
	go t.accept()
	return t, nil
}

func (t *tcpListener) status() ListenerStatus {
	return ListenerStatus{Proto: "tcp", Addr: t.addr, Port: int(t.bound.Port()), OK: true}
}

// sourceKey is a connection's source group: the IPv4 address, or the IPv6 /64.
func sourceKey(a netip.Addr) netip.Prefix {
	a = a.Unmap().WithZone("")
	if a.Is4() {
		return netip.PrefixFrom(a, 32)
	}
	p, _ := a.Prefix(64)
	return p
}

func (t *tcpListener) accept() {
	defer t.wg.Done()
	var backoff time.Duration
	var lastLog time.Time
	for {
		c, err := t.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Out of file descriptors and the like: back off (5 ms doubling to 1 s), as
			// net/http does, instead of spinning.
			if backoff == 0 {
				backoff = 5 * time.Millisecond
			} else if backoff *= 2; backoff > time.Second {
				backoff = time.Second
			}
			if time.Since(lastLog) > time.Minute {
				t.srv.log.Warn("listen: TCP accept error", "err", err, "retry_in", backoff)
				lastLog = time.Now()
			}
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		from := addrOf(c.RemoteAddr())
		if r := t.srv.allow.Load().refusal(from); r != "" {
			t.srv.rejects.add(from, t.srv.opts.Clock().UTC(), 1, r)
			c.Close()
			continue
		}
		tc := newTconn(c, from, time.Now())
		if !t.track(tc) {
			c.Close()
			continue
		}
		go t.serve(tc)
	}
}

func newTconn(c net.Conn, from netip.Addr, now time.Time) *tconn {
	tc := &tconn{c: c, from: from, addr: from.Unmap().WithZone(""), group: sourceKey(from), connAt: now}
	tc.last.Store(now.UnixNano())
	return tc
}

// track registers a connection. It reports false (and counts why) if the listener
// is closing, the sender's address or IPv6 /64 is at its limit, or the table is
// full and the newcomer's own source holds the most connections.
func (t *tcpListener) track(tc *tconn) bool {
	s := t.srv
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closing {
		return false
	}
	overAddr := t.perAddr[tc.addr] >= int(s.perSource.Load())
	overPrefix := tc.addr.Is6() && t.perGroup[tc.group] >= int(s.perPrefix.Load())
	if overAddr || overPrefix {
		s.rejects.add(tc.from, s.opts.Clock().UTC(), 1, RejectPerSource)
		if s.perSourceRefused.Add(1)%1000 == 1 {
			s.log.Warn("listen: TCP connections per source limit reached; refusing", "from", tc.from.String(),
				"per_address", s.perSource.Load(), "per_ipv6_64", s.perPrefix.Load())
		}
		return false
	}
	if len(t.conns) >= s.opts.MaxConns {
		now := time.Now()
		victim, why := t.longestIdle(now), "idle longest"
		if victim == nil {
			victim, why = t.oldestOfBiggest(tc.group), "oldest of the biggest source"
		}
		if victim == nil {
			if s.connsRefused.Add(1)%1000 == 1 {
				s.log.Warn("listen: TCP connection limit reached and the newcomer's source holds the most; refusing",
					"limit", s.opts.MaxConns, "from", tc.from.String())
			}
			return false
		}
		victim.cut.Store(true)
		victim.c.Close()
		t.removeLocked(victim) // free the slot now; its goroutine finishes on its own
		if s.evicted.Add(1)%1000 == 1 {
			s.log.Warn("listen: TCP connection limit reached; closing a connection", "limit", s.opts.MaxConns,
				"evicted", victim.from.String(), "why", why)
		}
	}
	t.seq++
	tc.seq = t.seq
	t.conns[tc] = struct{}{}
	t.perAddr[tc.addr]++
	t.perGroup[tc.group]++
	s.conns.Add(1)
	t.wg.Add(1)
	return true
}

// oldestOfBiggest returns the oldest connection of the source group holding the
// most connections, or nil if the newcomer's group holds at least as many as any
// other (t.mu held).
func (t *tcpListener) oldestOfBiggest(newcomer netip.Prefix) *tconn {
	var big netip.Prefix
	most := 0
	for g, n := range t.perGroup {
		if g != newcomer && (n > most || (n == most && g.Addr().Less(big.Addr()))) {
			big, most = g, n
		}
	}
	if most == 0 || t.perGroup[newcomer] >= most {
		return nil
	}
	var oldest *tconn
	for c := range t.conns {
		if c.group == big && (oldest == nil || c.seq < oldest.seq) {
			oldest = c
		}
	}
	return oldest
}

// longestIdle returns the connection with the oldest complete message (or connect),
// if that was more than EvictIdle ago (t.mu held).
func (t *tcpListener) longestIdle(now time.Time) *tconn {
	var best *tconn
	var bestAt int64
	for c := range t.conns {
		if at := c.last.Load(); best == nil || at < bestAt || (at == bestAt && c.seq < best.seq) {
			best, bestAt = c, at
		}
	}
	if best == nil || now.Sub(time.Unix(0, bestAt)) <= t.srv.opts.EvictIdle {
		return nil
	}
	return best
}

func (t *tcpListener) removeLocked(tc *tconn) {
	if _, ok := t.conns[tc]; !ok {
		return
	}
	delete(t.conns, tc)
	if t.perAddr[tc.addr]--; t.perAddr[tc.addr] <= 0 {
		delete(t.perAddr, tc.addr)
	}
	if t.perGroup[tc.group]--; t.perGroup[tc.group] <= 0 {
		delete(t.perGroup, tc.group)
	}
	t.srv.conns.Add(-1)
}

func (t *tcpListener) untrack(tc *tconn) {
	t.mu.Lock()
	t.removeLocked(tc)
	t.mu.Unlock()
	t.wg.Done()
}

func (t *tcpListener) serve(tc *tconn) {
	defer t.untrack(tc)
	defer tc.c.Close()
	fr := &frameReader{c: tc.c, idle: t.srv.opts.IdleTimeout, frameTimeout: &t.srv.frameTimeout, last: &tc.last}
	r := syslog.NewStreamReader(fr, int(t.srv.maxSize.Load()))
	for {
		fails := fr.fails
		msg, truncated, err := r.Next()
		if err != nil {
			if fr.frameCut {
				t.srv.frameCuts.Add(1)
			}
			return // EOF, a deadline, reset, or closed by close() or an eviction
		}
		// A message returned by the call in which the connection ended is the
		// unterminated last one. If we ended it (a deadline, an eviction), it's cut.
		if fr.fails != fails && (fr.timedOut || tc.cut.Load()) {
			truncated = true
		}
		if tc.blocked.Load() {
			return
		}
		if t.srv.deliver(msg, tc.from, truncated) == RejectBlocked {
			return // blocked since it connected: close it
		}
		fr.frameDone(r.Buffered() > 0)
	}
}

// closeBlocked closes the open connections whose sender al blocks, each counted
// once as a refusal. Their partial messages aren't stored.
func (t *tcpListener) closeBlocked(al *allowlist) {
	now := t.srv.opts.Clock().UTC()
	t.mu.Lock()
	defer t.mu.Unlock()
	for c := range t.conns {
		if al.isBlocked(c.from) && !c.blocked.Swap(true) {
			c.c.Close()
			t.srv.rejects.add(c.from, now, 1, RejectBlocked)
		}
	}
}

// close stops accepting, closes every connection and waits for their goroutines.
func (t *tcpListener) close() {
	t.mu.Lock()
	t.closing = true
	t.ln.Close()
	for c := range t.conns {
		c.cut.Store(true)
		c.c.Close()
	}
	t.mu.Unlock()
	t.wg.Wait()
}

// frameReader sets the read deadline before every read: between messages, the idle
// timeout from the last complete message (bytes alone don't renew it); once a
// message's first byte has arrived, the frame timeout from that byte, if sooner.
//
// It hands the StreamReader errors only with no data (a read returning both is
// split), so that "the reader failed during this Next" means "this message is
// the unterminated last one". The frame clock starts at the first read that
// brings a byte other than LF, CR or NUL (the junk the StreamReader skips between
// messages), or as soon as a message completes if the next one has already
// started arriving (StreamReader.Buffered).
type frameReader struct {
	c            net.Conn
	idle         time.Duration
	frameTimeout *atomic.Int64 // time.Duration, live from config
	last         *atomic.Int64 // UnixNano of the last complete message (or the connect)

	frameStart time.Time // zero between messages
	stashed    error
	fails      int  // errors returned to the StreamReader
	timedOut   bool // the connection ended on one of our deadlines
	frameCut   bool // ... on the frame deadline
}

func (r *frameReader) Read(p []byte) (int, error) {
	if r.stashed != nil {
		r.fails++
		return 0, r.stashed
	}
	deadline := time.Unix(0, r.last.Load()).Add(r.idle)
	frameBound := false
	if !r.frameStart.IsZero() {
		if fd := r.frameStart.Add(time.Duration(r.frameTimeout.Load())); fd.Before(deadline) {
			deadline, frameBound = fd, true
		}
	}
	if err := r.c.SetReadDeadline(deadline); err != nil {
		r.fails++
		return 0, err
	}
	n, err := r.c.Read(p)
	if n > 0 && r.frameStart.IsZero() && hasMessageByte(p[:n]) {
		r.frameStart = time.Now()
	}
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			r.timedOut, r.frameCut = true, frameBound
		}
		if n > 0 {
			r.stashed = err
			return n, nil
		}
		r.fails++
		return 0, err
	}
	return n, nil
}

// frameDone records a complete message: it renews the idle timeout, and restarts
// the frame clock if the next message is already buffered (else stops it).
func (r *frameReader) frameDone(nextStarted bool) {
	now := time.Now()
	r.frameStart = time.Time{}
	if nextStarted {
		r.frameStart = now
	}
	r.last.Store(now.UnixNano())
}

// hasMessageByte reports whether b holds a byte other than the LF, CR and NUL
// junk the StreamReader skips between messages.
func hasMessageByte(b []byte) bool {
	for _, c := range b {
		if c != '\n' && c != '\r' && c != 0 {
			return true
		}
	}
	return false
}
