package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/index"
	"github.com/gmccollu/woodpile/internal/model"
)

// TestCapCountsActiveSenders: 10,000 one-shot spoofed senders fill the cap and lock
// out a known device; after IdleClose and IdleEvict they hold no slots and the known
// device is admitted again, appending to its own file for today.
func TestCapCountsActiveSenders(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.NewDeviceBurst = 20000 }) // the cap, not the rate, is the limit here
	known := ip(7)
	e.write(known, "known, morning")
	e.clock.Add(DefaultIdleClose + time.Second)
	e.maintain() // known goes quiet: its file is parked and its state freed
	if n := e.s.Usage().Devices; n != 0 {
		t.Fatalf("%d devices after the known one went quiet", n)
	}
	before := heapInuse()
	for i := 0; i < DefaultMaxDevices; i++ {
		if err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: v6(i), Raw: []byte("spoofed")}); err != nil {
			t.Fatalf("spoofer %d: %v", i, err)
		}
	}
	if err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: known, Raw: []byte("locked out")}); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("known device at the cap: %v", err)
	}
	if u := e.s.Usage(); u.DeviceLimitReason != "cap" || u.Devices != DefaultMaxDevices {
		t.Fatalf("usage %+v", u)
	}
	e.maintain()
	full := int64(heapInuse()) - int64(before)
	e.clock.Add(DefaultIdleClose + time.Second)
	e.maintain() // parks every spoofer's file, then evicts them
	freed := int64(heapInuse()) - int64(before)
	t.Logf("heap over baseline: %d MiB with %d live spoofers, %d MiB after they idled out",
		full>>20, DefaultMaxDevices, freed>>20)
	if u := e.s.Usage(); u.Devices != 0 {
		t.Fatalf("%d devices still live", u.Devices)
	}
	if freed > 50<<20 || freed > full {
		t.Fatalf("heap stays %d MiB over baseline after eviction (%d MiB when full)", freed>>20, full>>20)
	}
	e.write(known, "known, afternoon")
	e.s.Flush()
	if got := strings.Join(e.files(known), ","); got != "2026-11-10.log" {
		t.Fatalf("known device files %s", got)
	}
	if got := rawStrings(readLines(t, e.dir, "192.0.2.7")); strings.Join(got, "|") != "known, morning|known, afternoon" {
		t.Fatalf("lines %q", got)
	}
}

// TestEarlyCloseAndReopen: a device goes quiet mid-day, its file is closed early,
// and its next message appends to the same file (no new part) whether the device
// state was freed, kept, or lost in a restart. The index, checkpoints included,
// matches a rebuild from the files; at midnight the parked file is compressed.
func TestEarlyCloseAndReopen(t *testing.T) {
	for _, mode := range []string{"evicted", "kept", "restart"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t, func(o *Options) {
				if mode == "kept" {
					o.IdleEvict = 24 * time.Hour
				}
			})
			a := ip(1)
			e.clock.Set(time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC))
			burst := func(tag string) {
				for i := 0; i < 600; i++ { // ~120 KiB over 10 min: several checkpoints
					e.write(a, fmt.Sprintf("%s %d %s", tag, i, strings.Repeat("x", 180)))
					e.clock.Add(time.Second)
				}
			}
			burst("first")
			e.clock.Add(DefaultIdleClose + time.Second)
			e.maintain()
			if got := e.s.Usage().Devices; (mode == "kept") != (got == 1) {
				t.Fatalf("%d live devices", got)
			}
			if mode == "restart" {
				e.reopen()
			}
			burst("second")
			e.maintain()
			if got := strings.Join(e.files(a), ","); got != "2026-11-10.log" {
				t.Fatalf("files %s", got)
			}
			lines := rawStrings(readLines(t, e.dir, "192.0.2.1"))
			if len(lines) != 1200 || !strings.HasPrefix(lines[600], "second 0 ") {
				t.Fatalf("%d lines", len(lines))
			}
			f, _ := e.idx.FileByPath(bg, "logs/192.0.2.1/2026-11-10.log")
			if f.Lines != 1200 {
				t.Fatalf("index lines %d", f.Lines)
			}
			snap := indexSnapshot(t, e.idx)
			fresh, err := index.Open(filepath.Join(t.TempDir(), "fresh.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			if _, err := fresh.Reindex(bg, e.dir); err != nil {
				t.Fatal(err)
			}
			if want := indexSnapshot(t, fresh); filesPart(snap) != filesPart(want) {
				t.Fatalf("index:\n%s\nrebuild:\n%s", filesPart(snap), filesPart(want))
			}
			// Quiet again, then midnight: the parked file is closed and compressed, and
			// the next message starts the new day.
			e.clock.Add(DefaultIdleClose + time.Second)
			e.maintain()
			e.clock.Set(time.Date(2026, 11, 11, 0, 0, 30, 0, time.UTC))
			e.maintain()
			e.write(a, "next day")
			e.maintain()
			if got := strings.Join(e.files(a), ","); got != "2026-11-10.log.gz,2026-11-11.log" {
				t.Fatalf("after midnight %s", got)
			}
			if u := e.s.Usage(); u.PendingCompression != 0 {
				t.Fatalf("usage %+v", u)
			}
		})
	}
}

func TestDeviceLimitsApplyLive(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.NewDeviceBurst = 1; o.NewDevicesPerMinute = 1 })
	e.write(ip(1), "takes the token")
	refuse := func(i int) {
		t.Helper()
		if err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: ip(i), Raw: []byte("x")}); !errors.Is(err, ErrDeviceLimit) {
			t.Fatalf("sender %d: %v", i, err)
		}
	}
	refuse(2)
	// Raising the burst admits senders at once, without waiting for a refill.
	e.s.SetLimits(Limits{NewDeviceBurst: 3})
	e.write(ip(2), "in")
	e.write(ip(3), "in")
	refuse(4)
	// A faster refill applies to the next new sender.
	e.s.SetLimits(Limits{NewDevicesPerMinute: 120})
	e.clock.Add(time.Second)
	e.write(ip(4), "refilled")
	// Lowering the cap refuses the next new sender even with tokens left.
	e.s.SetLimits(Limits{MaxDevices: 4, NewDeviceBurst: 100})
	e.clock.Add(time.Minute)
	refuse(5)
	if l := e.s.Limits(); l.MaxDevices != 4 || l.NewDeviceBurst != 100 || l.NewDevicesPerMinute != 120 || l.RetentionDays != 30 {
		t.Fatalf("limits %+v", l)
	}
	if u := e.s.Usage(); u.MaxDevices != 4 || u.DeviceLimitReason != "cap" {
		t.Fatalf("usage %+v", u)
	}
}
