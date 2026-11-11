package index

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestQueryDevices(t *testing.T) {
	d := openIn(t, t.TempDir())
	var acts []Activity
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("192.0.2.%d", i)
		heard := t0.Add(time.Duration(i) * time.Minute)
		acts = append(acts, Activity{DeviceID: id, IP: id, Minute: t0.Add(-time.Duration(i) * time.Hour), FirstHeard: heard,
			LastHeard: heard, Counts: Counts{Msgs: 1}})
		// Recent rate: device i sent i messages in the last minute.
		acts = append(acts, Activity{DeviceID: id, IP: id, Minute: t0.Add(time.Hour), LastHeard: heard, Counts: Counts{Msgs: int64(i)}})
	}
	if err := d.Apply(ctx, Batch{Activity: acts}); err != nil {
		t.Fatal(err)
	}
	set := func(id, name, group string, hidden bool) {
		d.UpdateDevice(ctx, id, DeviceUpdate{Name: &name, Group: &group, Hidden: &hidden})
	}
	set("192.0.2.3", "Zeta-Edge", "HQ", false)
	set("192.0.2.4", "alpha-core", "HQ", true)
	set("192.0.2.5", "beta", "Lab", false)
	window := DeviceQuery{RateFrom: t0.Add(time.Hour), RateTo: t0.Add(time.Hour + time.Minute)}

	q := window
	q.Limit = 5
	p, err := d.QueryDevices(ctx, q)
	if err != nil || p.Total != 30 || len(p.Devices) != 5 || p.Devices[0].ID != "192.0.2.29" || p.Devices[0].RecentMsgs != 29 {
		t.Fatalf("rate page: %+v %v", p, err)
	}
	q.Offset = 28
	if p, _ := d.QueryDevices(ctx, q); len(p.Devices) != 2 || p.Devices[1].ID != "192.0.2.0" {
		t.Fatalf("last page %+v", p.Devices)
	}
	ids := func(p DevicePage) string {
		var s []string
		for _, dv := range p.Devices {
			s = append(s, dv.ID)
		}
		return strings.Join(s, ",")
	}
	// Name A-Z, case-insensitive, an unnamed device by its IP (as internal/api does):
	// the IPs' digits sort before alpha-core, beta, Zeta-Edge.
	q = DeviceQuery{Sort: SortName, Limit: 30}
	if got := ids(func() DevicePage { p, _ := d.QueryDevices(ctx, q); return p }()); !strings.HasPrefix(got, "192.0.2.0,192.0.2.1,192.0.2.10,") ||
		!strings.HasSuffix(got, ",192.0.2.4,192.0.2.5,192.0.2.3") {
		t.Fatalf("name sort %s", got)
	}
	if p, _ := d.QueryDevices(ctx, DeviceQuery{Sort: SortLastHeard, Limit: 1}); ids(p) != "192.0.2.29" {
		t.Fatalf("last_heard %s", ids(p))
	}
	if p, _ := d.QueryDevices(ctx, DeviceQuery{Q: "ZETA"}); ids(p) != "192.0.2.3" {
		t.Fatalf("q by name %s", ids(p))
	}
	if p, _ := d.QueryDevices(ctx, DeviceQuery{Q: "2.2", Sort: SortName}); p.Total != 11 {
		t.Fatalf("q by ip: %d", p.Total) // 192.0.2.2 and 2.20-2.29
	}
	if p, _ := d.QueryDevices(ctx, DeviceQuery{Group: "HQ", Sort: SortName}); ids(p) != "192.0.2.4,192.0.2.3" {
		t.Fatalf("group %s", ids(p))
	}
	if p, _ := d.QueryDevices(ctx, DeviceQuery{Group: "HQ", Hidden: HiddenExclude}); ids(p) != "192.0.2.3" {
		t.Fatalf("group, hidden excluded %s", ids(p))
	}
	if p, _ := d.QueryDevices(ctx, DeviceQuery{Hidden: HiddenOnly}); ids(p) != "192.0.2.4" {
		t.Fatalf("hidden only %s", ids(p))
	}
	if p, _ := d.QueryDevices(ctx, DeviceQuery{Ungrouped: true}); p.Total != 27 {
		t.Fatalf("ungrouped %d", p.Total)
	}
	if _, err := d.QueryDevices(ctx, DeviceQuery{Sort: "bogus"}); err == nil {
		t.Fatal("accepted a bad sort")
	}
	if p, _ := d.QueryDevices(ctx, DeviceQuery{Limit: 5000}); len(p.Devices) != 30 {
		t.Fatal("limit cap")
	}
	gs, err := d.Groups(ctx)
	if err != nil || len(gs) != 2 || gs[0] != (GroupCount{"HQ", 1}) || gs[1] != (GroupCount{"Lab", 1}) {
		t.Fatalf("groups %+v %v", gs, err)
	}
}

func TestPruneDevices(t *testing.T) {
	d := openIn(t, t.TempDir())
	old, recent := t0.Add(-48*time.Hour), t0
	for _, x := range []struct {
		id    string
		heard time.Time
	}{{"192.0.2.1", old}, {"192.0.2.2", old}, {"192.0.2.3", old}, {"192.0.2.4", recent}, {"192.0.2.5", old}} {
		d.Apply(ctx, Batch{Activity: []Activity{{DeviceID: x.id, IP: x.id, Minute: x.heard, LastHeard: x.heard, Counts: Counts{Msgs: 1}}}})
	}
	name, group := "kept", "HQ"
	d.UpdateDevice(ctx, "192.0.2.2", DeviceUpdate{Name: &name})
	d.UpdateDevice(ctx, "192.0.2.3", DeviceUpdate{Group: &group})
	d.Apply(ctx, Batch{Files: []FileOp{{Kind: OpUpsert, File: fileRow("192.0.2.5", "2026-11-08.log.gz", old, old, 1)}}})
	ids, err := d.PruneDevices(ctx, t0.Add(-24*time.Hour))
	if err != nil || strings.Join(ids, ",") != "192.0.2.1" {
		t.Fatalf("pruned %v %v", ids, err)
	}
	devs, _ := d.ListDevices(ctx)
	if len(devs) != 4 {
		t.Fatalf("%d devices left", len(devs))
	}
}
