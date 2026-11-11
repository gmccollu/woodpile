package index

import (
	"strings"
	"testing"
	"time"
)

func TestDropCountsMatchCounts(t *testing.T) {
	d := openIn(t, t.TempDir())
	now := t0.Add(30 * time.Hour)
	var acts []Activity
	for i := 0; i < 48*60; i += 7 {
		m := t0.Add(time.Duration(i) * time.Minute)
		acts = append(acts, Activity{DeviceID: "192.0.2.1", IP: "192.0.2.1", Minute: m, LastHeard: m, Counts: Counts{Msgs: 5, Dropped: int64(i % 3), Truncated: int64(i % 2)}})
	}
	acts = append(acts, Activity{DeviceID: RefusedDeviceID, Minute: now.Add(-time.Hour), Counts: Counts{Dropped: 9}})
	if err := d.Apply(ctx, Batch{Activity: acts}); err != nil {
		t.Fatal(err)
	}
	d.RollupStats(ctx, now.Add(-MinuteRetention))
	for _, from := range []time.Time{now.Add(-24 * time.Hour), now.Add(-90 * time.Minute), t0, now.Add(-24*time.Hour + 17*time.Minute)} {
		c, _ := d.Counts(ctx, nil, from, time.Time{})
		tr, dr, err := d.DropCounts(ctx, from)
		if err != nil || tr != c.Truncated || dr != c.Dropped {
			t.Errorf("from %v: DropCounts %d/%d, Counts %d/%d (%v)", from, tr, dr, c.Truncated, c.Dropped, err)
		}
	}
	var plan string
	d.db.QueryRow(`EXPLAIN QUERY PLAN SELECT sum(dropped) FROM stats WHERE minute >= 0 AND (dropped > 0 OR truncated > 0)`).Scan(new(int), new(int), new(int), &plan)
	if !strings.Contains(plan, "stats_drops") {
		t.Errorf("plan %q doesn't use the partial index", plan)
	}
}

func TestStatusCounts(t *testing.T) {
	d := openIn(t, t.TempDir())
	now := t0.Add(time.Hour)
	var acts []Activity
	for i, ago := range []time.Duration{time.Minute, 4 * time.Minute, 10 * time.Minute, 59 * time.Minute, 2 * time.Hour, 3 * time.Hour} {
		id := []string{"a", "b", "c", "d", "e", "f"}[i]
		h := now.Add(-ago)
		acts = append(acts, Activity{DeviceID: id, IP: id, Minute: h.Truncate(time.Minute), FirstHeard: h, LastHeard: h, Counts: Counts{Msgs: 1}})
	}
	d.Apply(ctx, Batch{Activity: acts})
	yes := true
	d.UpdateDevice(ctx, "f", DeviceUpdate{Hidden: &yes})
	on, q, off, err := d.StatusCounts(ctx, now.Add(-5*time.Minute), now.Add(-time.Hour))
	if err != nil || on != 2 || q != 2 || off != 1 {
		t.Errorf("online %d quiet %d offline %d (%v)", on, q, off, err)
	}
}

func TestQueryDevicesOnlyAndNotIDs(t *testing.T) {
	d := openIn(t, t.TempDir())
	var acts []Activity
	for _, id := range []string{"a", "b", "c", "d"} {
		acts = append(acts, Activity{DeviceID: id, IP: id, Minute: t0, FirstHeard: t0, LastHeard: t0, Counts: Counts{Msgs: 1}})
	}
	d.Apply(ctx, Batch{Activity: acts})
	ids := func(p DevicePage) string {
		var out []string
		for _, dv := range p.Devices {
			out = append(out, dv.ID)
		}
		return strings.Join(out, ",")
	}
	p, _ := d.QueryDevices(ctx, DeviceQuery{Sort: SortName, OnlyIDs: []string{"b", "d", "zz"}})
	if ids(p) != "b,d" || p.Total != 2 {
		t.Errorf("only %q %d", ids(p), p.Total)
	}
	p, _ = d.QueryDevices(ctx, DeviceQuery{Sort: SortName, NotIDs: []string{"b"}})
	if ids(p) != "a,c,d" || p.Total != 3 {
		t.Errorf("not %q %d", ids(p), p.Total)
	}
	if p, _ := d.QueryDevices(ctx, DeviceQuery{OnlyIDs: []string{}}); p.Total != 0 || len(p.Devices) != 0 {
		t.Errorf("empty only: %+v", p)
	}
}
