package store

import (
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/model"
)

// TestStatsRollupBoundary drives the maintenance pass's rollup with the fake clock.
func TestStatsRollupBoundary(t *testing.T) {
	e := newEnv(t, nil)
	a := ip(1)
	e.clock.Set(time.Date(2026, 11, 10, 10, 59, 30, 0, time.UTC))
	e.write(a, "10:59")
	e.clock.Set(time.Date(2026, 11, 10, 11, 0, 10, 0, time.UTC))
	e.write(a, "11:00")
	e.maintain()
	if n, _ := e.idx.TotalCounts(bg, time.Time{}, time.Time{}); n.Msgs != 2 {
		t.Fatalf("%+v", n)
	}
	// 24 h 30 min later the boundary is 11:00 the day before: 10:59 rolls up, 11:00 stays.
	now := time.Date(2026, 11, 11, 11, 30, 0, 0, time.UTC)
	e.clock.Set(now)
	e.maintain()
	ms, _ := e.idx.MinuteSeries(bg, nil, time.Date(2026, 11, 10, 10, 59, 0, 0, time.UTC), time.Date(2026, 11, 10, 11, 1, 0, 0, time.UTC))
	if ms[0].Msgs != 0 || ms[1].Msgs != 1 {
		t.Fatalf("minute series %+v", ms)
	}
	hs, _ := e.idx.HourSeries(bg, nil, time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	if hs[0].Msgs != 1 || hs[1].Msgs != 1 {
		t.Fatalf("hour series %+v", hs)
	}
	if c, _ := e.idx.Counts(bg, []string{"192.0.2.1"}, now.Add(-7*24*time.Hour), now); c.Msgs != 2 {
		t.Fatalf("7-day count %+v", c)
	}
	if c, _ := e.idx.TotalCounts(bg, now.Add(-24*time.Hour), now); c.Msgs != 0 {
		t.Fatalf("24h count %+v", c)
	}
	// Past the 7-day stats retention both buckets are pruned; device totals stay.
	e.clock.Set(now.Add(7 * 24 * time.Hour))
	e.maintain()
	if c, _ := e.idx.TotalCounts(bg, time.Time{}, time.Time{}); c.Msgs != 0 {
		t.Fatalf("after retention %+v", c)
	}
	if d, _ := e.idx.Device(bg, "192.0.2.1"); d.TotalMsgs != 2 {
		t.Fatalf("device %+v", d)
	}
}

// TestMidnightDoesNotStallWrites crosses midnight with many devices while the fsync of
// each closed file is slow, and checks that writes for the new day stay fast and that
// nothing is lost.
func TestMidnightDoesNotStallWrites(t *testing.T) {
	const devices, writers, perDevice = 400, 4, 20
	const slowSync = 50 * time.Millisecond
	e := newEnv(t, func(o *Options) {
		o.Manual = false
		o.FlushInterval = 10 * time.Millisecond
		o.CloseWorkers = 8
	})
	var synced atomic.Int64
	e.s.closeMu.Lock()
	real := e.s.syncPath
	e.s.syncPath = func(p string) error {
		time.Sleep(slowSync)
		synced.Add(1)
		return real(p)
	}
	e.s.closeMu.Unlock()
	addrs := make([]netip.Addr, devices)
	for i := range addrs {
		addrs[i] = netip.AddrFrom4([4]byte{198, 51, 100 + byte(i/250), byte(i % 250)})
	}
	e.clock.Set(time.Date(2026, 11, 10, 23, 59, 59, 0, time.UTC))
	for _, a := range addrs {
		e.write(a, "yesterday")
	}
	if err := e.s.Flush(); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(time.Date(2026, 11, 11, 0, 0, 1, 0, time.UTC))

	// A maintenance pass that closes all 400 files returns quickly; sequential fsyncs
	// alone would take devices*slowSync = 20 s.
	start := time.Now()
	go e.s.Maintain()
	var mu sync.Mutex
	var lat []time.Duration
	var wg sync.WaitGroup
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			local := make([]time.Duration, 0, devices*perDevice)
			for n := 0; n < perDevice; n++ {
				for i := range addrs {
					a := addrs[(i+g*97)%devices]
					t0 := time.Now()
					err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: a, Raw: []byte(fmt.Sprintf("today %d %d", g, n))})
					local = append(local, time.Since(t0))
					if err != nil {
						t.Error(err)
						return
					}
				}
			}
			mu.Lock()
			lat = append(lat, local...)
			mu.Unlock()
		}(g)
	}
	wg.Wait()
	writesDone := time.Since(start)
	syncedDuringWrites := synced.Load()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p99, max := lat[len(lat)*99/100], lat[len(lat)-1]
	t.Logf("%d writes in %v; latency p50 %v p99 %v max %v; %d/%d old files fsynced by then",
		len(lat), writesDone, lat[len(lat)/2], p99, max, syncedDuringWrites, devices)
	if max >= slowSync/2 {
		t.Fatalf("a write took %v, so it waited for a closed file's fsync (%v)", max, slowSync)
	}
	if err := e.s.Close(); err != nil {
		t.Fatal(err)
	}
	e.s = nil
	if n := synced.Load(); n != devices {
		t.Fatalf("%d of %d closed files fsynced", n, devices)
	}
	for _, a := range addrs {
		ls := readLines(t, e.dir, model.DeviceID(a))
		if len(ls) != 1+writers*perDevice || string(ls[0].Raw()) != "yesterday" {
			t.Fatalf("%v: %d lines", a, len(ls))
		}
		if got := e.files(a); len(got) != 2 || got[1] != "2026-11-11.log" {
			t.Fatalf("%v: files %v", a, got)
		}
	}
}
