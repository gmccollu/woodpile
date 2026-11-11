package server

import (
	"context"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/api"
	"github.com/gmccollu/woodpile/internal/index"
)

// scaleLimit is the most any device endpoint may take at 10,000 devices on a
// small CI runner (without -race).
const scaleLimit = 500 * time.Millisecond

// TestDeviceEndpointsAt10000Devices: the device list, the dashboard's stats and
// a PATCH (which refreshes the live tail's group and hidden caches) stay fast
// with the store's device cap of 10,000. Results are checked always; the
// timing only without -race.
func TestDeviceEndpointsAt10000Devices(t *testing.T) {
	if testing.Short() {
		t.Skip("-short")
	}
	dir := t.TempDir()
	r := start(t, writeConfig(t, dir, freePort(t), 0, nil))
	now := time.Now().UTC().Truncate(time.Minute)
	var b index.Batch
	for i := 0; i < 10000; i++ {
		id := fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255)
		b.Activity = append(b.Activity, index.Activity{DeviceID: id, IP: id, Minute: now, FirstHeard: now, LastHeard: now,
			Counts: index.Counts{Msgs: int64(i % 500)}})
	}
	if err := r.srv.idx.Apply(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	// And the live counters a running server keeps (what msgs_per_min shows
	// and sort=rate orders by): device i sent i%7 messages just now.
	at := time.Now()
	for i := 0; i < 10000; i++ {
		a := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		for n := 0; n < i%7; n++ {
			r.srv.be.live.add(a, at)
		}
	}
	timed := func(what string, f func()) {
		t.Helper()
		t0 := time.Now()
		f()
		d := time.Since(t0)
		t.Logf("%s: %v", what, d.Round(time.Millisecond))
		// Timing only without -race: instrumented SQLite on a CI runner is 25x
		// slower and says nothing about real hardware. CI runs this test without
		// -race too (ci.yml, "Scale test without -race").
		if !raceEnabled && d > scaleLimit {
			t.Errorf("%s took %v (limit %v)", what, d, scaleLimit)
		}
	}
	var page api.DevicePage
	timed("GET /api/devices?sort=rate&limit=30", func() { r.do(t, "GET", "/api/devices?sort=rate&limit=30", nil, &page) })
	if page.Total != 10000 || len(page.Devices) != 30 || page.Devices[0].MsgsPerMin < page.Devices[29].MsgsPerMin {
		t.Fatalf("page: total %d, %d devices", page.Total, len(page.Devices))
	}
	var st api.Stats
	timed("GET /api/stats", func() { r.do(t, "GET", "/api/stats", nil, &st) })
	if st.Devices.Total != 10000 {
		t.Errorf("stats devices %+v", st.Devices)
	}
	timed("PATCH a device (refreshes the hub caches)", func() {
		if code := r.do(t, "PATCH", "/api/devices/10.0.0.7", map[string]any{"hidden": true, "group": "lab"}, nil); code != 200 {
			t.Fatalf("patch %d", code)
		}
	})
	if !r.srv.be.hiddenOf("10.0.0.7") || r.srv.be.groupOf("10.0.0.7") != "lab" {
		t.Error("caches not refreshed")
	}
	// Groups: "lab" holds only a hidden device, so it isn't listed.
	r.do(t, "PATCH", "/api/devices/10.0.0.8", map[string]any{"group": "HQ"}, nil)
	r.do(t, "PATCH", "/api/devices/10.0.0.9", map[string]any{"group": "HQ"}, nil)
	var groups []api.Group
	timed("GET /api/groups", func() { r.do(t, "GET", "/api/groups", nil, &groups) })
	if len(groups) != 1 || groups[0] != (api.Group{Name: "HQ", Devices: 2}) {
		t.Errorf("groups %+v", groups)
	}
	// Paging in SQL: filters and the total.
	timed("GET /api/devices?group=HQ", func() { r.do(t, "GET", "/api/devices?group=HQ", nil, &page) })
	if page.Total != 2 || len(page.Devices) != 2 {
		t.Errorf("HQ page %+v", page.Total)
	}
	r.do(t, "GET", "/api/devices?hidden=only", nil, &page)
	if page.Total != 1 || page.Devices[0].ID != "10.0.0.7" {
		t.Errorf("hidden page %+v", page)
	}
}
