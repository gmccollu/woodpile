package server

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodshed/internal/api"
	"github.com/gmccollu/woodshed/internal/config"
	"github.com/gmccollu/woodshed/internal/index"
)

func TestBlockDevice(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, freePort(t), 0, nil)
	r := start(t, cfgPath)
	now := time.Now().UTC()
	var acts []index.Activity
	for _, id := range []string{"192.0.2.10", "192.0.2.11", "198.51.100.5", "2001_db8__7"} {
		ip := strings.ReplaceAll(id, "_", ":")
		acts = append(acts, index.Activity{DeviceID: id, IP: ip, Minute: now.Truncate(time.Minute), FirstHeard: now, LastHeard: now,
			Counts: index.Counts{Msgs: 1}})
	}
	if err := r.srv.idx.Apply(context.Background(), index.Batch{Activity: acts}); err != nil {
		t.Fatal(err)
	}
	var d api.Device
	if code := r.do(t, "PATCH", "/api/devices/192.0.2.10", map[string]any{"blocked": true}, &d); code != 200 {
		t.Fatalf("block: %d", code)
	}
	if !d.Blocked || d.Status != "blocked" || d.BlockedBy != "" {
		t.Errorf("blocked device %+v", d)
	}
	if code := r.do(t, "PATCH", "/api/devices/2001_db8__7", map[string]any{"blocked": true, "name": "v6 box"}, &d); code != 200 ||
		!d.Blocked || d.Name != "v6 box" {
		t.Errorf("block v6: %d %+v", code, d)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil || strings.Join(cfg.Listeners.Blocklist, ",") != "192.0.2.10,2001:db8::7" {
		t.Fatalf("saved blocklist %v (%v)", cfg.Listeners.Blocklist, err)
	}
	if by, ok := r.srv.be.ln.Load().BlockedBy(mustAddr("192.0.2.10")); !ok || by != "192.0.2.10" {
		t.Errorf("not applied live: %q %v", by, ok)
	}

	// A covering CIDR (set in Settings) keeps a device blocked after its own
	// entry goes, and says which entry.
	var set api.Settings
	r.do(t, "GET", "/api/settings", nil, &set)
	set.Listeners.Blocklist = append(set.Listeners.Blocklist, "192.0.2.0/24")
	if code := r.do(t, "PUT", "/api/settings", set, nil); code != 200 {
		t.Fatalf("PUT %d", code)
	}
	d = api.Device{}
	r.do(t, "PATCH", "/api/devices/192.0.2.10", map[string]any{"blocked": false}, &d)
	if !d.Blocked || d.BlockedBy != "192.0.2.0/24" || d.Status != "blocked" {
		t.Errorf("unblock under a CIDR: %+v", d)
	}
	cfg, _ = config.Load(cfgPath)
	if strings.Join(cfg.Listeners.Blocklist, ",") != "2001:db8::7,192.0.2.0/24" {
		t.Errorf("blocklist after unblock %v", cfg.Listeners.Blocklist)
	}

	// Lists, filters and stats: 192.0.2.10, .11 (the /24) and the v6 box are
	// blocked; 198.51.100.5 isn't.
	var page api.DevicePage
	r.do(t, "GET", "/api/devices?blocked=only&sort=name", nil, &page)
	if page.Total != 3 {
		t.Errorf("blocked=only (name sort): %d", page.Total)
	}
	r.do(t, "GET", "/api/devices?blocked=exclude", nil, &page)
	if page.Total != 1 || page.Devices[0].ID != "198.51.100.5" || page.Devices[0].Blocked {
		t.Errorf("blocked=exclude (rate sort): %+v", page)
	}
	r.do(t, "GET", "/api/devices?blocked=only", nil, &page)
	for _, dv := range page.Devices {
		if !dv.Blocked || dv.Status != "blocked" {
			t.Errorf("listed %+v", dv)
		}
	}
	var st api.Stats
	r.do(t, "GET", "/api/stats", nil, &st)
	if st.Devices.Total != 1 || st.Devices.Online != 1 {
		t.Errorf("stats %+v", st.Devices)
	}
	if code := r.do(t, "GET", "/api/devices?blocked=yes", nil, nil); code != 400 {
		t.Errorf("bad filter: %d", code)
	}

	// Unblocking the v6 box clears it; events record both.
	d = api.Device{}
	r.do(t, "PATCH", "/api/devices/2001_db8__7", map[string]any{"blocked": false}, &d)
	if d.Blocked || d.Status == "blocked" || d.BlockedBy != "" {
		t.Errorf("unblocked %+v", d)
	}
	evs, _ := r.srv.idx.Events(context.Background(), time.Time{}, time.Time{}, 0)
	var texts []string
	for _, e := range evs {
		if e.Kind == index.KindDevice {
			texts = append(texts, e.DeviceID+": "+e.Text)
		}
	}
	want := "192.0.2.10: blocked 192.0.2.10 by the admin|2001_db8__7: blocked 2001:db8::7 by the admin|" +
		"192.0.2.10: unblocked 192.0.2.10 by the admin|2001_db8__7: unblocked 2001:db8::7 by the admin"
	if strings.Join(texts, "|") != want {
		t.Errorf("events %q", texts)
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
