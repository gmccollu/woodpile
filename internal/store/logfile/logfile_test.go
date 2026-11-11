package logfile

import (
	"bytes"
	"compress/gzip"
	"io"
	"math/rand"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var ip1 = netip.MustParseAddr("192.0.2.1")

func TestEscapeRoundTrip(t *testing.T) {
	cases := []string{
		"", "plain", "a\nb", "a\r\nb", `back\slash`, `\n literal`, "\\", "\\\\", "\n", "\r",
		"trailing\\", "nul\x00byte", "\xff\xfe invalid utf8", "tab\there", `\r\n\\`, "\\\n\\\r",
	}
	for _, c := range cases {
		esc := AppendEscaped(nil, []byte(c))
		if bytes.ContainsAny(esc, "\r\n") {
			t.Errorf("%q escaped to %q, which still has CR/LF", c, esc)
		}
		if got := string(Unescape(nil, esc)); got != c {
			t.Errorf("round trip %q -> %q -> %q", c, esc, got)
		}
		if n := UnescapedLen(esc); n != len(c) {
			t.Errorf("UnescapedLen(%q) = %d, want %d", esc, n, len(c))
		}
	}
	r := rand.New(rand.NewSource(1))
	alphabet := []byte("ab\\\r\nnr\x00 ")
	for i := 0; i < 20000; i++ {
		b := make([]byte, r.Intn(40))
		for j := range b {
			b[j] = alphabet[r.Intn(len(alphabet))]
		}
		esc := AppendEscaped(nil, b)
		if got := Unescape(nil, esc); !bytes.Equal(got, b) {
			t.Fatalf("round trip %q -> %q -> %q", b, esc, got)
		}
	}
}

func TestEscapeExamples(t *testing.T) {
	if got := string(AppendEscaped(nil, []byte("a\r\nb\\c"))); got != `a\r\nb\\c` {
		t.Fatalf("got %q", got)
	}
	// Unknown sequences and a lone trailing backslash (a torn line) are kept.
	if got := string(Unescape(nil, []byte(`x\ty\`))); got != `x\ty\` {
		t.Fatalf("got %q", got)
	}
}

func FuzzEscapeRoundTrip(f *testing.F) {
	for _, s := range []string{"", "a\nb", `\\n`, "\r\n\\"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		line := AppendLine(nil, time.Date(2026, 11, 10, 20, 41, 7, 112e6, time.UTC), ip1, raw)
		if bytes.IndexByte(line[:len(line)-1], '\n') >= 0 {
			t.Fatalf("line has an embedded newline: %q", line)
		}
		l, err := ParseLine(line)
		if err != nil {
			t.Fatal(err)
		}
		if got := l.Raw(); !bytes.Equal(got, raw) {
			t.Fatalf("raw %q came back as %q", raw, got)
		}
	})
}

func TestLineFormat(t *testing.T) {
	rec := time.Date(2026, 11, 10, 20, 41, 7, 112_999_999, time.UTC)
	line := AppendLine(nil, rec, ip1, []byte("<134>Nov 10 20:41:07 host app: hi"))
	want := "2026-11-10T20:41:07.112Z 192.0.2.1 <134>Nov 10 20:41:07 host app: hi\n"
	if string(line) != want {
		t.Fatalf("got %q, want %q", line, want)
	}
	l, err := ParseLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Received.Equal(rec.Truncate(time.Millisecond)) || l.Sender != ip1 || string(l.Escaped) != "<134>Nov 10 20:41:07 host app: hi" {
		t.Fatalf("parsed %+v", l)
	}
	// IPv6, a 4-in-6 mapped sender (written unmapped), an empty message, CRLF endings.
	v6 := netip.MustParseAddr("2001:db8::5")
	if l, err := ParseLine(AppendLine(nil, rec, v6, nil)); err != nil || l.Sender != v6 || len(l.Escaped) != 0 {
		t.Fatalf("v6 empty: %+v %v", l, err)
	}
	mapped := netip.MustParseAddr("::ffff:198.51.100.7")
	if l, _ := ParseLine(AppendLine(nil, rec, mapped, []byte("x"))); l.Sender.String() != "198.51.100.7" {
		t.Fatalf("mapped sender written as %v", l.Sender)
	}
	if l, err := ParseLine([]byte("2026-11-10T20:41:07.112Z 192.0.2.1 hi\r\n")); err != nil || string(l.Escaped) != "hi" {
		t.Fatalf("crlf: %+v %v", l, err)
	}
	// A hand-edited line with another RFC 3339 form still parses.
	if l, err := ParseLine([]byte("2026-11-10T21:41:07+01:00 192.0.2.1 hi")); err != nil || !l.Received.Equal(time.Date(2026, 11, 10, 20, 41, 7, 0, time.UTC)) {
		t.Fatalf("rfc3339: %+v %v", l, err)
	}
	for _, bad := range []string{"", "garbage", "2026-11-10T20:41:07.112Z", "2026-11-10T20:41:07.112Z not-an-ip x",
		"2026-02-31T20:41:07.112Z 192.0.2.1 x", "\x00\x00\x00"} {
		if _, err := ParseLine([]byte(bad)); err == nil {
			t.Errorf("ParseLine(%q) succeeded", bad)
		}
	}
}

func TestTimeFormatMatchesStdlib(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 10000; i++ {
		tt := time.Unix(r.Int63n(4e9), r.Int63n(1e9)).UTC()
		want := tt.Format("2006-01-02T15:04:05.000Z07:00")
		if got := string(AppendTime(nil, tt)); got != want {
			t.Fatalf("AppendTime(%v) = %q, want %q", tt, got, want)
		}
		back, err := ParseTime([]byte(want))
		if err != nil || !back.Equal(tt.Truncate(time.Millisecond)) {
			t.Fatalf("ParseTime(%q) = %v, %v", want, back, err)
		}
		if DayOf(tt) != tt.Format(DayLayout) {
			t.Fatalf("DayOf(%v) = %s", tt, DayOf(tt))
		}
	}
}

func TestNames(t *testing.T) {
	cases := map[string]Name{
		"2026-11-10.log":        {Day: "2026-11-10"},
		"2026-11-10.log.gz":     {Day: "2026-11-10", Compressed: true},
		"2026-11-08.001.log.gz": {Day: "2026-11-08", Part: 1, Compressed: true},
		"2026-11-08.042.log":    {Day: "2026-11-08", Part: 42},
		"2026-11-08.1234.log":   {Day: "2026-11-08", Part: 1234},
	}
	for s, want := range cases {
		got, ok := ParseName(s)
		if !ok || got != want {
			t.Errorf("ParseName(%q) = %+v, %v", s, got, ok)
		}
		if got.String() != s {
			t.Errorf("%+v.String() = %q, want %q", got, got.String(), s)
		}
	}
	for _, s := range []string{"2026-11-10.log.gz.tmp", "notes.txt", "2026-13-01.log", "2026-11-10.000.log",
		"2026-11-10.1.log", "2026-11-10.x01.log", "2026-11-10.log.zip"} {
		if _, ok := ParseName(s); ok {
			t.Errorf("ParseName(%q) accepted", s)
		}
	}
	// ls order is time order within a day.
	names := []string{"2026-11-08.log.gz", "2026-11-08.002.log.gz", "2026-11-08.001.log.gz", "2026-11-09.001.log"}
	sorted := append([]string(nil), names...)
	sortStrings(sorted)
	want := []string{"2026-11-08.001.log.gz", "2026-11-08.002.log.gz", "2026-11-08.log.gz", "2026-11-09.001.log"}
	if strings.Join(sorted, ",") != strings.Join(want, ",") {
		t.Fatalf("sorted %v", sorted)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func TestCheckpointRule(t *testing.T) {
	var c Checkpointer
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	if cp, ok := c.Observe(0, 0, t0); !ok || cp.Offset != 0 || !cp.Received.Equal(t0) {
		t.Fatalf("first line: %+v %v", cp, ok)
	}
	// Two minutes later but under the minimum bytes: no checkpoint.
	if _, ok := c.Observe(1000, 1, t0.Add(2*time.Minute)); ok {
		t.Fatal("checkpoint below CheckpointMinBytes")
	}
	// Past the minimum bytes with over a minute elapsed: checkpoint.
	cp, ok := c.Observe(CheckpointMinBytes, 2, t0.Add(2*time.Minute))
	if !ok || cp.Line != 2 {
		t.Fatalf("time checkpoint: %+v %v", cp, ok)
	}
	// An out-of-order line doesn't lower the high-water mark.
	c.Observe(CheckpointMinBytes+10, 3, t0)
	if !c.HighWater().Equal(t0.Add(2 * time.Minute)) {
		t.Fatalf("hwm %v", c.HighWater())
	}
	// 1 MiB later in the same second: checkpoint.
	if cp, ok := c.Observe(CheckpointMinBytes+CheckpointBytes, 4, t0.Add(2*time.Minute)); !ok || cp.Line != 4 {
		t.Fatalf("size checkpoint: %+v %v", cp, ok)
	}
}

func TestScanAndResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-11-10.log")
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	var buf []byte
	var cps []Checkpoint
	var c Checkpointer
	lines := 0
	big := bytes.Repeat([]byte("x"), 300<<10) // longer than the scanner's buffer
	for i := 0; i < 20000; i++ {
		raw := []byte("message with some text in it\n second line")
		if i == 7 {
			raw = big
		}
		rec := t0.Add(time.Duration(i) * 50 * time.Millisecond)
		if cp, ok := c.Observe(int64(len(buf)), int64(lines), rec); ok {
			cps = append(cps, cp)
		}
		buf = AppendLine(buf, rec, ip1, raw)
		lines++
	}
	half := len(buf)
	buf = append(buf, "garbage line\n"...)
	lines++
	buf = append(buf, "2026-11-10T00:00:00.000Z 192.0.2.1 torn"...)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	sum, err := ScanFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Lines != int64(lines) || sum.BadLines != 1 || sum.TornBytes != 39 || sum.RawSize != int64(len(buf)) {
		t.Fatalf("summary %+v", sum)
	}
	if !sum.First.Equal(t0) || sum.Sender != ip1 {
		t.Fatalf("first %v sender %v", sum.First, sum.Sender)
	}
	if len(sum.Checkpoints) != len(cps) || len(cps) < 10 {
		t.Fatalf("got %d checkpoints, writer had %d", len(sum.Checkpoints), len(cps))
	}
	for i := range cps {
		if sum.Checkpoints[i] != cps[i] {
			t.Fatalf("checkpoint %d: scan %+v, writer %+v", i, sum.Checkpoints[i], cps[i])
		}
	}
	// Resuming from a checkpoint reproduces the rest of the sequence.
	k := len(cps) / 2
	f, _ := os.Open(path)
	defer f.Close()
	f.Seek(cps[k].Offset, io.SeekStart)
	cpr := ResumeCheckpointer(cps[k], cps[k].Received)
	rs, err := Scan(f, ScanOptions{Start: cps[k].Offset, StartLine: cps[k].Line, Checkpointer: &cpr})
	if err != nil {
		t.Fatal(err)
	}
	if rs.Lines != sum.Lines || len(rs.Checkpoints) != len(cps)-k-1 || rs.Checkpoints[0] != cps[k+1] {
		t.Fatalf("resume: lines %d, %d checkpoints", rs.Lines, len(rs.Checkpoints))
	}

	// OpenAt on plain and gzip files lands on the checkpoint's line.
	gzPath := path + ".gz"
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	zw.Write(buf[:half])
	zw.Close()
	os.WriteFile(gzPath, zb.Bytes(), 0o600)
	for _, p := range []string{path, gzPath} {
		rc, err := OpenAt(p, cps[3].Offset)
		if err != nil {
			t.Fatal(err)
		}
		head := make([]byte, TimeLen)
		io.ReadFull(rc, head)
		rc.Close()
		if string(head) != string(AppendTime(nil, cps[3].Received)) {
			t.Fatalf("%s: OpenAt landed on %q", p, head)
		}
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	dev := filepath.Join(dir, LogsDir, "192.0.2.1")
	os.MkdirAll(dev, 0o700)
	for _, n := range []string{"2026-11-10.log", "2026-11-09.log.gz", "2026-11-09.001.log.gz", "x.txt", "2026-11-08.log.gz.tmp"} {
		os.WriteFile(filepath.Join(dev, n), []byte("x"), 0o600)
	}
	files, temps, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, f := range files {
		rels = append(rels, f.Rel)
	}
	want := "logs/192.0.2.1/2026-11-09.001.log.gz,logs/192.0.2.1/2026-11-09.log.gz,logs/192.0.2.1/2026-11-10.log"
	if strings.Join(rels, ",") != want || len(temps) != 1 {
		t.Fatalf("files %v temps %v", rels, temps)
	}
	if f, _, _ := List(filepath.Join(dir, "missing")); f != nil {
		t.Fatal("missing dir listed files")
	}
}
