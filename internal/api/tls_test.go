package api

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTLS is the fake backend's TLS side (kept apart from fakeBackend's fields
// so the shared helpers don't change).
type fakeTLS struct {
	mu       sync.Mutex
	state    TLSState
	caDER    []byte
	caErr    error
	renewErr error
	renewals []bool
	failures []TLSFailure
}

var fakeTLSOf sync.Map // *fakeBackend -> *fakeTLS

func (b *fakeBackend) tls() *fakeTLS {
	if v, ok := fakeTLSOf.Load(b); ok {
		return v.(*fakeTLS)
	}
	ft := &fakeTLS{
		state: TLSState{
			Enabled: true, Port: 6514, Source: "woodshed",
			CA: &TLSCA{Subject: "CN=Woodshed CA logs-01 2026-11-13", NotBefore: ts("2026-11-13T15:00:00Z"),
				NotAfter: ts("2037-12-31T23:59:59Z"), SHA256: "AB:CD", SHA1: "EF:01"},
			Cert: &TLSCert{Subject: "CN=logs-01.example.net", Issuer: "CN=Woodshed CA logs-01 2026-11-13",
				NotBefore: ts("2026-11-13T15:00:00Z"), NotAfter: ts("2036-11-13T15:00:00Z"), SHA256: "12:34",
				DNSNames: []string{"localhost", "logs-01"}, IPAddresses: []string{"127.0.0.1"}},
			Firewall: TLSFirewall{State: "unknown"},
		},
		caDER: []byte{0x30, 0x82, 0x01, 0x02},
		failures: []TLSFailure{{IP: "198.51.100.7", Count24h: 144, LastSeen: ts("2026-11-13T15:10:00Z"), Reason: "cert_rejected",
			Detail: "remote error: tls: unknown certificate authority"}},
	}
	v, _ := fakeTLSOf.LoadOrStore(b, ft)
	return v.(*fakeTLS)
}

func (b *fakeBackend) TLS(context.Context) (TLSState, error) {
	f := b.tls()
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}

func (b *fakeBackend) RenewTLS(_ context.Context, newCA bool) (TLSState, error) {
	f := b.tls()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renewErr != nil {
		return TLSState{}, f.renewErr
	}
	f.renewals = append(f.renewals, newCA)
	return f.state, nil
}

func (b *fakeBackend) TLSCA(context.Context) ([]byte, error) {
	f := b.tls()
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caDER, f.caErr
}

func (b *fakeBackend) TLSFailures(context.Context) ([]TLSFailure, error) {
	f := b.tls()
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failures, nil
}

func TestTLSRoutes(t *testing.T) {
	b := newFakeBackend()
	f := b.tls()
	srv := startServer(t, b, UIConfig{})
	c := httpClient()

	m := do(t, c, "GET", srv.url("/api/tls"), "").json(t)
	if m["source"] != "woodshed" || m["ca"] == nil || m["firewall"].(map[string]any)["state"] != "unknown" {
		t.Errorf("GET /api/tls: %v", m)
	}
	if mn, ok := m["cert"].(map[string]any)["missing_names"].([]any); !ok || len(mn) != 0 {
		t.Errorf("missing_names = %v, want []", m["cert"])
	}

	// Renew: the body's new_ca reaches the backend; a user certificate is a 409.
	for _, body := range []string{`{"new_ca": false}`, `{"new_ca": true}`} {
		if r := do(t, c, "POST", srv.url("/api/tls/renew"), body); r.code != 200 {
			t.Errorf("renew %s: %d %s", body, r.code, r.body)
		}
	}
	if r := do(t, c, "POST", srv.url("/api/tls/renew"), `{"new_ca": "yes"}`); r.code != 400 {
		t.Errorf("bad body: %d", r.code)
	}
	if r := do(t, c, "GET", srv.url("/api/tls/renew"), ""); r.code != 405 {
		t.Errorf("GET renew: %d", r.code)
	}
	f.mu.Lock()
	if fmt.Sprint(f.renewals) != "[false true]" {
		t.Errorf("renewals %v", f.renewals)
	}
	f.renewErr = ErrTLSUserCert
	f.mu.Unlock()
	if r := do(t, c, "POST", srv.url("/api/tls/renew"), `{"new_ca": false}`); r.code != 409 || !strings.Contains(string(r.body), "own certificate") {
		t.Errorf("renew with a user certificate: %d %s", r.code, r.body)
	}

	// Downloads.
	r := do(t, c, "GET", srv.url("/api/tls/ca.pem"), "")
	blk, _ := pem.Decode(r.body)
	if r.code != 200 || r.header.Get("Content-Type") != "application/x-pem-file" || blk == nil || blk.Type != "CERTIFICATE" ||
		string(blk.Bytes) != string(f.caDER) || !strings.Contains(r.header.Get("Content-Disposition"), `filename="woodshed-ca.pem"`) {
		t.Errorf("ca.pem: %d %v %q", r.code, r.header, r.body)
	}
	r = do(t, c, "GET", srv.url("/api/tls/ca.der"), "")
	if r.code != 200 || r.header.Get("Content-Type") != "application/pkix-cert" || string(r.body) != string(f.caDER) ||
		!strings.Contains(r.header.Get("Content-Disposition"), `filename="woodshed-ca.cer"`) {
		t.Errorf("ca.der: %d %v", r.code, r.header)
	}
	for _, e := range []struct {
		err  error
		text string
	}{{ErrTLSUserCert, "whoever issued it"}, {ErrNoTLSCA, "never been turned on"}} {
		f.mu.Lock()
		f.caErr = e.err
		f.mu.Unlock()
		for _, p := range []string{"/api/tls/ca.pem", "/api/tls/ca.der"} {
			if r := do(t, c, "GET", srv.url(p), ""); r.code != 404 || !strings.Contains(string(r.body), e.text) {
				t.Errorf("%s with %v: %d %s", p, e.err, r.code, r.body)
			}
		}
	}

	// Failures: sorted, capped, [] when none.
	f.mu.Lock()
	f.failures = nil
	for i := range 250 {
		f.failures = append(f.failures, TLSFailure{IP: fmt.Sprintf("198.51.100.%d", i), Count24h: int64(i % 40),
			LastSeen: ts("2026-11-13T00:00:00Z").Add(time.Duration(i) * time.Second), Reason: "timeout"})
	}
	f.mu.Unlock()
	var fs []TLSFailure
	json.Unmarshal(do(t, c, "GET", srv.url("/api/tls/failures"), "").body, &fs)
	if len(fs) != MaxTLSFailures || fs[0].Count24h != 39 {
		t.Errorf("failures: %d, first %+v", len(fs), fs[0])
	}
	f.mu.Lock()
	f.failures = nil
	f.mu.Unlock()
	if r := do(t, c, "GET", srv.url("/api/tls/failures"), ""); strings.TrimSpace(string(r.body)) != "[]" {
		t.Errorf("empty failures = %s", r.body)
	}
	_ = http.StatusOK
}

func TestPrimaryName(t *testing.T) {
	for _, c := range []struct {
		host    string
		domains []string
		want    string
	}{
		{"logs-01", []string{"example.net", "corp.example"}, "logs-01.example.net"},
		{"logs-01", nil, "logs-01"},
		{"logs-01.example.org", []string{"example.net"}, "logs-01.example.org"},
		{"", []string{"example.net"}, ""},
	} {
		if got := primaryName(c.host, c.domains); got != c.want {
			t.Errorf("primaryName(%q, %v) = %q, want %q", c.host, c.domains, got, c.want)
		}
	}
	host, primary, names, ips, err := HostIdentity([]string{"edge-logs.example.net", "192.0.2.9"})
	if err != nil || len(names) == 0 || len(ips) == 0 {
		t.Fatalf("HostIdentity: %v", err)
	}
	if !slices.Contains(names, "edge-logs.example.net") || !slices.Contains(names, "localhost") {
		t.Errorf("names %v", names)
	}
	if host != "" && primary == "" {
		t.Errorf("primary empty for host %q", host)
	}
}
