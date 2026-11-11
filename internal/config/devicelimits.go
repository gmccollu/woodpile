package config

import "fmt"

// New-sender limits (docs/api.md, Settings and the device_limit banner): at
// most MaxDevices active senders, and a new sender needs a token from a bucket
// of NewDeviceBurst refilled at NewDevicesPerMinute.
const (
	DefaultMaxDevices          = 10000
	DefaultNewDeviceBurst      = 5000
	DefaultNewDevicesPerMinute = 200
)

func normalizeDeviceLimits(l *Listeners) {
	if l.MaxDevices == 0 {
		l.MaxDevices = DefaultMaxDevices
	}
	if l.NewDeviceBurst == 0 {
		l.NewDeviceBurst = DefaultNewDeviceBurst
	}
	if l.NewDevicesPerMinute == 0 {
		l.NewDevicesPerMinute = DefaultNewDevicesPerMinute
	}
}

func checkDeviceLimits(e Errors, l Listeners) {
	for _, c := range []struct {
		key       string
		v, lo, hi int
	}{
		{"listeners.max_devices", l.MaxDevices, 100, 1000000},
		{"listeners.new_device_burst", l.NewDeviceBurst, 1, 1000000},
		{"listeners.new_devices_per_minute", l.NewDevicesPerMinute, 1, 100000},
	} {
		if c.v < c.lo || c.v > c.hi {
			e[c.key] = fmt.Sprintf("must be between %d and %d", c.lo, c.hi)
		}
	}
}
