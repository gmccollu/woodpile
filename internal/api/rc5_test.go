package api

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestParseResolvConf(t *testing.T) {
	in := `# generated
nameserver 192.0.2.53
domain corp.example
search Lab.Example. corp.example local bad_domain ;comment
search example.net # trailing
options ndots:2
`
	got := parseResolvConf(strings.NewReader(in))
	want := []string{"corp.example", "lab.example", "corp.example", "example.net"}
	if !slices.Equal(got, want) {
		t.Errorf("%q", got)
	}
}

func TestIdentitySearchDomainsAndExtras(t *testing.T) {
	id := identityFrom("Box", []string{"lab.example", "corp.example"}, nil, nil)
	for _, n := range []string{"box", "box.local", "box.lab.example", "box.corp.example", "localhost"} {
		if !slices.Contains(id.Names, n) {
			t.Errorf("missing %s: %v", n, id.Names)
		}
	}
	if id.Host != "box" {
		t.Errorf("host %q", id.Host)
	}
	// An FQDN hostname isn't qualified again.
	if id := identityFrom("box.corp.example", []string{"lab.example"}, nil, nil); len(id.Names) != 2 {
		t.Errorf("fqdn: %v", id.Names)
	}
	// At most maxSearchDomains.
	many := []string{"a.example", "b.example", "c.example", "d.example", "e.example", "f.example", "g.example"}
	if id := identityFrom("box", many, nil, nil); slices.Contains(id.Names, "box.g.example") || !slices.Contains(id.Names, "box.f.example") {
		t.Errorf("cap: %v", id.Names)
	}
	ex := id.withExtras([]string{"logs.example.net", "192.0.2.80", "Box"})
	if !slices.Contains(ex.Names, "logs.example.net") || !slices.Contains(ex.IPs, netip.MustParseAddr("192.0.2.80")) ||
		len(ex.Names) != len(id.Names)+1 || ex.Host != "box" {
		t.Errorf("extras: %+v", ex)
	}
	if len(id.Names) == len(ex.Names) {
		t.Error("withExtras changed its receiver's slices")
	}
}

// TestExtraHostnamesRegenerateKeepingKey: a new ui.extra_hostnames entry in LAN
// mode reissues the certificate with the same key, covering the name.
func TestExtraHostnamesRegenerateKeepingKey(t *testing.T) {
	srv := lanServer(t, "password-123")
	c := tlsClient(t, srv.dataDir)
	login(t, srv, c, "password-123")
	before := servedLeaf(t, srv)
	if slices.Contains(before.DNSNames, "logs.example.net") {
		t.Fatal("already there")
	}
	r := do(t, c, "GET", srv.url("/api/settings"), "")
	var st Settings
	json.Unmarshal(r.body, &st)
	st.UI.ExtraHostnames = []string{"logs.example.net", "192.0.2.80"}
	body, _ := json.Marshal(st)
	if r := do(t, c, "PUT", srv.url("/api/settings"), string(body), "If-Match: "+r.header.Get("ETag")); r.code != 200 {
		t.Fatalf("PUT %d %s", r.code, r.body)
	} else if strings.Contains(string(r.body), "restarting") {
		t.Errorf("no restart for extra names: %s", r.body)
	}
	after := servedLeaf(t, srv)
	if !slices.Contains(after.DNSNames, "logs.example.net") || !slices.ContainsFunc(after.IPAddresses, func(ip net.IP) bool {
		a, _ := netip.AddrFromSlice(ip)
		return a.Unmap() == netip.MustParseAddr("192.0.2.80")
	}) {
		t.Errorf("not covered: %v %v", after.DNSNames, after.IPAddresses)
	}
	if !after.PublicKey.(*ecdsa.PublicKey).Equal(before.PublicKey) {
		t.Error("key changed")
	}
	if srv.State().CertFingerprint != certFingerprint(after.Raw) {
		t.Error("state fingerprint is stale")
	}
	// Removing the name doesn't reissue (the set didn't gain one).
	st.UI.ExtraHostnames = nil
	body, _ = json.Marshal(st)
	do(t, c, "PUT", srv.url("/api/settings"), string(body))
	if again := servedLeaf(t, srv); !again.Equal(after) {
		t.Error("reissued on removal")
	}
}

func servedLeaf(t *testing.T, srv *testServer) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", srv.State().Addr, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // reading the served cert
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0]
}

func TestTrustedHosts(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	c := httpClient()
	r := do(t, c, "GET", srv.url("/api/stats"), "", "Host: proxy.example.net")
	wantError(t, r, http.StatusForbidden, `Host "proxy.example.net" isn't allowed`)
	if !strings.Contains(string(r.body), "ui.trusted_hosts") {
		t.Errorf("no hint: %s", r.body)
	}

	st := srv.b.settings
	st.UI.TrustedHosts = []string{"proxy.example.net", "198.51.100.4:8443", "[2001:db8::4]"}
	body, _ := json.Marshal(st)
	if r := do(t, c, "PUT", srv.url("/api/settings"), string(body)); r.code != 200 {
		t.Fatalf("PUT %d %s", r.code, r.body)
	}
	for host, code := range map[string]int{
		"proxy.example.net":                          200,
		"PROXY.example.net.:443":                     200,
		"proxy.example.net:80":                       200,
		"198.51.100.4:8443":                          200,
		"198.51.100.4":                               403,
		"198.51.100.4:443":                           403,
		"[2001:db8::4]:443":                          200,
		"[2001:db8:0::4]":                            200,
		"other.example.net":                          403,
		"proxy.example.net.evil":                     403,
		"evil.proxy.example.net":                     403,
		"localhost:" + strconv.Itoa(srv.boundPort()): 200,
	} {
		if r := do(t, c, "GET", srv.url("/api/stats"), "", "Host: "+host); r.code != code {
			t.Errorf("%s: %d, want %d", host, r.code, code)
		}
	}
}

func TestSettingsETag(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	c := httpClient()
	r := do(t, c, "GET", srv.url("/api/settings"), "")
	etag := r.header.Get("ETag")
	if len(etag) < 10 || etag[0] != '"' {
		t.Fatalf("ETag %q", etag)
	}
	if r2 := do(t, c, "GET", srv.url("/api/settings"), ""); r2.header.Get("ETag") != etag {
		t.Error("ETag not stable")
	}
	var st Settings
	json.Unmarshal(r.body, &st)
	st.Quiet.QuietAfterSeconds++
	body, _ := json.Marshal(st)
	r = do(t, c, "PUT", srv.url("/api/settings"), string(body), "If-Match: "+etag)
	if r.code != 200 || r.header.Get("ETag") == etag || r.header.Get("ETag") == "" {
		t.Fatalf("PUT %d %q %s", r.code, r.header.Get("ETag"), r.body)
	}
	next := r.header.Get("ETag")
	if g := do(t, c, "GET", srv.url("/api/settings"), ""); g.header.Get("ETag") != next {
		t.Errorf("PUT ETag %q, GET %q", next, g.header.Get("ETag"))
	}
	// The old ETag is stale now.
	st.Quiet.QuietAfterSeconds++
	body, _ = json.Marshal(st)
	r = do(t, c, "PUT", srv.url("/api/settings"), string(body), "If-Match: "+etag)
	wantError(t, r, http.StatusConflict, "settings changed since you loaded them")
	if srv.b.settings.Quiet.QuietAfterSeconds != st.Quiet.QuietAfterSeconds-1 {
		t.Error("a stale PUT saved")
	}
	// A list, a weak tag and "*" match; no If-Match is accepted.
	for _, mk := range []func(string) string{
		func(e string) string { return `"x", ` + e },
		func(e string) string { return "W/" + e },
		func(string) string { return "*" },
	} {
		h := mk(next)
		r = do(t, c, "PUT", srv.url("/api/settings"), string(body), "If-Match: "+h)
		if r.code != 200 {
			t.Errorf("If-Match %s: %d %s", h, r.code, r.body)
		}
		next = r.header.Get("ETag")
	}
	if r = do(t, c, "PUT", srv.url("/api/settings"), string(body)); r.code != 200 {
		t.Errorf("no If-Match: %d", r.code)
	}
	// The backend re-checks under its lock: a change between the handler's check
	// and the save still loses.
	srv.b.mu.Lock()
	cur := srv.b.settings
	srv.b.mu.Unlock()
	if _, err := srv.b.SaveSettings(t.Context(), cur, `"stale"`); err != ErrSettingsChanged {
		t.Errorf("backend: %v", err)
	}
}

func TestSettingsRestartingFlag(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	c := httpClient()
	st := srv.b.settings
	st.Quiet.QuietAfterSeconds++
	body, _ := json.Marshal(st)
	r := do(t, c, "PUT", srv.url("/api/settings"), string(body))
	if m := r.json(t); r.code != 200 || m["restarting"] != nil {
		t.Errorf("no UI change: %d %v", r.code, m["restarting"])
	}
	old := srv.State()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	st.UI.Port = l.Addr().(*net.TCPAddr).Port
	l.Close()
	body, _ = json.Marshal(st)
	r = do(t, c, "PUT", srv.url("/api/settings"), string(body))
	if m := r.json(t); r.code != 200 || m["restarting"] != true || m["ui"] == nil {
		t.Errorf("port change: %d %s", r.code, r.body)
	}
	srv.waitRestart(t, false, old)
}
