package listen

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/gmccollu/woodpile/internal/config"
	"github.com/gmccollu/woodpile/internal/syslog"
)

type tcpListener struct {
	srv   *Server
	ln    *net.TCPListener
	addr  string // as configured
	bound netip.AddrPort

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	closing bool
	wg      sync.WaitGroup // the accept loop and every connection
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
		srv:   s,
		ln:    l,
		addr:  ln.Addr,
		bound: l.Addr().(*net.TCPAddr).AddrPort(),
		conns: map[net.Conn]struct{}{},
	}
	t.wg.Add(1)
	go t.accept()
	return t, nil
}

func (t *tcpListener) status() ListenerStatus {
	return ListenerStatus{Proto: "tcp", Addr: t.addr, Port: int(t.bound.Port()), OK: true}
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
			t.srv.rejects.add(from, t.srv.opts.Clock().UTC(), 1)
			c.Close()
			continue
		}
		if !t.track(c) {
			c.Close()
			continue
		}
		go t.serve(c, from)
	}
}

// track registers a connection, and reports false if the listener is closing or
// the connection limit is reached.
func (t *tcpListener) track(c net.Conn) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closing {
		return false
	}
	if int(t.srv.conns.Load()) >= t.srv.opts.MaxConns {
		if t.srv.connsRefused.Add(1)%1000 == 1 {
			t.srv.log.Warn("listen: TCP connection limit reached; refusing connections", "limit", t.srv.opts.MaxConns,
				"from", c.RemoteAddr().String())
		}
		return false
	}
	t.conns[c] = struct{}{}
	t.srv.conns.Add(1)
	t.wg.Add(1)
	return true
}

func (t *tcpListener) untrack(c net.Conn) {
	t.mu.Lock()
	delete(t.conns, c)
	t.mu.Unlock()
	t.srv.conns.Add(-1)
	t.wg.Done()
}

func (t *tcpListener) serve(c net.Conn, from netip.Addr) {
	defer t.untrack(c)
	defer c.Close()
	r := syslog.NewStreamReader(idleReader{c, t.srv.opts.IdleTimeout}, int(t.srv.maxSize.Load()))
	for {
		msg, truncated, err := r.Next()
		if err != nil {
			return // EOF, idle timeout, reset, or closed by close()
		}
		t.srv.deliver(msg, from, truncated)
	}
}

// close stops accepting, closes every connection and waits for their goroutines.
func (t *tcpListener) close() {
	t.mu.Lock()
	t.closing = true
	t.ln.Close()
	for c := range t.conns {
		c.Close()
	}
	t.mu.Unlock()
	t.wg.Wait()
}

// idleReader extends the read deadline before every read, so a connection that
// sends nothing for the idle timeout is closed.
type idleReader struct {
	c    net.Conn
	idle time.Duration
}

func (r idleReader) Read(p []byte) (int, error) {
	if err := r.c.SetReadDeadline(time.Now().Add(r.idle)); err != nil {
		return 0, err
	}
	return r.c.Read(p)
}
