package loadgen

import (
	"context"
	"net"
	"strings"
	"testing"
)

// TestSendMessageAndSource checks the Message and Source options: the text
// replaces the padding after the sequence tag, and the packets come from Source.
func TestSendMessageAndSource(t *testing.T) {
	for _, proto := range []string{"udp", "tcp"} {
		t.Run(proto, func(t *testing.T) {
			_, k, addr := startListener(t, proto)
			res, err := Send(context.Background(), SendOptions{
				Addr: addr, Proto: proto, Count: 3, RunID: "abcd1234",
				Message: "hello from the test", Source: "127.0.0.1",
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Sent != 3 {
				t.Fatalf("sent %d, want 3", res.Sent)
			}
			waitN(t, k, 3)
			k.mu.Lock()
			defer k.mu.Unlock()
			for i, m := range k.msgs {
				raw := string(m.Raw)
				want := SeqTag("abcd1234") + string(rune('0'+i)) + " hello from the test"
				if !strings.HasSuffix(raw, want) {
					t.Errorf("message %d = %q, want suffix %q", i, raw, want)
				}
				if m.Sender.String() != "127.0.0.1" {
					t.Errorf("message %d from %s, want 127.0.0.1", i, m.Sender)
				}
			}
		})
	}
}

func TestSendOptionErrors(t *testing.T) {
	for name, o := range map[string]SendOptions{
		"newline in message": {Addr: "127.0.0.1:9", Count: 1, Message: "a\nb"},
		"NUL in message":     {Addr: "127.0.0.1:9", Count: 1, Message: "a\x00b"},
		"bad source":         {Addr: "127.0.0.1:9", Count: 1, Source: "not-an-ip"},
	} {
		if _, err := Send(context.Background(), o); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// A source address this machine doesn't have fails at dial time, clearly.
	_, err := Send(context.Background(), SendOptions{Addr: "127.0.0.1:9", Proto: "udp", Count: 1, Source: "192.0.2.123"})
	if err == nil {
		t.Error("sending from an address this machine doesn't have: no error")
	} else if _, ok := err.(net.Error); !ok && !strings.Contains(err.Error(), "loadgen:") {
		t.Errorf("unexpected error shape: %v", err)
	}
}
