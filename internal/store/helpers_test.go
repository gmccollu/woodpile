package store

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"woodpile/internal/index"
	"woodpile/internal/model"
	"woodpile/internal/store/logfile"
)

var bg = context.Background()

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeDisk reports a volume of total bytes, of which other files use `other` bytes
// and the log files under dir use what they really use.
type fakeDisk struct {
	mu    sync.Mutex
	dir   string
	total uint64
	other uint64
}

func (f *fakeDisk) setOther(n uint64) {
	f.mu.Lock()
	f.other = n
	f.mu.Unlock()
}

func (f *fakeDisk) usage(string) (uint64, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	used := f.other + uint64(duLogs(f.dir))
	if used > f.total {
		return 0, f.total, nil
	}
	return f.total - used, f.total, nil
}

func duLogs(dataDir string) int64 {
	var n int64
	filepath.WalkDir(filepath.Join(dataDir, logfile.LogsDir), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

type env struct {
	t     *testing.T
	dir   string
	idx   *index.DB
	clock *fakeClock
	disk  *fakeDisk
	opts  Options
	s     *Store
}

var day0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

// newEnv opens a Manual store on a temp dir with a fake clock and a roomy fake disk.
func newEnv(t *testing.T, mod func(*Options)) *env {
	t.Helper()
	dir := t.TempDir()
	idx, err := index.Open(filepath.Join(dir, index.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	e := &env{t: t, dir: dir, idx: idx, clock: newClock(day0), disk: &fakeDisk{dir: dir, total: 1 << 40}}
	e.opts = Options{DataDir: dir, Index: idx, Clock: e.clock.Now, DiskUsage: e.disk.usage, Manual: true, Logger: quiet}
	if mod != nil {
		mod(&e.opts)
	}
	e.open()
	t.Cleanup(func() {
		if e.s != nil {
			e.s.Close()
		}
	})
	return e
}

func (e *env) open() {
	e.t.Helper()
	s, err := Open(e.opts)
	if err != nil {
		e.t.Fatal(err)
	}
	e.s = s
}

func (e *env) reopen() {
	e.t.Helper()
	if err := e.s.Close(); err != nil {
		e.t.Fatal(err)
	}
	e.open()
}

func (e *env) maintain() {
	e.t.Helper()
	if err := e.s.Maintain(); err != nil {
		e.t.Fatal(err)
	}
}

func ip(i int) netip.Addr { return netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}) }

func (e *env) write(addr netip.Addr, raw string) {
	e.t.Helper()
	if err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: addr, Raw: []byte(raw)}); err != nil {
		e.t.Fatal(err)
	}
}

// files lists base names under a device dir, sorted.
func (e *env) files(addr netip.Addr) []string {
	ents, _ := os.ReadDir(filepath.Join(e.dir, logfile.LogsDir, model.DeviceID(addr)))
	var out []string
	for _, x := range ents {
		out = append(out, x.Name())
	}
	sort.Strings(out)
	return out
}

// readLines returns every line of every log file of a device, in file name order.
func readLines(t *testing.T, dataDir, dev string) []logfile.Line {
	t.Helper()
	dir := filepath.Join(dataDir, logfile.LogsDir, dev)
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range ents {
		if _, ok := logfile.ParseName(x.Name()); ok {
			names = append(names, x.Name())
		}
	}
	sort.Strings(names)
	var out []logfile.Line
	for _, n := range names {
		rc, err := logfile.Open(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			l, err := logfile.ParseLine(sc.Bytes())
			if err != nil {
				t.Fatalf("%s: bad line %q", n, sc.Text())
			}
			l.Escaped = append([]byte(nil), l.Escaped...)
			out = append(out, l)
		}
		if err := sc.Err(); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		rc.Close()
	}
	return out
}

func rawStrings(ls []logfile.Line) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = string(l.Raw())
	}
	return out
}

func eventsOf(t *testing.T, idx *index.DB, kind index.Kind) []index.Event {
	t.Helper()
	evs, err := idx.Events(bg, time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []index.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// indexSnapshot renders files, checkpoints and devices without row IDs.
func indexSnapshot(t *testing.T, idx *index.DB) string {
	t.Helper()
	files, err := idx.Files(bg, "")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, f := range files {
		cps, _ := idx.Checkpoints(bg, f.ID)
		f.ID = 0
		fmt.Fprintf(&b, "%+v\n", f)
		for _, c := range cps {
			fmt.Fprintf(&b, "  %d %d %s\n", c.Offset, c.Line, c.Received.Format(time.RFC3339Nano))
		}
	}
	devs, _ := idx.ListDevices(bg)
	for _, d := range devs {
		fmt.Fprintf(&b, "dev %s %s %s %s %d %d %d\n", d.ID, d.IP, d.FirstSeen.Format(time.RFC3339Nano),
			d.LastHeard.Format(time.RFC3339Nano), d.TotalMsgs, d.TotalBytes, d.BytesOnDisk)
	}
	return b.String()
}
