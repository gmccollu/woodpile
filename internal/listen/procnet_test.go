package listen

import (
	"encoding/binary"
	"strings"
	"testing"
)

const procUDP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  123: 00000000:0202 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 18111 2 0000000000000000 0
  456: 0100007F:15AA 00000000:0000 07 00000000:00000000 00:00000000 00000000  1000        0 99887 2 0000000000000000 1234
`

const procUDP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
   12: 00000000000000000000000000000000:0202 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000   999        0 55501 2 0000000000000000 77
`

const procTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0202 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 4242 1 0000000000000000 100 0 0 10 0
   1: 0100007F:0202 0100007F:D431 01 00000000:00000000 00:00000000 00000000     0        0 4343 1 0000000000000000 20 4 30 10 -1
`

func TestProcNetUDPDrops(t *testing.T) {
	if v, ok := procNetUDPDrops(strings.NewReader(procUDP), 99887); !ok || v != 1234 {
		t.Errorf("v4: %d %v", v, ok)
	}
	if v, ok := procNetUDPDrops(strings.NewReader(procUDP6), 55501); !ok || v != 77 {
		t.Errorf("v6: %d %v", v, ok)
	}
	if _, ok := procNetUDPDrops(strings.NewReader(procUDP), 1); ok {
		t.Error("found a missing inode")
	}
}

func TestProcNetInodes(t *testing.T) {
	if got := procNetInodes(strings.NewReader(procUDP), 514, false); len(got) != 1 || got[0] != 18111 {
		t.Errorf("udp 514: %v", got)
	}
	if got := procNetInodes(strings.NewReader(procUDP), 5546, false); len(got) != 1 || got[0] != 99887 {
		t.Errorf("udp 5546: %v", got)
	}
	if got := procNetInodes(strings.NewReader(procUDP6), 514, false); len(got) != 1 || got[0] != 55501 {
		t.Errorf("udp6: %v", got)
	}
	// Only the LISTEN socket, not the established one on the same local port.
	if got := procNetInodes(strings.NewReader(procTCP), 514, true); len(got) != 1 || got[0] != 4242 {
		t.Errorf("tcp: %v", got)
	}
}

func TestParseSocketLink(t *testing.T) {
	if v, ok := parseSocketLink("socket:[4242]"); !ok || v != 4242 {
		t.Errorf("%d %v", v, ok)
	}
	for _, bad := range []string{"pipe:[1]", "socket:[x]", "socket:[12", "/dev/null"} {
		if _, ok := parseSocketLink(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestParseLsof(t *testing.T) {
	cases := map[string]string{
		"p312\ncsyslogd\nf5\n":           "syslogd (pid 312)",
		"p312\ncsyslogd\np400\ncother\n": "syslogd (pid 312)",
		"p77\n":                          "pid 77",
		"":                               "",
	}
	for in, want := range cases {
		if got := parseLsof([]byte(in)); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// winTable builds an IP Helper table buffer of n rows of rowSize.
func winTable(rowSize int, rows ...func(r []byte)) []byte {
	buf := make([]byte, 4+len(rows)*rowSize)
	binary.LittleEndian.PutUint32(buf, uint32(len(rows)))
	for i, f := range rows {
		f(buf[4+i*rowSize : 4+(i+1)*rowSize])
	}
	return buf
}

func putPort(r []byte, off, port int) { r[off], r[off+1] = byte(port>>8), byte(port) }

func TestWinTables(t *testing.T) {
	for _, tc := range []struct {
		proto string
		v6    bool
	}{{"tcp", false}, {"tcp", true}, {"udp", false}, {"udp", true}} {
		size, portOff, pidOff, stateOff := winRowLayout(tc.proto, tc.v6)
		row := func(port int, pid uint32, state uint32) func([]byte) {
			return func(r []byte) {
				putPort(r, portOff, port)
				binary.LittleEndian.PutUint32(r[pidOff:], pid)
				if stateOff >= 0 {
					binary.LittleEndian.PutUint32(r[stateOff:], state)
				}
			}
		}
		buf := winTable(size, row(8514, 10, winTCPListen), row(514, 20, 5 /* established */), row(514, 312, winTCPListen))
		pid, ok := winTablePID(buf, size, portOff, pidOff, stateOff, 514)
		want := uint32(312)
		if tc.proto == "udp" {
			want = 20 // no state for UDP: the first row on the port
		}
		if !ok || pid != want {
			t.Errorf("%s v6=%v: pid %d ok %v, want %d", tc.proto, tc.v6, pid, ok, want)
		}
		if _, ok := winTablePID(buf, size, portOff, pidOff, stateOff, 1); ok {
			t.Errorf("%s v6=%v: found a free port", tc.proto, tc.v6)
		}
		// A short buffer (count larger than the rows present) is not read past its end.
		if _, ok := winTablePID(buf[:4+size], size, portOff, pidOff, stateOff, 9999); ok {
			t.Error("short buffer")
		}
	}
	if _, ok := winTablePID(nil, 12, 4, 8, -1, 514); ok {
		t.Error("nil buffer")
	}
}
