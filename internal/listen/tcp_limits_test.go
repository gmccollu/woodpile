package listen

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/config"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// closedByServer reports whether the server closed c (EOF or reset) within d.
func closedByServer(c net.Conn, d time.Duration) bool {
	c.SetReadDeadline(time.Now().Add(d))
	_, err := c.Read(make([]byte, 1))
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return false
	}
	return err != nil
}

func TestSourceKey(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.7":                 "192.0.2.7/32",
		"::ffff:192.0.2.7":          "192.0.2.7/32",
		"2001:db8:1:2:aaaa::1":      "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::9%eth0": "2001:db8:1:2::/64",
	} {
		if got := sourceKey(netip.MustParseAddr(in)).String(); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

// TestOneSourceCantTakeAllSlots is the review's M6 attack: 1,000 connections from
// one host, each sending one byte. With the default limits only 32 get in, and a
// second source still delivers.
func TestOneSourceCantTakeAllSlots(t *testing.T) {
	// A dual-stack wildcard listener, so 127.0.0.1 and ::1 are two sources.
	cfg := config.Listeners{TCP: config.Listener{Enabled: true, Addr: "::", Port: 0}}
	s, c, _ := start(t, cfg, nil)
	ap, _ := s.TCPAddr()
	v4 := fmt.Sprintf("127.0.0.1:%d", ap.Port())
	var conns []net.Conn
	defer func() {
		for _, cn := range conns {
			cn.Close()
		}
	}()
	for i := 0; i < DefaultMaxConns; i++ {
		cn, err := net.Dial("tcp", v4)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		conns = append(conns, cn)
		cn.Write([]byte("<"))
	}
	refused := int64(DefaultMaxConns - config.DefaultTCPMaxConnsPerSource)
	waitFor(t, "the accepts", func() bool { return s.Status().TCPConnsPerSourceRefused == refused })
	st := s.Status()
	if st.TCPConns != config.DefaultTCPMaxConnsPerSource || st.TCPConnsRefused != 0 || st.TCPConnsEvicted != 0 {
		t.Errorf("status %+v", st)
	}
	rej := s.Rejected()
	if len(rej) != 1 || rej[0].IP != "127.0.0.1" || rej[0].Count24h != refused || rej[0].Reason != RejectPerSource {
		t.Errorf("rejected %+v", rej)
	}

	// A second source gets in and its message is stored.
	other, err := net.Dial("tcp", fmt.Sprintf("[::1]:%d", ap.Port()))
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	defer other.Close()
	other.Write([]byte("<13>real sender\n"))
	got := c.wait(t, 1)
	if string(got[0].Raw) != "<13>real sender" || got[0].Sender != netip.MustParseAddr("::1") {
		t.Errorf("got %q from %v", got[0].Raw, got[0].Sender)
	}
}

func TestFrameDeadlineCutsTrickler(t *testing.T) {
	cfg := loopback(0, 0)
	cfg.TCPFrameTimeout = config.Duration(300 * time.Millisecond)
	s, c, _ := start(t, cfg, nil)
	conn, err := net.Dial("tcp", tcpAddr(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	msg := "<13>a message sent one byte at a time"
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for i := 0; i < len(msg); i++ {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			if _, err := conn.Write([]byte{msg[i]}); err != nil {
				return
			}
		}
	}()
	start := time.Now()
	if !closedByServer(conn, 5*time.Second) {
		t.Fatal("trickling connection still open after 5 s")
	}
	if d := time.Since(start); d < 250*time.Millisecond || d > 2*time.Second {
		t.Errorf("cut after %v, want about 300 ms after the first byte", d)
	}
	got := c.wait(t, 1)
	if !got[0].Truncated || len(got[0].Raw) == 0 || !strings.HasPrefix(msg, string(got[0].Raw)) || len(got[0].Raw) == len(msg) {
		t.Errorf("partial: %q truncated=%v", got[0].Raw, got[0].Truncated)
	}
	if st := s.Status(); st.TCPFrameTimeouts != 1 || st.Truncated != 1 {
		t.Errorf("status %+v", st)
	}
}

func TestPauseBetweenFramesKept(t *testing.T) {
	cfg := loopback(0, 0)
	cfg.TCPFrameTimeout = config.Duration(200 * time.Millisecond)
	s, c, _ := start(t, cfg, nil)
	conn, err := net.Dial("tcp", tcpAddr(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for i, m := range []string{"<13>a", "<13>b", "<13>c"} {
		if i > 0 {
			time.Sleep(600 * time.Millisecond) // three frame timeouts between messages
		}
		if _, err := conn.Write([]byte(m + "\n")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	got := c.wait(t, 3)
	for i, w := range []string{"<13>a", "<13>b", "<13>c"} {
		if string(got[i].Raw) != w || got[i].Truncated {
			t.Errorf("[%d] %q truncated=%v", i, got[i].Raw, got[i].Truncated)
		}
	}
	if closedByServer(conn, 100*time.Millisecond) {
		t.Error("server closed a connection that paused between messages")
	}
	if st := s.Status(); st.TCPConns != 1 || st.TCPFrameTimeouts != 0 {
		t.Errorf("status %+v", st)
	}
}

// Single bytes don't renew the idle timeout; only complete messages do.
func TestIdleRenewedOnlyByFrames(t *testing.T) {
	cfg := loopback(0, 0)
	cfg.TCPFrameTimeout = config.Duration(time.Hour)
	s, c, _ := start(t, cfg, func(o *Options) { o.IdleTimeout = 400 * time.Millisecond })
	conn, err := net.Dial("tcp", tcpAddr(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("<13>first\n"))
	c.wait(t, 1)
	start := time.Now()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
			if _, err := conn.Write([]byte("x")); err != nil {
				return
			}
		}
	}()
	if !closedByServer(conn, 5*time.Second) {
		t.Fatal("a byte every 100 ms kept the connection open")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("closed after %v, want about the 400 ms idle timeout", d)
	}
	got := c.wait(t, 2)
	if !got[1].Truncated || !strings.HasPrefix(string(got[1].Raw), "x") {
		t.Errorf("partial %q truncated=%v", got[1].Raw, got[1].Truncated)
	}
}

func TestEvictLongestIdle(t *testing.T) {
	cfg := loopback(0, 0)
	s, c, _ := start(t, cfg, func(o *Options) {
		o.MaxConns = 3
		o.EvictIdle = time.Second // wide, so B, C and D never look idle on a slow runner
	})
	addr := tcpAddr(t, s)
	dial := func() net.Conn {
		cn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cn.Close() })
		return cn
	}
	// A: a partial message, then nothing.
	a := dial()
	a.Write([]byte("<13>half a mess"))
	waitFor(t, "A", func() bool { return s.Status().TCPConns == 1 })
	time.Sleep(1300 * time.Millisecond)
	// B and C are fresh.
	b, cc := dial(), dial()
	fmt.Fprintf(b, "<13>b\n")
	fmt.Fprintf(cc, "<13>c\n")
	c.wait(t, 2)

	// D finds the table full and evicts A, the one idle longest.
	d := dial()
	fmt.Fprintf(d, "<13>d\n")
	if !closedByServer(a, 5*time.Second) {
		t.Fatal("the idle connection wasn't evicted")
	}
	got := c.wait(t, 4)
	var partial, fromD bool
	for _, m := range got {
		switch string(m.Raw) {
		case "<13>half a mess":
			partial = m.Truncated
		case "<13>d":
			fromD = true
		}
	}
	if !partial || !fromD {
		t.Errorf("evicted partial truncated=%v, D delivered=%v: %v", partial, fromD, got)
	}
	st := s.Status()
	if st.TCPConnsEvicted != 1 || st.TCPConns != 3 || st.TCPConnsRefused != 0 {
		t.Errorf("status %+v", st)
	}

	// Nobody is idle long enough now: the next newcomer is refused.
	e := dial()
	if !closedByServer(e, 5*time.Second) {
		t.Error("newcomer accepted with no idle connection to evict")
	}
	waitFor(t, "the refusal", func() bool { return s.Status().TCPConnsRefused == 1 })
	if st := s.Status(); st.TCPConnsEvicted != 1 {
		t.Errorf("status %+v", st)
	}
	for _, cn := range []net.Conn{b, cc, d} {
		if closedByServer(cn, 20*time.Millisecond) {
			t.Error("an active connection was closed")
		}
	}
}
