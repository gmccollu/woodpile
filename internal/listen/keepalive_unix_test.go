//go:build !windows

package listen

import (
	"net"
	"syscall"
	"testing"
)

// Accepted TCP connections have keepalives on, so a connection to a sender that
// rebooted or vanished is closed within minutes, whatever the idle timeout.
func TestAcceptedConnectionsKeepAlive(t *testing.T) {
	s, c, _ := start(t, loopback(0, 0), nil)
	cn, err := net.Dial("tcp", tcpAddr(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Close()
	cn.Write([]byte("<13>hello\n"))
	c.wait(t, 1)
	s.mu.Lock()
	tl := s.tcp
	s.mu.Unlock()
	tl.mu.Lock()
	var server net.Conn
	for tc := range tl.conns {
		server = tc.c
	}
	tl.mu.Unlock()
	raw, err := server.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var on int
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		on, gerr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_KEEPALIVE)
	}); err != nil || gerr != nil {
		t.Fatal(err, gerr)
	}
	if on == 0 {
		t.Error("SO_KEEPALIVE is off on an accepted connection")
	}
	if !tcpKeepAlive.Enable || tcpKeepAlive.Idle <= 0 || tcpKeepAlive.Interval <= 0 || tcpKeepAlive.Count <= 0 {
		t.Errorf("tcpKeepAlive %+v", tcpKeepAlive)
	}
}
