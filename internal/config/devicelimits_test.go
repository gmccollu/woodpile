package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeviceLimits(t *testing.T) {
	c := Default()
	if c.Listeners.MaxDevices != 10000 || c.Listeners.NewDeviceBurst != 5000 || c.Listeners.NewDevicesPerMinute != 200 {
		t.Fatalf("defaults %+v", c.Listeners)
	}
	// An older config without the keys gets the defaults.
	dir := t.TempDir()
	path := filepath.Join(dir, "woodshed.toml")
	os.WriteFile(path, []byte("[listeners]\nmax_devices = 500\n"), 0o640)
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listeners.MaxDevices != 500 || got.Listeners.NewDeviceBurst != 5000 || got.Listeners.NewDevicesPerMinute != 200 {
		t.Errorf("loaded %+v", got.Listeners)
	}
	for _, tc := range []struct {
		max, burst, rate int
		bad              []string
	}{
		{100, 1, 1, nil},
		{1000000, 1000000, 100000, nil},
		{99, 0, 0, []string{"listeners.max_devices", "listeners.new_device_burst", "listeners.new_devices_per_minute"}},
		{1000001, 1000001, 100001, []string{"listeners.max_devices", "listeners.new_device_burst", "listeners.new_devices_per_minute"}},
	} {
		c := testConfig(t)
		c.Listeners.MaxDevices, c.Listeners.NewDeviceBurst, c.Listeners.NewDevicesPerMinute = tc.max, tc.burst, tc.rate
		errs := c.validate("linux", nil)
		for _, k := range tc.bad {
			if !strings.Contains(errs[k], "between") {
				t.Errorf("%+v: %s = %q", tc, k, errs[k])
			}
		}
		if tc.bad == nil && len(errs) != 0 {
			t.Errorf("%+v: %v", tc, errs)
		}
	}
}
