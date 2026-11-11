package listen

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/config"
)

func rejectedFrom(s *Server, ip string) (RejectedSender, bool) {
	for _, r := range s.Rejected() {
		if r.IP == ip {
			return r, true
		}
	}
	return RejectedSender{}, false
}

func TestBlockSingleIP(t *testing.T) {
	cfg := loopback(0, 0)
	cfg.Blocklist = []string{"127.0.0.1"}
	s, c, _ := start(t, cfg, nil)
	sendUDP(t, udpAddr(t, s), "<13>one", "<13>two")
	conn, err := net.Dial("tcp", tcpAddr(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("<13>tcp\n"))
	if !closedByServer(conn, 5*time.Second) {
		t.Error("blocked TCP connection accepted")
	}
	waitFor(t, "the refusals", func() bool { return s.Status().Rejected == 3 })
	if c.count() != 0 {
		t.Errorf("%d blocked messages reached the sink", c.count())
	}
	r, ok := rejectedFrom(s, "127.0.0.1")
	if !ok || r.Count24h != 3 || r.Reason != RejectBlocked || s.Rejected24h() != 3 {
		t.Errorf("rejected %+v (24h %d)", r, s.Rejected24h())
	}
	if e, ok := s.BlockedBy(netip.MustParseAddr("127.0.0.1")); !ok || e != "127.0.0.1" {
		t.Errorf("BlockedBy %q %v", e, ok)
	}
	if _, ok := s.BlockedBy(netip.MustParseAddr("192.0.2.1")); ok {
		t.Error("BlockedBy: unlisted address")
	}
}

func TestBlockCIDRBeatsAllowlist(t *testing.T) {
	cfg := loopback(0, 0)
	cfg.Allowlist = []string{"127.0.0.1"}
	cfg.Blocklist = []string{"127.0.0.0/8", "198.51.100.0/24"}
	s, c, _ := start(t, cfg, nil)
	sendUDP(t, udpAddr(t, s), "<13>allowed but blocked")
	waitFor(t, "the refusal", func() bool { return s.Status().Rejected == 1 })
	if c.count() != 0 {
		t.Error("the allowlist won over the blocklist")
	}
	if r, _ := rejectedFrom(s, "127.0.0.1"); r.Reason != RejectBlocked {
		t.Errorf("reason %q, want blocked (checked before the allowlist)", r.Reason)
	}
	if e, ok := s.BlockedBy(netip.MustParseAddr("127.0.0.1")); !ok || e != "127.0.0.0/8" {
		t.Errorf("BlockedBy %q %v", e, ok)
	}
	// Unblock: the allowlist applies again.
	cfg.Blocklist = []string{"198.51.100.0/24"}
	if err := s.Reconfigure(cfg); err != nil {
		t.Fatal(err)
	}
	sendUDP(t, udpAddr(t, s), "<13>unblocked")
	if got := c.wait(t, 1); string(got[0].Raw) != "<13>unblocked" {
		t.Errorf("got %q", got[0].Raw)
	}
	if _, ok := s.BlockedBy(netip.MustParseAddr("127.0.0.1")); ok {
		t.Error("still blocked after Reconfigure")
	}
	cfg.Blocklist = []string{"nope"}
	if err := s.Reconfigure(cfg); err == nil {
		t.Error("bad blocklist accepted")
	}
}

func TestBlockClosesOpenConnection(t *testing.T) {
	cfg := loopback(0, 0)
	s, c, _ := start(t, cfg, nil)
	conn, err := net.Dial("tcp", tcpAddr(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("<13>before\n<13>partial, then blocked"))
	c.wait(t, 1)

	cfg.Blocklist = []string{"127.0.0.1"}
	if err := s.Reconfigure(cfg); err != nil {
		t.Fatal(err)
	}
	if !closedByServer(conn, 5*time.Second) {
		t.Fatal("open connection from a newly blocked sender wasn't closed")
	}
	waitFor(t, "the connection to go", func() bool { return s.Status().TCPConns == 0 })
	time.Sleep(50 * time.Millisecond)
	if got := c.wait(t, 1); len(got) != 1 {
		t.Errorf("stored after the block: %q", got[len(got)-1].Raw)
	}
	r, ok := rejectedFrom(s, "127.0.0.1")
	if !ok || r.Count24h != 1 || r.Reason != RejectBlocked {
		t.Errorf("rejected %+v", r)
	}

	// Unblock: a new connection works again.
	cfg.Blocklist = nil
	s.Reconfigure(cfg)
	again, err := net.Dial("tcp", tcpAddr(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	again.Write([]byte("<13>after unblock\n"))
	if got := c.wait(t, 2); string(got[1].Raw) != "<13>after unblock" {
		t.Errorf("got %q", got[1].Raw)
	}
}

// A connection blocked while open is closed at its next message even if the
// Reconfigure sweep didn't see it (the per-message check).
func TestBlockAtNextMessage(t *testing.T) {
	s, c, _ := start(t, loopback(0, 0), nil)
	conn, err := net.Dial("tcp", tcpAddr(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("<13>first\n"))
	c.wait(t, 1)
	// Swap the lists without the sweep, as a race with Reconfigure would.
	al, _ := newAllowlist(config.Listeners{Blocklist: []string{"127.0.0.1"}})
	s.allow.Store(al)
	conn.Write([]byte("<13>second\n<13>third\n"))
	if !closedByServer(conn, 5*time.Second) {
		t.Fatal("not closed at the next message")
	}
	time.Sleep(50 * time.Millisecond)
	if n := c.count(); n != 1 {
		t.Errorf("%d messages stored", n)
	}
	if r, _ := rejectedFrom(s, "127.0.0.1"); r.Count24h != 1 || r.Reason != RejectBlocked {
		t.Errorf("rejected %+v", r)
	}
}
