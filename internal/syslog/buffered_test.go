package syslog

import (
	"io"
	"testing"
)

func TestBuffered(t *testing.T) {
	// One read delivers everything: after each message, Buffered counts the rest,
	// without the junk Next skips.
	r := NewStreamReader(&chunks{parts: []string{"<13>a\n<13>bb\r\n\n\x0015 <13>octet frame\n<13>par"}}, 0)
	if n := r.Buffered(); n != 0 {
		t.Errorf("before the first read: %d", n)
	}
	want := []struct {
		msg      string
		buffered int
	}{
		{"<13>a", len("<13>bb\r\n\n\x0015 <13>octet frame\n<13>par")},
		{"<13>bb", len("15 <13>octet frame\n<13>par")}, // the "\n\x00" junk isn't counted
		{"<13>octet frame", len("<13>par")},            // nor the LF after the frame
		{"<13>par", 0},
	}
	for i, w := range want {
		msg, _, err := r.Next()
		if err != nil || string(msg) != w.msg {
			t.Fatalf("[%d] %q %v, want %q", i, msg, err, w.msg)
		}
		if n := r.Buffered(); n != w.buffered {
			t.Errorf("[%d] after %q: Buffered %d, want %d", i, msg, n, w.buffered)
		}
	}
	if _, _, err := r.Next(); err != io.EOF {
		t.Errorf("end: %v", err)
	}

	// Only junk after a message: nothing of the next message has arrived.
	r = NewStreamReader(&chunks{parts: []string{"<13>a\n\r\n\x00", "<13>b\n"}}, 0)
	if msg, _, _ := r.Next(); string(msg) != "<13>a" || r.Buffered() != 0 {
		t.Errorf("junk only: %q, Buffered %d", msg, r.Buffered())
	}
	if msg, _, _ := r.Next(); string(msg) != "<13>b" || r.Buffered() != 0 {
		t.Errorf("second: %q, Buffered %d", msg, r.Buffered())
	}
}

// chunks returns one part per Read.
type chunks struct{ parts []string }

func (c *chunks) Read(p []byte) (int, error) {
	if len(c.parts) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.parts[0])
	if c.parts[0] = c.parts[0][n:]; c.parts[0] == "" {
		c.parts = c.parts[1:]
	}
	return n, nil
}
