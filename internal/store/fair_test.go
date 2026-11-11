package store

import (
	"fmt"
	"math/rand"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/index"
	"github.com/gmccollu/woodpile/internal/model"
)

func randText(r *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + r.Intn(26))
	}
	return string(b)
}

// TestFairShareManySmallSenders is the review's N7 case: a flood spread over 200 new
// senders, each small, pushes the store over its cap. Largest-first would have wiped
// the busiest real device. With fair shares the target keeps its newest day, and the
// new senders' data goes before any of the target's newest day.
func TestFairShareManySmallSenders(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.PartBytes = 32 << 10 })
	r := rand.New(rand.NewSource(3))
	target := ip(1)
	end := e.clock.Now()
	e.clock.Set(end.Add(-6 * 24 * time.Hour))
	for day := 0; day < 7; day++ { // the target: a week of busy history
		for i := 0; i < 20; i++ {
			e.write(target, fmt.Sprintf("target %d %s", day, randText(r, 1000)))
		}
		e.maintain()
		if day < 6 {
			e.clock.Add(24 * time.Hour)
		}
	}
	// Today, 200 new senders each send a little over one part (one closed part each).
	for i := 0; i < 200; i++ {
		a := spread(i)
		for j := 0; j < 40; j++ {
			e.s.Write(model.Message{Received: e.clock.Now(), Sender: a, Raw: []byte(randText(r, 1000))})
		}
	}
	e.maintain()
	before := e.files(target)
	e.s.SetLimits(Limits{RetentionBytes: duLogs(e.dir) / 2})
	e.maintain()
	after := e.files(target)
	t.Logf("target files %d -> %d: %v", len(before), len(after), after)
	if !contains(after, "2026-11-09.log.gz") {
		t.Fatalf("the target lost its newest closed day: %v", after)
	}
	hist, _ := e.idx.Deletions(bg, time.Time{}, time.Time{}, 0)
	var targetDel, spoofDel int
	for _, h := range hist {
		if h.DeviceID == "192.0.2.1" {
			targetDel++
		} else {
			spoofDel++
		}
	}
	if spoofDel == 0 {
		t.Fatalf("no new sender's file was deleted; target lost %d", targetDel)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestLowDiskClosesOpenFiles is N9: many senders, each under the part size, fill the
// disk with active and parked files only. The guard closes the largest as parts and
// deletes them instead of pausing until midnight.
func TestLowDiskClosesOpenFiles(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.MinFreeBytes = 1 << 20; o.MinFreePercent = 1 })
	e.disk.total = 1 << 30 // reserve: 10 MiB (1%)
	r := rand.New(rand.NewSource(5))
	var addrs []netip.Addr
	for i := 0; i < 60; i++ {
		a := netip.AddrFrom4([4]byte{198, 51, 100, byte(i)})
		addrs = append(addrs, a)
		for j := 0; j < 10+i; j++ {
			e.write(a, randText(r, 2000))
		}
	}
	e.maintain()
	// Half go quiet: their files are parked.
	e.clock.Add(DefaultIdleClose + time.Second)
	for _, a := range addrs[:30] {
		e.write(a, "still here")
	}
	e.maintain()
	used := uint64(duLogs(e.dir))
	e.disk.setOther(1<<30 - used - (10<<20 - 1<<20)) // 1 MiB short of the reserve
	e.maintain()
	if e.s.Paused() {
		t.Fatal("paused although open files could be closed and deleted")
	}
	if free, _, _ := e.disk.usage(""); free < 10<<20 {
		t.Fatalf("free %d after the guard", free)
	}
	hist, _ := e.idx.Deletions(bg, time.Time{}, time.Time{}, 0)
	if len(hist) == 0 || hist[0].Reason != index.ReasonLowDisk || !strings.Contains(hist[0].File, ".001.log") {
		t.Fatalf("deletions %+v", hist)
	}
	if n := len(eventsOf(t, e.idx, index.KindRotate)); n == 0 {
		t.Fatal("no early-close event")
	}
	// Every sender keeps writing, into a fresh file for today.
	for _, a := range addrs {
		e.write(a, "after the guard")
	}
	e.maintain()
	if e.s.Paused() {
		t.Fatal("paused")
	}
	for _, a := range addrs {
		ls := readLines(t, e.dir, model.DeviceID(a))
		if string(ls[len(ls)-1].Raw()) != "after the guard" {
			t.Fatalf("%v: last line %q", a, ls[len(ls)-1].Raw())
		}
	}
}

// TestPruneEmptyDevices is N15: a device whose logs retention has deleted, with no
// name or group and quiet for a day, is forgotten (index row, folder, known entry); a
// named one is kept.
func TestPruneEmptyDevices(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.RetentionDays = 2 })
	gone, named := ip(1), ip(2)
	e.write(gone, "x")
	e.write(named, "y")
	name := "kept"
	e.maintain()
	if _, err := e.idx.UpdateDevice(bg, "192.0.2.2", index.DeviceUpdate{Name: &name}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ { // days pass; retention deletes both devices' files
		e.clock.Add(24 * time.Hour)
		e.maintain()
	}
	if files, _ := e.idx.Files(bg, ""); len(files) != 0 {
		t.Fatalf("files left %+v", files)
	}
	if devs, _ := e.idx.ListDevices(bg); len(devs) != 1 || devs[0].ID != "192.0.2.2" {
		t.Fatalf("devices %+v", devs)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "logs", "192.0.2.1")); !os.IsNotExist(err) {
		t.Fatalf("folder kept: %v", err)
	}
	e.s.devMu.Lock()
	_, knownGone := e.s.known["192.0.2.1"]
	_, knownNamed := e.s.known["192.0.2.2"]
	e.s.devMu.Unlock()
	if knownGone || !knownNamed {
		t.Fatalf("known: gone %v, named %v", knownGone, knownNamed)
	}
	// It comes back as a new device if it sends again.
	e.write(gone, "back")
	e.maintain()
	if d, err := e.idx.Device(bg, "192.0.2.1"); err != nil || d.TotalMsgs != 1 {
		t.Fatalf("%+v %v", d, err)
	}
}

// TestLowDiskFairOrderForOpenFiles: 200 small new senders and one busy established
// device, all with only open files for today, and low disk. The new senders' open
// files are closed and deleted first; the real device keeps today's file as long as
// anything else can be deleted, and every device that lost today's data is named in
// a warn event.
func TestLowDiskFairOrderForOpenFiles(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.MinFreeBytes = 1 << 20; o.MinFreePercent = 1 })
	e.disk.total = 1 << 30 // reserve 10 MiB
	r := rand.New(rand.NewSource(9))
	real := ip(1)
	// The real device has a few days of history (closed and compressed) and is busy today.
	for d := 3; d >= 1; d-- {
		e.clock.Set(day0.Add(-time.Duration(d) * 24 * time.Hour))
		e.write(real, fmt.Sprintf("day -%d %s", d, randText(r, 1000)))
		e.maintain()
	}
	e.clock.Set(day0)
	e.maintain()
	for i := 0; i < 400; i++ {
		e.write(real, "today "+randText(r, 1000))
	}
	for i := 0; i < 200; i++ {
		for j := 0; j < 3; j++ {
			e.s.Write(model.Message{Received: e.clock.Now(), Sender: spread(i), Raw: []byte(randText(r, 1000))})
		}
	}
	e.maintain()
	used := uint64(duLogs(e.dir))
	// Short by 200 KiB: well under what the new senders' ~600 KiB of open files can free.
	e.disk.setOther(1<<30 - used - (10<<20 - 200<<10))
	e.maintain()
	if e.s.Paused() {
		t.Fatal("paused")
	}
	// Its older closed days may go (closed files older than a device's newest day come
	// before anyone's open files); its newest closed day and today's file stay.
	if got := strings.Join(e.files(real), ","); got != "2026-11-09.log.gz,2026-11-10.log" {
		t.Fatalf("real device files %s", got)
	}
	hist, _ := e.idx.Deletions(bg, time.Time{}, time.Time{}, 0)
	if len(hist) == 0 {
		t.Fatal("nothing deleted")
	}
	named := map[string]bool{}
	for _, ev := range eventsOf(t, e.idx, index.KindDelete) {
		if ev.Level == index.LevelWarn && strings.Contains(ev.Text, "today's logs") {
			named[ev.DeviceID] = true
		}
	}
	for _, h := range hist {
		if h.DeviceID == "192.0.2.1" && !strings.Contains(h.File, "2026-11-07") && !strings.Contains(h.File, "2026-11-08") {
			t.Fatalf("the real device lost %s while new senders had open files", h.File)
		}
		if strings.Contains(h.File, "2026-11-10") && !named[h.DeviceID] {
			t.Fatalf("no warn event for %s losing today's %s", h.DeviceID, h.File)
		}
	}
	// Only when nothing else is left does the real device's today file go.
	e.disk.setOther(1<<30 - uint64(duLogs(e.dir)) - (10<<20 - uint64(duLogs(e.dir))/2))
	e.maintain()
	if !named["192.0.2.1"] {
		for _, ev := range eventsOf(t, e.idx, index.KindDelete) {
			if ev.Level == index.LevelWarn && ev.DeviceID == "192.0.2.1" {
				named["192.0.2.1"] = true
			}
		}
	}
	hist, _ = e.idx.Deletions(bg, time.Time{}, time.Time{}, 0)
	for _, h := range hist {
		if h.DeviceID == "192.0.2.1" && strings.Contains(h.File, "2026-11-10") && !named["192.0.2.1"] {
			t.Fatal("the real device's today file went without a warn event")
		}
	}
}
