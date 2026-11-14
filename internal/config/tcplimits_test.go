package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTCPLimits(t *testing.T) {
	c := testConfig(t)
	if c.Listeners.TCPMaxConnsPerSource != 32 || c.Listeners.TCPMaxConnsPerPrefix != 256 || time.Duration(c.Listeners.TCPFrameTimeout) != time.Minute {
		t.Errorf("defaults %+v", c.Listeners)
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "tcp_max_conns_per_source = 32") || !strings.Contains(string(b), "tcp_max_conns_per_prefix = 256") || !strings.Contains(string(b), "tcp_frame_timeout = '1m0s'") {
		t.Errorf("file:\n%s", b)
	}
	os.WriteFile(path, []byte("[listeners]\ntcp_frame_timeout = \"90s\"\ntcp_max_conns_per_source = 4\n"), 0o640)
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listeners.TCPMaxConnsPerSource != 4 || time.Duration(got.Listeners.TCPFrameTimeout) != 90*time.Second {
		t.Errorf("loaded %+v", got.Listeners)
	}
	os.WriteFile(path, []byte("[listeners]\ntcp_frame_timeout = \"soon\"\n"), 0o640)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "duration") {
		t.Errorf("bad duration: %v", err)
	}
	c.Listeners.TCPMaxConnsPerSource = -1
	c.Listeners.TCPMaxConnsPerPrefix = -1
	c.Listeners.TCPFrameTimeout = Duration(time.Millisecond)
	errs := c.validate("linux", nil)
	if !strings.Contains(errs["listeners.tcp_max_conns_per_source"], "between") || !strings.Contains(errs["listeners.tcp_max_conns_per_prefix"], "between") || !strings.Contains(errs["listeners.tcp_frame_timeout"], "between") {
		t.Errorf("errors %v", errs)
	}
}

func TestTCPIdleTimeout(t *testing.T) {
	c := testConfig(t)
	if time.Duration(c.Listeners.TCPIdleTimeout) != 24*time.Hour {
		t.Errorf("default %v, want 24h", time.Duration(c.Listeners.TCPIdleTimeout))
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "tcp_idle_timeout = '24h0m0s'") {
		t.Errorf("file:\n%s", b)
	}
	// A file from 1.0.0 (no key) gets the default; a set value is kept.
	os.WriteFile(path, []byte("[listeners]\ntcp_frame_timeout = \"90s\"\n"), 0o640)
	if got, err := Load(path); err != nil || time.Duration(got.Listeners.TCPIdleTimeout) != 24*time.Hour {
		t.Errorf("missing key: %v %v", time.Duration(got.Listeners.TCPIdleTimeout), err)
	}
	os.WriteFile(path, []byte("[listeners]\ntcp_idle_timeout = \"72h\"\n"), 0o640)
	if got, err := Load(path); err != nil || time.Duration(got.Listeners.TCPIdleTimeout) != 72*time.Hour {
		t.Errorf("72h: %v %v", time.Duration(got.Listeners.TCPIdleTimeout), err)
	}
	for d, ok := range map[time.Duration]bool{
		time.Minute: true, 10 * time.Minute: true, 24 * time.Hour: true, 7 * 24 * time.Hour: true,
		59 * time.Second: false, 7*24*time.Hour + time.Second: false, -time.Hour: false,
	} {
		c.Listeners.TCPIdleTimeout = Duration(d)
		msg, bad := c.validate("linux", nil)["listeners.tcp_idle_timeout"]
		if bad == ok {
			t.Errorf("%v: error %q", d, msg)
		}
		if bad && !strings.Contains(msg, "between 1m and 168h") {
			t.Errorf("%v: message %q", d, msg)
		}
	}
}
