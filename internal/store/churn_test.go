package store

import (
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gmccollu/woodshed/internal/index"
	"github.com/gmccollu/woodshed/internal/model"
)

// TestChurnAtTheCap is K1: with the cap full, known senders that aren't resident are
// readmitted at ReadmitPerSecond (each evicting the least recently heard resident,
// whose file is parked, not lost) and refused beyond it (reason "churn"). A resident
// sender is never refused.
func TestChurnAtTheCap(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.MaxDevices = 20; o.ReadmitPerSecond = 2 })
	addr := func(i int) netip.Addr { return netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}) }
	for i := 0; i < 20; i++ {
		e.write(addr(i), "first")
	}
	e.clock.Add(DefaultIdleClose + time.Second)
	e.maintain() // all quiet: parked and evicted, but known
	if n := e.s.Usage().Devices; n != 0 {
		t.Fatalf("%d resident", n)
	}
	e.s.SetLimits(Limits{MaxDevices: 10})
	for i := 0; i < 10; i++ { // fill the cap with known senders (not full yet: no limit)
		e.write(addr(i), "second")
	}
	admitted, churned := 0, 0
	for i := 10; i < 20; i++ {
		err := e.s.Write(model.Message{Received: e.clock.Now(), Sender: addr(i), Raw: []byte("second")})
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, ErrDeviceLimit):
			churned++
		default:
			t.Fatal(err)
		}
	}
	if admitted != 2 || churned != 8 {
		t.Fatalf("admitted %d, refused %d; want 2 and 8", admitted, churned)
	}
	if u := e.s.Usage(); u.DeviceLimitReason != "churn" || u.Devices != 10 {
		t.Fatalf("usage %+v", u)
	}
	// Every resident sender still gets in, the two evicted ones included (they're
	// known, so they're readmitted in turn once the bucket refills).
	for i := 2; i < 10; i++ {
		e.write(addr(i), "third")
	}
	e.maintain() // the closer pool carries out the evictions
	e.clock.Add(time.Second)
	for _, i := range []int{0, 1} {
		e.write(addr(i), "third")
	}
	if err := e.s.Close(); err != nil {
		t.Fatal(err)
	}
	e.s = nil
	// Nothing written was lost: 20 first lines, 12 second, 10 third.
	if n := countLines(t, e.dir); n != 20+12+10 {
		t.Fatalf("%d lines on disk", n)
	}
	evs := eventsOf(t, e.idx, index.KindDeviceLimit)
	if len(evs) == 0 {
		t.Fatal("no device_limit event")
	}
}

// BenchmarkChurn is the reviewer's K1 case: 20,000 known senders cycling through a
// cap of 10,000. Before the fix it ran at about 11,700 msg/s.
func BenchmarkChurn(b *testing.B) {
	dir := b.TempDir()
	idx, err := index.Open(filepath.Join(dir, index.FileName))
	if err != nil {
		b.Fatal(err)
	}
	defer idx.Close()
	const senders = 20000
	addrs := make([]netip.Addr, senders)
	for i := range addrs {
		addrs[i] = spread(i)
	}
	// Make them all known (logs on disk), then start over with the default cap.
	s, err := Open(Options{DataDir: dir, Index: idx, Logger: quiet, MaxDevices: senders, NewDeviceBurst: senders})
	if err != nil {
		b.Fatal(err)
	}
	for _, a := range addrs {
		if err := s.Write(model.Message{Received: time.Now(), Sender: a, Raw: []byte("seed")}); err != nil {
			b.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(Options{DataDir: dir, Index: idx, Logger: quiet})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	raw := []byte(fmt.Sprintf("<134>Nov 10 20:41:07 host app: churn %0150d", 0))
	var ctr, refused atomic.Int64
	b.ResetTimer()
	start := time.Now()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := ctr.Add(1)
			if err := s.Write(model.Message{Received: time.Now(), Sender: addrs[n%senders], Raw: raw}); err != nil {
				refused.Add(1)
			}
		}
	})
	b.StopTimer()
	el := time.Since(start).Seconds()
	b.ReportMetric(float64(b.N)/el, "msgs/s")
	b.ReportMetric(float64(b.N-int(refused.Load()))/el, "stored/s")
}
