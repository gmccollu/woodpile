package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/api"
	"github.com/gmccollu/woodpile/internal/index"
)

// TestDeviceEndpointsAt10000Devices: the device list, the dashboard's stats and
// a PATCH (which refreshes the live tail's group and hidden caches) stay fast
// with the store's device cap of 10,000.
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
	timed := func(what string, f func()) {
		t.Helper()
		t0 := time.Now()
		f()
		d := time.Since(t0)
		t.Logf("%s: %v", what, d.Round(time.Millisecond))
		if d > 2*time.Second {
			t.Errorf("%s took %v", what, d)
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
}
