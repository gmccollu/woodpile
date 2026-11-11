package config

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlocklistValidate(t *testing.T) {
	c := testConfig(t)
	c.Listeners.Blocklist = []string{"198.51.100.7", "bad", " 203.0.113.0/24 ", "2001:db8::/129"}
	errs := c.validate("linux", nil)
	if len(errs) != 2 || !strings.Contains(errs["listeners.blocklist[1]"], "CIDR") || errs["listeners.blocklist[3]"] == "" {
		t.Errorf("errors %v", errs)
	}
	c.Listeners.Blocklist = []string{"198.51.100.7", "2001:db8::/32"}
	if errs := c.validate("linux", nil); errs != nil {
		t.Errorf("valid list: %v", errs)
	}
}

func TestBlocklistRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	c := testConfig(t)
	c.Listeners.Blocklist = []string{"198.51.100.7", "203.0.113.0/24"}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || strings.Join(got.Listeners.Blocklist, ",") != "198.51.100.7,203.0.113.0/24" {
		t.Fatalf("%v %v", got.Listeners.Blocklist, err)
	}
	// A file without the key, and a JSON null, both give [].
	os.WriteFile(path, []byte("[listeners]\n"), 0o640)
	if got, _ := Load(path); got.Listeners.Blocklist == nil || len(got.Listeners.Blocklist) != 0 {
		t.Errorf("missing key: %#v", got.Listeners.Blocklist)
	}
	next := c.Clone()
	json.Unmarshal([]byte(`{"listeners":{"blocklist":null}}`), &next)
	next.Normalize()
	if b, _ := json.Marshal(next.Listeners); !strings.Contains(string(b), `"blocklist":[]`) {
		t.Errorf("JSON %s", b)
	}
}

func TestBlockedByBlockUnblock(t *testing.T) {
	ip := netip.MustParseAddr("198.51.100.7")
	l := Listeners{Blocklist: []string{"198.51.100.0/24", "bad", " 198.51.100.0/28 "}}
	if e, ok := l.BlockedBy(ip); !ok || e != "198.51.100.0/28" {
		t.Errorf("most specific: %q %v", e, ok)
	}
	if e, ok := l.BlockedBy(netip.MustParseAddr("::ffff:198.51.100.200")); !ok || e != "198.51.100.0/24" {
		t.Errorf("mapped: %q %v", e, ok)
	}
	if _, ok := l.BlockedBy(netip.MustParseAddr("192.0.2.1")); ok {
		t.Error("unlisted address blocked")
	}
	if !l.Block(ip) || l.Block(ip) {
		t.Error("Block should add once")
	}
	if e, _ := l.BlockedBy(ip); e != "198.51.100.7" {
		t.Errorf("after Block: %q", e)
	}
	l.Blocklist = append(l.Blocklist, "198.51.100.7/32")
	if n := l.Unblock(ip); n != 2 {
		t.Errorf("Unblock removed %d", n)
	}
	// The broader CIDRs stay, and still block it.
	if e, ok := l.BlockedBy(ip); !ok || e != "198.51.100.0/28" || len(l.Blocklist) != 3 {
		t.Errorf("after Unblock: %q %v %v", e, ok, l.Blocklist)
	}
	v6 := Listeners{}
	v6.Block(netip.MustParseAddr("2001:db8::1%eth0"))
	if v6.Blocklist[0] != "2001:db8::1" {
		t.Errorf("v6 entry %q", v6.Blocklist[0])
	}
	if ps, err := (Listeners{Blocklist: []string{"2001:db8::1", "203.0.113.0/24"}}).BlockPrefixes(); err != nil || ps[0].String() != "2001:db8::1/128" {
		t.Errorf("%v %v", ps, err)
	}
}
