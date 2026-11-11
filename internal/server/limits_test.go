package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gmccollu/woodpile/internal/api"
	"github.com/gmccollu/woodpile/internal/config"
	"github.com/gmccollu/woodpile/internal/index"
	"github.com/gmccollu/woodpile/internal/store"
)

func TestDeviceLimitBanner(t *testing.T) {
	for _, tc := range []struct {
		reason, want string
	}{
		{"cap", "the limit of 500 active devices was reached"},
		{"rate", "new-device rate limit"},
		{"prefix", "from one network"},
	} {
		bs := buildBanners(nil, store.Usage{DeviceLimited: true, DeviceLimitReason: tc.reason, MaxDevices: 500, RefusedMsgs: 7}, "/data")
		if len(bs) != 1 || bs[0].Kind != "device_limit" || bs[0].Level != api.LevelWarn || !strings.Contains(bs[0].Text, tc.want) ||
			!strings.Contains(bs[0].Text, "Settings") || !strings.Contains(bs[0].Text, "7 so far") {
			t.Errorf("%s: %+v", tc.reason, bs)
		}
	}
	if bs := buildBanners(nil, store.Usage{MaxDevices: 500, RefusedMsgs: 7}, "/data"); len(bs) != 0 {
		t.Errorf("no recent refusal, still a banner: %+v", bs)
	}
}

// TestRefusedBucketIsNotADevice: the stats of refused new senders count in
// dropped_24h but never appear as a device.
func TestRefusedBucketIsNotADevice(t *testing.T) {
	dir := t.TempDir()
	r := start(t, writeConfig(t, dir, freePort(t), 0, nil))
	now := time.Now().UTC().Truncate(time.Minute)
	if err := r.srv.idx.Apply(context.Background(), index.Batch{Activity: []index.Activity{
		{DeviceID: index.RefusedDeviceID, Minute: now, LastHeard: now, Counts: index.Counts{Dropped: 9}},
		{DeviceID: "192.0.2.1", IP: "192.0.2.1", Minute: now, FirstHeard: now, LastHeard: now, Counts: index.Counts{Msgs: 3}},
	}}); err != nil {
		t.Fatal(err)
	}
	var page api.DevicePage
	r.do(t, "GET", "/api/devices", nil, &page)
	if page.Total != 1 || page.Devices[0].ID != "192.0.2.1" {
		t.Errorf("devices %+v", page)
	}
	var st api.Stats
	r.do(t, "GET", "/api/stats", nil, &st)
	if st.Dropped24h != 9 || st.Devices.Total != 1 {
		t.Errorf("stats dropped %d, devices %+v", st.Dropped24h, st.Devices)
	}
	if _, ok := mustRates(t, r)[index.RefusedDeviceID]; ok {
		t.Error("the refused bucket has a rate")
	}
}

func mustRates(t *testing.T, r *running) map[string]index.Counts {
	t.Helper()
	rs, err := r.srv.be.deviceRates(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return rs.counts
}

func TestSenderLimitsReachTheStore(t *testing.T) {
	dir := t.TempDir()
	r := start(t, writeConfig(t, dir, freePort(t), 0, func(c *config.Config) {
		c.Listeners.MaxDevices, c.Listeners.NewDeviceBurst, c.Listeners.NewDevicesPerMinute = 321, 50, 7
	}))
	if l := r.srv.store.Limits(); l.MaxDevices != 321 || l.NewDeviceBurst != 50 || l.NewDevicesPerMinute != 7 {
		t.Fatalf("at start: %+v", l)
	}
	var set api.Settings
	r.do(t, "GET", "/api/settings", nil, &set)
	if set.Listeners.MaxDevices != 321 || set.Listeners.NewDeviceBurst != 50 || set.Listeners.NewDevicesPerMinute != 7 {
		t.Fatalf("settings %+v", set.Listeners)
	}
	set.Listeners.MaxDevices, set.Listeners.NewDeviceBurst, set.Listeners.NewDevicesPerMinute = 2000, 100, 60
	if code := r.do(t, "PUT", "/api/settings", set, nil); code != 200 {
		t.Fatalf("PUT %d", code)
	}
	if l := r.srv.store.Limits(); l.MaxDevices != 2000 || l.NewDeviceBurst != 100 || l.NewDevicesPerMinute != 60 {
		t.Errorf("after PUT: %+v", l)
	}
	set.Listeners.MaxDevices = 5
	var verr struct{ Errors map[string]string }
	if code := r.do(t, "PUT", "/api/settings", set, &verr); code != 400 || verr.Errors["listeners.max_devices"] == "" {
		t.Errorf("bad max_devices: %d %v", code, verr.Errors)
	}
}
