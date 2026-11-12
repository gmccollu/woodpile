package config

import (
	"strings"
	"testing"
)

func TestParseExtraHostname(t *testing.T) {
	for _, tc := range []struct {
		in, name, ip string
	}{
		{"logs.example.net", "logs.example.net", ""},
		{" Logs.Example.NET. ", "logs.example.net", ""},
		{"box", "box", ""},
		{"x-1.example", "x-1.example", ""},
		{"192.0.2.7", "", "192.0.2.7"},
		{"2001:db8::7", "", "2001:db8::7"},
		{"[2001:db8::7]", "", "2001:db8::7"},
		{"::ffff:192.0.2.7", "", "192.0.2.7"},
	} {
		name, ip, ok := ParseExtraHostname(tc.in)
		if !ok || name != tc.name || (tc.ip != "" && ip.String() != tc.ip) || (tc.ip == "" && ip.IsValid()) {
			t.Errorf("%q: %q %v %v", tc.in, name, ip, ok)
		}
	}
	for _, bad := range []string{"", "*.example.net", "-a.example", "a-.example", "a..b", "a b", "a_b.example", "1.2.3",
		"logs.example.net:443", "fe80::1%eth0", strings.Repeat("a", 64) + ".example", "http://x"} {
		if _, _, ok := ParseExtraHostname(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseTrustedHost(t *testing.T) {
	for in, want := range map[string]TrustedHost{
		"logs.example.net":     {"logs.example.net", 0},
		"Logs.Example.net:443": {"logs.example.net", 443},
		"192.0.2.7":            {"192.0.2.7", 0},
		"192.0.2.7:8443":       {"192.0.2.7", 8443},
		"[2001:db8::7]":        {"2001:db8::7", 0},
		"[2001:db8::7]:443":    {"2001:db8::7", 443},
		"2001:db8::7":          {"2001:db8::7", 0},
	} {
		got, ok := ParseTrustedHost(in)
		if !ok || got != want {
			t.Errorf("%q: %+v %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "logs.example.net:", "logs.example.net:0", "logs.example.net:70000", "logs.example.net:x",
		"*.example.net", "a b", "[logs.example.net]:443", "http://logs.example.net", "logs.example.net/x"} {
		if th, ok := ParseTrustedHost(bad); ok {
			t.Errorf("%q accepted: %+v", bad, th)
		}
	}
}

func TestValidateUIHosts(t *testing.T) {
	c := Default()
	c.Storage.DataDir = t.TempDir()
	c.UI.ExtraHostnames = []string{"logs.example.net", "bad name", "192.0.2.9"}
	c.UI.TrustedHosts = []string{"proxy.example.net:443", "proxy.example.net:"}
	c.Normalize()
	e := c.validate("linux", nil)
	if len(e) != 2 || e["ui.extra_hostnames[1]"] == "" || e["ui.trusted_hosts[1]"] == "" {
		t.Errorf("%v", e)
	}
	c.UI.ExtraHostnames = make([]string, MaxUIHostEntries+1)
	for i := range c.UI.ExtraHostnames {
		c.UI.ExtraHostnames[i] = "a.example"
	}
	if e := c.validate("linux", nil); e["ui.extra_hostnames"] == "" {
		t.Errorf("no cap: %v", e)
	}
	// Normalize keeps the indexes (no entries dropped) and lists non-nil.
	var u Config
	u.Normalize()
	if u.UI.ExtraHostnames == nil || u.UI.TrustedHosts == nil {
		t.Error("nil lists")
	}
	c = Default()
	c.UI.TrustedHosts = []string{" A.example ", ""}
	c.Normalize()
	if c.UI.TrustedHosts[0] != "a.example" || len(c.UI.TrustedHosts) != 2 {
		t.Errorf("%q", c.UI.TrustedHosts)
	}
	if got := c.UI.TrustedHostList(); len(got) != 1 || got[0].Host != "a.example" {
		t.Errorf("%+v", got)
	}
}
