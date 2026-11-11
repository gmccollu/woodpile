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

// TestMidnightDoesNotStallWrites crosses midnight with many devices and checks that
// writes never wait for the fsync of a closed file, and that nothing is lost.
//
// The fake fsync doesn't sleep: it blocks on a gate that stays shut until every write
// of the new day is done. A write (or maintenance pass) that depended on a closer's
// fsync could then never finish, and the watchdog fails the test. That catches a real
// wait however fast or slow the machine is, where a "no write over N ms" bound would
// have to choose between missing a wait and failing on scheduler noise: on a 2-vCPU
// runner under -race, a goroutine holding a device lock can be descheduled for tens of
// milliseconds, and Windows' clock moves in ~15.6 ms ticks. Latencies are only logged.
func TestMidnightDoesNotStallWrites(t *testing.T) {
	const devices, writers, perDevice, workers = 400, 4, 20, 8
	e := newEnv(t, func(o *Options) {
		o.Manual = false
		o.FlushInterval = 10 * time.Millisecond
		o.CloseWorkers = workers
	})
	gate := make(chan struct{})
	var started, finished atomic.Int64
	e.s.closeMu.Lock()
	real := e.s.syncPath
	e.s.syncPath = func(p string) error {
		started.Add(1)
		<-gate
		finished.Add(1)
		return real(p)
	}
	e.s.closeMu.Unlock()
	released := false
	release := func() {
		if !released {
			released = true
			close(gate)
		}
	}
	defer release()
	// within fails the test if fn doesn't return in time, which here means it was
	// waiting for a gated fsync.
	within := func(what string, fn func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { defer close(done); fn() }()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Fatalf("%s didn't finish while closed files' fsyncs were held: it waits on the closer pool", what)
		}
	}

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
	// Half the devices rotate on the write path (their first message of the new day),
	// the other half in a maintenance pass.
	within("a first write of the new day", func() {
		for _, a := range addrs[:devices/2] {
			e.write(a, "first today")
		}
	})
	within("the maintenance pass", func() { e.s.Maintain() })
	// Every closer is now stuck in an fsync, with the rest of the 400 queued behind.
	deadline := time.Now().Add(30 * time.Second)
	for started.Load() < workers && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := started.Load(); n != workers {
		t.Fatalf("%d closers in fsync, want %d", n, workers)
	}

	var mu sync.Mutex
	var lat []time.Duration
	within("the new day's writes", func() {
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
		e.s.Usage() // the API's view doesn't wait on the closers either
	})
	if n := finished.Load(); n != 0 {
		t.Fatalf("%d fsyncs finished while the gate was shut", n)
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	t.Logf("%d writes with all %d old files' fsyncs pending; latency p50 %v p99 %v max %v",
		len(lat), devices, lat[len(lat)/2], lat[len(lat)*99/100], lat[len(lat)-1])

	release()
	if err := e.s.Close(); err != nil {
		t.Fatal(err)
	}
	e.s = nil
	if n := finished.Load(); n != devices {
		t.Fatalf("%d of %d closed files fsynced", n, devices)
	}
	for i, a := range addrs {
		ls := readLines(t, e.dir, model.DeviceID(a))
		want := 1 + writers*perDevice
		if i < devices/2 {
			want++
		}
		if len(ls) != want || string(ls[0].Raw()) != "yesterday" {
			t.Fatalf("%v: %d lines, want %d", a, len(ls), want)
		}
		if got := e.files(a); len(got) != 2 || got[1] != "2026-11-11.log" {
			t.Fatalf("%v: files %v", a, got)
		}
	}
}
