package syslog

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/gmccollu/woodpile/internal/model"
)

// benchMessages are typical messages; TestParseAllocs also checks each one.
var benchMessages = []struct{ name, raw string }{
	{"3164", "<86>Nov 10 20:41:07 edge-fw-01 sshd[4721]: Accepted publickey for admin from 198.51.100.7 port 52114 ssh2"},
	{"3164_pcttag", "<166>Nov 10 2026 20:41:07 edge-fw-01 : %FW-6-302013: Built outbound TCP connection 3109 for outside:198.51.100.7/443 (198.51.100.7/443) to inside:192.0.2.10/52114 (203.0.113.5/52114)"},
	{"3164_seq_ms_zone", "<189>123: edge-sw-01: *Nov 10 20:41:07.123 UTC: %SYS-5-CONFIG_I: Configured from console by vty0 (198.51.100.20)"},
	{"5424", `<165>1 2026-11-10T20:41:07.003Z edge-fw-01.example.net evntslog - ID47 [exampleSDID@32473 iut="3" eventSource="Application"] An application event log entry`},
	{"iso", "<13>2026-11-10T21:41:07.123+01:00 edge-fw-01 app[9]: config reloaded"},
	{"no_pri", "Nov 10 20:41:07 edge-fw-01 kernel: [ 1234.567890] eth0: link up"},
}

func BenchmarkParse(b *testing.B) {
	for _, bm := range benchMessages {
		raw := []byte(bm.raw)
		b.Run(bm.name, func(b *testing.B) {
			m := model.Message{Received: recv}
			b.SetBytes(int64(len(raw)))
			b.ReportAllocs()
			for b.Loop() {
				Parse(raw, &m)
			}
		})
	}
}

func BenchmarkStreamReader(b *testing.B) {
	line := benchMessages[0].raw
	var lf, oc bytes.Buffer
	for range 1000 {
		lf.WriteString(line)
		lf.WriteByte('\n')
		fmt.Fprintf(&oc, "%d %s", len(line), line)
	}
	for _, c := range []struct {
		name string
		data []byte
	}{{"lf", lf.Bytes()}, {"octet", oc.Bytes()}} {
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(c.data)))
			b.ReportAllocs()
			r := bytes.NewReader(c.data)
			for b.Loop() {
				r.Reset(c.data)
				sr := NewStreamReader(r, 0)
				n := 0
				for {
					_, _, err := sr.Next()
					if err == io.EOF {
						break
					}
					n++
				}
				if n != 1000 {
					b.Fatalf("got %d messages", n)
				}
			}
		})
	}
}
