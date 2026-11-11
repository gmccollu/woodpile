package config

import (
	"fmt"
	"time"
)

// TCP abuse limits (file-only, see Listeners).
const (
	DefaultTCPMaxConnsPerSource = 32
	DefaultTCPFrameTimeout      = Duration(60 * time.Second)

	MaxTCPMaxConnsPerSource = 100000
	MinTCPFrameTimeout      = Duration(time.Second)
	MaxTCPFrameTimeout      = Duration(time.Hour)
)

// Duration is a time.Duration written in the config file as text ("60s", "2m").
type Duration time.Duration

// MarshalText writes d as Go duration text ("1m0s").
func (d Duration) MarshalText() ([]byte, error) { return []byte(time.Duration(d).String()), nil }

// UnmarshalText reads Go duration text ("60s", "1m30s").
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("%q is not a duration (e.g. 60s or 2m)", b)
	}
	*d = Duration(v)
	return nil
}

// normalizeTCPLimits gives zero TCP limits their defaults.
func normalizeTCPLimits(l *Listeners) {
	if l.TCPMaxConnsPerSource == 0 {
		l.TCPMaxConnsPerSource = DefaultTCPMaxConnsPerSource
	}
	if l.TCPFrameTimeout == 0 {
		l.TCPFrameTimeout = DefaultTCPFrameTimeout
	}
}

func checkTCPLimits(e Errors, l Listeners) {
	if n := l.TCPMaxConnsPerSource; n != 0 && (n < 1 || n > MaxTCPMaxConnsPerSource) {
		e["listeners.tcp_max_conns_per_source"] = fmt.Sprintf("must be between 1 and %d", MaxTCPMaxConnsPerSource)
	}
	if d := l.TCPFrameTimeout; d != 0 && (d < MinTCPFrameTimeout || d > MaxTCPFrameTimeout) {
		e["listeners.tcp_frame_timeout"] = "must be between 1s and 1h"
	}
}
