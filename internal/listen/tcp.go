package listen

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gmccollu/woodpile/internal/config"
	"github.com/gmccollu/woodpile/internal/syslog"
)

// TCP abuse limits (security review M6). One host must not be able to take every
// connection slot by opening many connections and dribbling a byte now and then:
//
//   - at most tcp_max_conns_per_source connections per source (an IPv4 address,
//     or an IPv6 /64);
//   - once a message starts, all of it must arrive within tcp_frame_timeout, or
//     the connection is closed and the part that arrived is stored, marked
//     truncated;
//   - between messages, the idle timeout is renewed only by complete messages;
//   - when MaxConns are open, a newcomer evicts the connection idle longest (no
//     complete message for more than EvictIdle) instead of being refused.

type tcpListener struct {
	srv   *Server
	ln    *net.TCPListener
	addr  string // as configured
	bound netip.AddrPort

	mu        sync.Mutex
	conns     map[*tconn]struct{}
	perSource map[netip.Prefix]int
	closing   bool
	wg        sync.WaitGroup // the accept loop and every connection
}

// tconn is one open connection.
type tconn struct {
	c    net.Conn
	from netip.Addr
	key  netip.Prefix
	last atomic.Int64 // UnixNano of the connect or the last complete message
	cut  atomic.Bool  // we closed it (eviction or shutdown): a partial message is truncated
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
		srv:       s,
		ln:        l,
		addr:      ln.Addr,
		bound:     l.Addr().(*net.TCPAddr).AddrPort(),
		conns:     map[*tconn]struct{}{},
		perSource: map[netip.Prefix]int{},
	}
	t.wg.Add(1)
	go t.accept()
	return t, nil
}

func (t *tcpListener) status() ListenerStatus {
	return ListenerStatus{Proto: "tcp", Addr: t.addr, Port: int(t.bound.Port()), OK: true}
}

// sourceKey is what the per-source limit counts by: the IPv4 address, or the
// IPv6 /64 (one host usually has a whole /64 to pick addresses from).
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
		if !t.srv.allow.Load().allowed(from) {
			t.srv.rejects.add(from, t.srv.opts.Clock().UTC(), 1, RejectAllowlist)
			c.Close()
			continue
		}
		tc := &tconn{c: c, from: from, key: sourceKey(from)}
		tc.last.Store(time.Now().UnixNano())
		if !t.track(tc) {
			c.Close()
			continue
		}
		go t.serve(tc)
	}
}

// track registers a connection. It reports false (and counts why) if the listener
// is closing, the source is at its limit, or every slot is taken by a connection
// that isn't idle enough to evict.
func (t *tcpListener) track(tc *tconn) bool {
	s := t.srv
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closing {
		return false
	}
	if t.perSource[tc.key] >= int(s.perSource.Load()) {
		s.rejects.add(tc.from, s.opts.Clock().UTC(), 1, RejectPerSource)
		if s.perSourceRefused.Add(1)%1000 == 1 {
			s.log.Warn("listen: TCP connections per source limit reached; refusing", "limit", s.perSource.Load(),
				"source", tc.key.String())
		}
		return false
	}
	if len(t.conns) >= s.opts.MaxConns {
		victim := t.longestIdle(time.Now())
		if victim == nil {
			if s.connsRefused.Add(1)%1000 == 1 {
				s.log.Warn("listen: TCP connection limit reached and none idle; refusing connections",
					"limit", s.opts.MaxConns, "from", tc.from.String())
			}
			return false
		}
		victim.cut.Store(true)
		victim.c.Close()
		t.removeLocked(victim) // free the slot now; its goroutine finishes on its own
		if s.evicted.Add(1)%1000 == 1 {
			s.log.Warn("listen: TCP connection limit reached; closing the connection idle longest",
				"limit", s.opts.MaxConns, "evicted", victim.from.String())
		}
	}
	t.conns[tc] = struct{}{}
	t.perSource[tc.key]++
	s.conns.Add(1)
	t.wg.Add(1)
	return true
}

// longestIdle returns the connection with the oldest complete message (or connect),
// if that was more than EvictIdle ago (t.mu held).
func (t *tcpListener) longestIdle(now time.Time) *tconn {
	var best *tconn
	var bestAt int64
	for c := range t.conns {
		if at := c.last.Load(); best == nil || at < bestAt {
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
	if t.perSource[tc.key]--; t.perSource[tc.key] <= 0 {
		delete(t.perSource, tc.key)
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
		t.srv.deliver(msg, tc.from, truncated)
		fr.frameDone()
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
// the unterminated last one". The frame clock starts at the first read with data
// after a message completes. Bytes of the next message that arrived in the same
// read as the end of the previous one start it at the next read instead, so a
// message's time is bounded by the idle timeout plus the frame timeout.
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
	if n > 0 && r.frameStart.IsZero() {
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

// frameDone records a complete message: it renews the idle timeout and stops the
// frame clock.
func (r *frameReader) frameDone() {
	r.frameStart = time.Time{}
	r.last.Store(time.Now().UnixNano())
}
