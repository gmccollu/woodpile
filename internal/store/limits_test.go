package store

import (
	"errors"
	"fmt"
	"math/rand"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodshed/internal/index"
	"github.com/gmccollu/woodshed/internal/model"
	"github.com/gmccollu/woodshed/internal/store/logfile"
)

// v6 returns distinct senders from one /64 cycling its interface IDs (the review's
// attack), as a spoofer would.
func v6(i int) netip.Addr {
	b := netip.MustParseAddr("2001:db8:1:2::").As16()
	b[12], b[13], b[14], b[15] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
	return netip.AddrFrom16(b)
}

// spread returns distinct senders each in its own /48 (2001:db8:NNNN::1), so the
// per-network buckets don't apply; only the global bucket and the cap do.
func spread(i int) netip.Addr {
	b := netip.MustParseAddr("2001:db8::1").As16()
	b[4], b[5] = byte(i>>8), byte(i)
	b[6], b[7] = byte(i>>24), byte(i>>16) // past 65,536: still distinct, other /48s
	return netip.AddrFrom16(b)
}

func heapInuse() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapInuse
}

func countLines(t *testing.T, dir string) int {
	t.Helper()
	files, _, err := logfile.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range files {
		sum, err := logfile.ScanFile(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		n += int(sum.Lines)
	}
	return n
}

// TestOneNetworkCyclingAddresses: the review's attack, one IPv6 host cycling its
// /64 through 50,000 addresses. Its network's bucket admits 256 and refuses the rest.
func TestOneNetworkCyclingAddresses(t *testing.T) {
	e := newEnv(t, nil)
	accepted := 0
	for i := 0; i < 50000; i++ {
		if e.s.Write(model.Message{Received: e.clock.Now(), Sender: v6(i), Raw: []byte("x")}) == nil {
			accepted++
		}
	}
	if accepted != DefaultPrefixBurst {
		t.Fatalf("one /64 got %d senders in", accepted)
	}
	e.maintain()
	if u := e.s.Usage(); u.DeviceLimitReason != "prefix" || u.RefusedMsgs != 50000-DefaultPrefixBurst {
		t.Fatalf("usage %+v", u)
	}
	evs := eventsOf(t, e.idx, index.KindDeviceLimit)
	if len(evs) != 1 || !strings.Contains(evs[0].Text, "one network") || !strings.Contains(evs[0].Text, "allowlist") {
		t.Fatalf("events %+v", evs)
	}
}

// TestFiftyThousandNewSenders: 50,000 one-message senders, each in its own network,
// with the default limits. The global burst admits 5,000; the rest are refused and
// counted; the heap stays small.
func TestFiftyThousandNewSenders(t *testing.T) {
	e := newEnv(t, nil)
	before := heapInuse()
	accepted, refused := 0, 0
	for i := 0; i < 50000; i++ {
		err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: spread(i), Raw: []byte("<134>spoofed hello")})
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrDeviceLimit):
			refused++
		default:
			t.Fatal(err)
		}
	}
	grew := int64(heapInuse()) - int64(before)
	t.Logf("accepted %d, refused %d, heap grew %d KiB", accepted, refused, grew>>10)
	if accepted != DefaultNewDeviceBurst || refused != 50000-DefaultNewDeviceBurst {
		t.Fatalf("accepted %d refused %d", accepted, refused)
	}
	if grew > 200<<20 {
		t.Fatalf("heap grew %d MiB", grew>>20)
	}
	e.maintain()
	u := e.s.Usage()
	const burst, refusedN = DefaultNewDeviceBurst, 50000 - DefaultNewDeviceBurst
	if u.RefusedMsgs != refusedN || u.Dropped != refusedN || !u.DeviceLimited || u.DeviceLimitReason != "rate" ||
		u.Devices != burst || u.MaxDevices != DefaultMaxDevices {
		t.Fatalf("usage %+v", u)
	}
	if c, _ := e.idx.TotalCounts(bg, time.Time{}, time.Time{}); c.Dropped != refusedN || c.Msgs != burst {
		t.Fatalf("stats %+v", c)
	}
	if devs, _ := e.idx.ListDevices(bg); len(devs) != burst {
		t.Fatalf("%d device rows", len(devs))
	}
	evs := eventsOf(t, e.idx, index.KindDeviceLimit)
	if len(evs) != 1 || evs[0].Level != index.LevelWarn || !strings.Contains(evs[0].Text, fmt.Sprintf("refused %d messages", refusedN)) {
		t.Fatalf("events %+v", evs)
	}
	if n := countLines(t, e.dir); n != burst {
		t.Fatalf("%d lines on disk", n)
	}
	// The bucket refills at 200 a minute; the event repeats at most once a minute.
	e.clock.Add(time.Minute)
	accepted = 0
	for i := 50000; i < 50500; i++ {
		if e.s.Write(model.Message{Received: e.clock.Now(), Sender: spread(i), Raw: []byte("later")}) == nil {
			accepted++
		}
	}
	if accepted != int(DefaultNewDevicesPerMinute) {
		t.Fatalf("after a minute %d accepted", accepted)
	}
	e.maintain()
	if n := len(eventsOf(t, e.idx, index.KindDeviceLimit)); n != 2 {
		t.Fatalf("%d device_limit events", n)
	}
	e.clock.Add(DeviceLimitWindow)
	if e.s.Usage().DeviceLimited {
		t.Fatal("banner still up after the window")
	}
}

// TestAcceptedSendersHeap raises the limits so every sender is accepted: small
// initial buffers keep each live sender to a few KiB until the flush. 10,000 senders
// keep the test quick (each creates a file); at <= 4 KiB each, 50,000 stay under 200 MiB.
func TestAcceptedSendersHeap(t *testing.T) {
	const n = 10000
	e := newEnv(t, func(o *Options) { o.MaxDevices = 100000; o.NewDeviceBurst = 100000 })
	before := heapInuse()
	for i := 0; i < n; i++ {
		if err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: spread(i), Raw: []byte("<134>hello")}); err != nil {
			t.Fatal(err)
		}
	}
	grew := int64(heapInuse()) - int64(before)
	t.Logf("%d live senders: heap grew %d MiB (%d B each)", n, grew>>20, grew/n)
	if grew/n > 4096 {
		t.Fatalf("%d bytes per live sender", grew/n)
	}
	if err := e.s.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := countLines(t, e.dir); got != n {
		t.Fatalf("%d lines on disk", got)
	}
}

func TestDeviceCapAndKnownSenders(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.MaxDevices = 3; o.NewDeviceBurst = 1 })
	a, b, c, d := ip(1), ip(2), ip(3), ip(4)
	e.write(a, "a takes the only token")
	if err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: b, Raw: []byte("b")}); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("b: %v", err)
	}
	if r := e.s.Usage().DeviceLimitReason; r != "rate" {
		t.Fatalf("reason %q", r)
	}
	// Close a's day so it has logs but no active file, then restart: a is known from
	// disk, so it's admitted without a token; new senders still need one.
	e.clock.Add(24 * time.Hour)
	e.maintain()
	e.reopen()
	e.write(c, "c takes the refilled token")
	if err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: d, Raw: []byte("d")}); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("d: %v", err)
	}
	e.write(a, "a is known")
	// The cap: three live devices; a fourth is refused even with tokens.
	e.s.devMu.Lock()
	e.s.global.tokens = 10
	e.s.devMu.Unlock()
	e.write(b, "b fits under the cap")
	if err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: d, Raw: []byte("d")}); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("d over the cap: %v", err)
	}
	if u := e.s.Usage(); u.DeviceLimitReason != "cap" || u.Devices != 3 {
		t.Fatalf("usage %+v", u)
	}
}

func TestIdleEviction(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.IdleEvict = 2 * time.Minute })
	e.clock.Set(time.Date(2026, 11, 10, 23, 59, 30, 0, time.UTC))
	for i := 0; i < 100; i++ {
		e.write(ip(i), "before midnight")
	}
	e.clock.Set(time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC))
	e.maintain() // closes every day-10 file
	if n := e.s.Usage().Devices; n != 100 {
		t.Fatalf("%d devices right after the close", n)
	}
	e.clock.Add(3 * time.Minute)
	e.write(ip(0), "still active today")
	e.maintain()
	if n := e.s.Usage().Devices; n != 1 {
		t.Fatalf("%d devices after the idle period, want 1", n)
	}
	// An evicted device comes back on its next message, and a late message for its
	// closed day goes to today's file instead of reopening the closed one.
	late := time.Date(2026, 11, 10, 23, 59, 0, 0, time.UTC)
	if err := e.s.Write(model.Message{Received: late, Sender: ip(5), Raw: []byte("late")}); err != nil {
		t.Fatal(err)
	}
	e.write(ip(5), "back")
	e.maintain()
	if got := strings.Join(e.files(ip(5)), ","); got != "2026-11-10.log.gz,2026-11-11.log" {
		t.Fatalf("files %s", got)
	}
	if got := rawStrings(readLines(t, e.dir, "192.0.2.5")); strings.Join(got, "|") != "before midnight|late|back" {
		t.Fatalf("lines %q", got)
	}
	if devs, _ := e.idx.ListDevices(bg); len(devs) != 100 {
		t.Fatalf("index rows %d", len(devs))
	}
}

// TestRetentionFairness: one device floods, five are quiet. The storage cap deletes
// the flooder's oldest files and the quiet devices keep all of theirs; the flood is
// reported; and under low disk nobody loses their guaranteed days.
func TestRetentionFairness(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.MinFreeBytes = 1 << 20; o.MinFreePercent = 1 })
	flood := ip(100)
	quiet := []netip.Addr{ip(1), ip(2), ip(3), ip(4), ip(5)}
	r := rand.New(rand.NewSource(7))
	text := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte('a' + r.Intn(26))
		}
		return string(b)
	}
	const days = 10
	end := e.clock.Now()
	e.clock.Set(end.Add(-(days - 1) * 24 * time.Hour))
	for day := 0; day < days; day++ {
		for i := 0; i < 60; i++ {
			e.write(flood, fmt.Sprintf("flood %d %s", day, text(20000)))
		}
		for _, q := range quiet {
			e.write(q, fmt.Sprintf("quiet %d %s", day, text(500)))
		}
		e.maintain()
		if day < days-1 {
			e.clock.Add(24 * time.Hour)
		}
	}
	e.maintain()
	evs := eventsOf(t, e.idx, index.KindFlood)
	if len(evs) == 0 {
		t.Fatal("no flood event")
	}
	for _, ev := range evs {
		if ev.DeviceID != "192.0.2.100" || ev.Level != index.LevelWarn {
			t.Fatalf("flood event %+v", ev)
		}
	}
	// Cap at 70% of what's on disk: only the flooder loses files.
	e.s.SetLimits(Limits{RetentionBytes: duLogs(e.dir) * 7 / 10})
	e.maintain()
	for _, q := range quiet {
		if n := len(e.files(q)); n != days {
			t.Fatalf("%v kept %d of %d files", q, n, days)
		}
	}
	fl := e.files(flood)
	if len(fl) >= days || fl[len(fl)-1] != "2026-11-10.log" {
		t.Fatalf("flooder files %v", fl)
	}
	hist, _ := e.idx.Deletions(bg, time.Time{}, time.Time{}, 0)
	for _, h := range hist {
		if h.DeviceID != "192.0.2.100" || h.Reason != index.ReasonCap {
			t.Fatalf("deleted %+v", h)
		}
	}
	// Under low disk with the flooder within its share, the order is oldest-first
	// across everyone, but no established device loses its guaranteed days (today
	// and yesterday) while anything older is left.
	e.disk.total = 1 << 30
	used := uint64(duLogs(e.dir))
	e.disk.setOther(1<<30 - used - (10<<20 - 100<<10)) // 100 KiB under the 10 MiB reserve
	e.maintain()
	for _, q := range append(quiet, flood) {
		if got := daysOf(e.files(q)); !strings.HasSuffix(got, "2026-11-09,2026-11-10") {
			t.Fatalf("low disk: %v kept days %s", q, got)
		}
	}
	hist, _ = e.idx.Deletions(bg, time.Time{}, time.Time{}, 0)
	if hist[0].Reason != index.ReasonLowDisk {
		t.Fatalf("low-disk deletion %+v", hist[0])
	}
}

// TestOneNetworkCantDrainTheBucket is the review's N5 case: an attacker in one
// network sending as 240 new addresses a minute (4 a second) for an hour doesn't
// keep 10 real new devices in other networks out. An IPv4 /24 has only 256
// addresses, so it's capped at 256 senders anyway; an IPv6 /48 has endless ones, and
// its bucket holds it to 256 plus 20 a minute. (Random sources across many networks
// can still drain the global bucket; the sender allowlist is the answer to that.)
func TestOneNetworkCantDrainTheBucket(t *testing.T) {
	v4 := func(i int) netip.Addr { return netip.AddrFrom4([4]byte{203, 0, 113, byte(i)}) }
	v6net := func(i int) netip.Addr {
		b := netip.MustParseAddr("2001:db8:ffff::").As16()
		b[8], b[9], b[10], b[11] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
		return netip.AddrFrom16(b)
	}
	for _, c := range []struct {
		name     string
		attacker func(int) netip.Addr
	}{{"IPv4 /24", v4}, {"IPv6 /48", v6net}} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, nil)
			n, real := 0, 0
			for minute := 0; minute < 60; minute++ {
				for i := 0; i < 240; i++ {
					e.clock.Add(250 * time.Millisecond)
					e.s.Write(model.Message{Received: e.clock.Now(), Sender: c.attacker(n), Raw: []byte("spoof")})
					n++
				}
				if minute%6 == 5 {
					if err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: spread(1000 + real), Raw: []byte("real")}); err != nil {
						t.Fatalf("minute %d: real device %d refused: %v", minute, real, err)
					}
					real++
				}
				e.maintain()
			}
			seen := map[netip.Addr]bool{}
			admitted := 0
			for i := 0; i < n; i++ {
				a := c.attacker(i)
				if seen[a] {
					continue
				}
				seen[a] = true
				if _, err := os.Stat(filepath.Join(e.dir, "logs", model.DeviceID(a))); err == nil {
					admitted++
				}
			}
			if max := DefaultPrefixBurst + 60*int(DefaultPrefixPerMinute) + 1; admitted > max {
				t.Fatalf("attacker got %d senders in, more than %d", admitted, max)
			}
			t.Logf("attacker: %d addresses tried, %d admitted; %d real devices admitted", n, admitted, real)
			if c.name == "IPv6 /48" {
				if u := e.s.Usage(); u.DeviceLimitReason != "prefix" || u.RefusedMsgs == 0 {
					t.Fatalf("usage %+v", u)
				}
			}
		})
	}
}
