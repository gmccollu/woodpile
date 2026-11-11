package store

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"woodpile/internal/index"
	"woodpile/internal/model"
)

func devAddr(i int) netip.Addr { return netip.AddrFrom4([4]byte{203, 0, 113, byte(i)}) }

// checkAll reads every file back and checks that each (writer, seq) pair sent to each
// device is there exactly once.
func checkAll(t *testing.T, dataDir string, devices int, sent []map[[2]int]bool) int {
	t.Helper()
	total := 0
	for d := 0; d < devices; d++ {
		lines := readLines(t, dataDir, model.DeviceID(devAddr(d)))
		seen := make(map[[2]int]bool, len(lines))
		for _, l := range lines {
			f := strings.Fields(string(l.Raw()))
			if len(f) < 3 {
				t.Fatalf("device %d: odd line %q", d, l.Raw())
			}
			g, _ := strconv.Atoi(f[1])
			n, _ := strconv.Atoi(f[2])
			k := [2]int{g, n}
			if seen[k] {
				t.Fatalf("device %d: duplicate %v", d, k)
			}
			if !sent[d][k] {
				t.Fatalf("device %d: unexpected %v", d, k)
			}
			seen[k] = true
		}
		if len(seen) != len(sent[d]) {
			t.Fatalf("device %d: %d lines on disk, %d sent", d, len(seen), len(sent[d]))
		}
		total += len(lines)
	}
	return total
}

func TestConcurrentWriters(t *testing.T) {
	e := newEnv(t, func(o *Options) {
		o.Manual = false
		o.FlushInterval = 5 * time.Millisecond
		o.SyncInterval = 20 * time.Millisecond
		o.PartBytes = 64 << 10
		o.MaxOpenFiles = 16
	})
	const writers, devices, perWriter = 16, 50, 3000
	sent := make([]map[[2]int]bool, devices)
	for i := range sent {
		sent[i] = map[[2]int]bool{}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	stop := make(chan struct{})
	// The clock races ahead (crossing midnights) while maintenance, Usage and index
	// readers run alongside the writers.
	var side sync.WaitGroup
	side.Add(1)
	go func() {
		defer side.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				e.clock.Add(20 * time.Minute)
				e.s.Usage()
				e.idx.ListDevices(bg)
			}
		}
	}()
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for n := 0; n < perWriter; n++ {
				d := (g*7 + n) % devices
				raw := fmt.Sprintf("w %d %d %s", g, n, strings.Repeat("c", n%200))
				if err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: devAddr(d), Raw: []byte(raw)}); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				sent[d][[2]int{g, n}] = true
				mu.Unlock()
				if n%500 == 0 && g == 0 {
					e.s.Flush()
				}
			}
		}(g)
	}
	wg.Wait()
	close(stop)
	side.Wait()
	if err := e.s.Close(); err != nil {
		t.Fatal(err)
	}
	if total := checkAll(t, e.dir, devices, sent); total != writers*perWriter {
		t.Fatalf("%d lines, want %d", total, writers*perWriter)
	}
	files, _ := e.idx.Files(bg, "")
	var lines int64
	for _, f := range files {
		lines += f.Lines
	}
	if lines != writers*perWriter {
		t.Fatalf("index counts %d lines", lines)
	}
}

// TestMillionMessages writes 1M messages across 50 devices with forced size parts, a
// midnight rotation and a restart in the middle, then checks that the lines on disk
// (decompressing the .gz files) match exactly, and that the index agrees.
func TestMillionMessages(t *testing.T) {
	total, partBytes := 1_000_000, int64(1<<20)
	if testing.Short() {
		// A tenth of the messages: shrink the parts too, so files still split.
		total, partBytes = 100_000, 64<<10
	}
	const devices, writers = 50, 8
	e := newEnv(t, func(o *Options) {
		o.Manual = false
		o.PartBytes = partBytes
		o.FlushInterval = 20 * time.Millisecond
		o.SyncInterval = 100 * time.Millisecond
	})
	e.clock.Set(time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC))
	sent := make([]map[[2]int]bool, devices)
	for i := range sent {
		sent[i] = make(map[[2]int]bool, total/devices)
	}
	var mu sync.Mutex
	var next atomic.Int64
	run := func(upto int64) {
		var wg sync.WaitGroup
		for g := 0; g < writers; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				local := make([][2]int, 0, total/writers)
				for {
					n := next.Add(1) - 1
					if n >= upto {
						break
					}
					d := int(n % devices)
					if n%2000 == 0 {
						e.clock.Add(4 * time.Second) // 1M msgs span ~33 min of fake time
					}
					raw := fmt.Sprintf("seq %d %d <134>Nov 10 23:00:00 host-%d app[42]: %s", g, n, d, strings.Repeat("v", int(n%120)))
					if err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: devAddr(d), Raw: []byte(raw)}); err != nil {
						t.Error(err)
						return
					}
					local = append(local, [2]int{d, int(n)})
				}
				mu.Lock()
				for _, x := range local {
					sent[x[0]][[2]int{g, x[1]}] = true
				}
				mu.Unlock()
			}(g)
		}
		wg.Wait()
	}
	run(int64(total / 2))
	next.Store(int64(total / 2)) // each writer took one number past the end
	e.reopen()                   // a service restart mid-run
	e.clock.Set(time.Date(2026, 11, 10, 23, 59, 50, 0, time.UTC))
	run(int64(total)) // crosses midnight
	if err := e.s.Close(); err != nil {
		t.Fatal(err)
	}
	e.s = nil
	if t.Failed() {
		return
	}
	got := checkAll(t, e.dir, devices, sent)
	if got != total {
		t.Fatalf("%d lines on disk, want %d", got, total)
	}
	files, _ := e.idx.Files(bg, "")
	var lines int64
	parts, gz := 0, 0
	for _, f := range files {
		lines += f.Lines
		if f.Part > 0 {
			parts++
		}
		if f.Compressed {
			gz++
		}
	}
	if lines != int64(total) {
		t.Fatalf("index counts %d lines, want %d", lines, total)
	}
	if parts == 0 || !strings.Contains(indexSnapshot(t, e.idx), "2026-11-11.log ") {
		t.Fatalf("no parts (%d) or no midnight rotation", parts)
	}
	t.Logf("%d messages, %d files (%d numbered parts, %d compressed at close)", total, len(files), parts, gz)
	devs, _ := e.idx.ListDevices(bg)
	var msgs int64
	for _, d := range devs {
		msgs += d.TotalMsgs
	}
	if msgs != int64(total) || len(devs) != devices {
		t.Fatalf("device totals %d over %d devices", msgs, len(devs))
	}
	// The files alone rebuild the same index.
	fresh, err := index.Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err := fresh.Reindex(bg, e.dir); err != nil {
		t.Fatal(err)
	}
	if a, b := indexSnapshot(t, e.idx), indexSnapshot(t, fresh); a != b {
		t.Fatal("rebuilt index differs from the store's")
	}
}

// BenchmarkWrite measures Write throughput with the production defaults (1 s flush,
// 5 s fsync, background gzip, 512 open handles) and ~180-byte messages: across 50
// devices, and across 5,000 devices so the handle LRU is cycling.
func BenchmarkWrite(b *testing.B) {
	for _, devices := range []int{50, 5000} {
		b.Run(fmt.Sprintf("devices=%d", devices), func(b *testing.B) { benchWrite(b, devices) })
	}
}

func benchWrite(b *testing.B, devices int) {
	dir := b.TempDir()
	idx, err := index.Open(filepath.Join(dir, index.FileName))
	if err != nil {
		b.Fatal(err)
	}
	defer idx.Close()
	s, err := Open(Options{DataDir: dir, Index: idx, Logger: quiet})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	raw := []byte("<134>Nov 10 20:41:07 edge-01 conn[311]: Built outbound TCP connection 4711 for outside:198.51.100.20/443 to inside:192.0.2.10/51515 duration 0:00:00 bytes 0")
	addrs := make([]netip.Addr, devices)
	for i := range addrs {
		addrs[i] = netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)})
	}
	var ctr atomic.Int64
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	start := time.Now()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := ctr.Add(1)
			if err := s.Write(model.Message{Received: time.Now(), Sender: addrs[n%int64(devices)], Raw: raw}); err != nil {
				b.Error(err)
				return
			}
		}
	})
	if err := s.Flush(); err != nil {
		b.Fatal(err)
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N)/time.Since(start).Seconds(), "msgs/s")
}
