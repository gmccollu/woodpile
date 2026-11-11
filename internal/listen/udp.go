package listen

import (
	"errors"
	"net"
	"net/netip"
	"time"

	"woodpile/internal/config"
	"woodpile/internal/syslog"
)

// udpReadBuf is the read buffer: big enough for any UDP datagram (65,507 bytes
// over IPv4, 65,527 over IPv6), so the OS never cuts one before we see its length.
const udpReadBuf = 64 << 10

type udpListener struct {
	srv       *Server
	conn      *net.UDPConn
	addr      string // as configured
	bound     netip.AddrPort
	requested int
	granted   int
	done      chan struct{}
}

func listenUDP(s *Server, ln config.Listener, rcvbuf int) (*udpListener, error) {
	ap, err := bindAddr(ln)
	if err != nil {
		return nil, err
	}
	// "udp" with a wildcard address binds dual-stack (IPv4 and IPv6) where the OS
	// supports it.
	conn, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(ap))
	if err != nil {
		return nil, err
	}
	u := &udpListener{
		srv:       s,
		conn:      conn,
		addr:      ln.Addr,
		bound:     conn.LocalAddr().(*net.UDPAddr).AddrPort(),
		requested: rcvbuf,
		done:      make(chan struct{}),
	}
	u.granted = setRcvbuf(conn, rcvbuf)
	go u.run()
	return u, nil
}

func (u *udpListener) status() ListenerStatus {
	return ListenerStatus{Proto: "udp", Addr: u.addr, Port: int(u.bound.Port()), OK: true}
}

// run reads datagrams until the socket is closed. One datagram is one message.
func (u *udpListener) run() {
	defer close(u.done)
	buf := make([]byte, udpReadBuf)
	var errRun int
	var lastLog time.Time
	for {
		n, from, err := u.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Transient errors (ICMP-induced resets on Windows, ENOBUFS, ...). Log at
			// most once a minute and back off if they persist, so a broken socket
			// can't spin a core.
			if time.Since(lastLog) > time.Minute {
				u.srv.log.Warn("listen: UDP read error", "err", err)
				lastLog = time.Now()
			}
			if errRun++; errRun > 100 {
				time.Sleep(10 * time.Millisecond)
			}
			continue
		}
		errRun = 0
		msg := syslog.TrimDatagram(buf[:n])
		if len(msg) == 0 {
			continue
		}
		u.srv.deliver(msg, from.Addr(), false)
	}
}

func (u *udpListener) close() {
	u.conn.Close()
	<-u.done
}

// setRcvbuf asks for a receive buffer of want bytes and returns what the OS
// granted. macOS refuses sizes above kern.ipc.maxsockbuf outright (ENOBUFS)
// instead of capping them, so on refusal it searches for the largest size the OS
// accepts.
func setRcvbuf(c *net.UDPConn, want int) int {
	if want <= 0 {
		return getRcvbuf(c)
	}
	if err := c.SetReadBuffer(want); err != nil {
		lo, hi := 0, want // lo: accepted (or untried 0), hi: refused
		for hi-lo > 16<<10 {
			mid := lo + (hi-lo)/2
			if c.SetReadBuffer(mid) == nil {
				lo = mid
			} else {
				hi = mid
			}
		}
		if lo > 0 {
			c.SetReadBuffer(lo)
		}
	}
	return getRcvbuf(c)
}

// getRcvbuf returns the socket's receive buffer size as the OS reports it (on
// Linux, which doubles the value for its own bookkeeping, halved back so it
// compares with the request).
func getRcvbuf(c *net.UDPConn) int {
	rc, err := c.SyscallConn()
	if err != nil {
		return 0
	}
	n := 0
	rc.Control(func(fd uintptr) { n = sockRcvbuf(fd) })
	return n
}
