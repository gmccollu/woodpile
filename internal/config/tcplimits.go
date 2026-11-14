package config

import (
	"fmt"
	"time"
)

// TCP abuse limits (file-only, see Listeners).
const (
	DefaultTCPMaxConnsPerSource = 32
	DefaultTCPMaxConnsPerPrefix = 256 // per IPv6 /64
	DefaultTCPFrameTimeout      = Duration(60 * time.Second)
	// DefaultTCPIdleTimeout is long because some senders keep a TCP connection
	// open for days with no keepalives, notice that it was closed only when a
	// later write fails, and lose what they send until they reconnect.
	DefaultTCPIdleTimeout = Duration(24 * time.Hour)

	MaxTCPMaxConnsPerSource = 100000
	MinTCPFrameTimeout      = Duration(time.Second)
	MaxTCPFrameTimeout      = Duration(time.Hour)
	MinTCPIdleTimeout       = Duration(time.Minute)
	MaxTCPIdleTimeout       = Duration(7 * 24 * time.Hour)
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
	if l.TCPMaxConnsPerPrefix == 0 {
		l.TCPMaxConnsPerPrefix = DefaultTCPMaxConnsPerPrefix
	}
	if l.TCPFrameTimeout == 0 {
		l.TCPFrameTimeout = DefaultTCPFrameTimeout
	}
	if l.TCPIdleTimeout == 0 {
		l.TCPIdleTimeout = DefaultTCPIdleTimeout
	}
}

func checkTCPLimits(e Errors, l Listeners) {
	if n := l.TCPMaxConnsPerSource; n != 0 && (n < 1 || n > MaxTCPMaxConnsPerSource) {
		e["listeners.tcp_max_conns_per_source"] = fmt.Sprintf("must be between 1 and %d", MaxTCPMaxConnsPerSource)
	}
	if n := l.TCPMaxConnsPerPrefix; n != 0 && (n < 1 || n > MaxTCPMaxConnsPerSource) {
		e["listeners.tcp_max_conns_per_prefix"] = fmt.Sprintf("must be between 1 and %d", MaxTCPMaxConnsPerSource)
	}
	if d := l.TCPFrameTimeout; d != 0 && (d < MinTCPFrameTimeout || d > MaxTCPFrameTimeout) {
		e["listeners.tcp_frame_timeout"] = "must be between 1s and 1h"
	}
	if d := l.TCPIdleTimeout; d != 0 && (d < MinTCPIdleTimeout || d > MaxTCPIdleTimeout) {
		e["listeners.tcp_idle_timeout"] = "must be between 1m and 168h (7 days)"
	}
}
