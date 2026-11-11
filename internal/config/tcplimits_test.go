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
	if c.Listeners.TCPMaxConnsPerSource != 32 || time.Duration(c.Listeners.TCPFrameTimeout) != time.Minute {
		t.Errorf("defaults %+v", c.Listeners)
	}
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "tcp_max_conns_per_source = 32") || !strings.Contains(string(b), "tcp_frame_timeout = '1m0s'") {
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
	c.Listeners.TCPFrameTimeout = Duration(time.Millisecond)
	errs := c.validate("linux", nil)
	if !strings.Contains(errs["listeners.tcp_max_conns_per_source"], "between") || !strings.Contains(errs["listeners.tcp_frame_timeout"], "between") {
		t.Errorf("errors %v", errs)
	}
}
