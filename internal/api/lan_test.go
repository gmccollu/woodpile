package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// setPasswordLocal sets the first UI password through the API in localhost
// mode. That turns on require_login_local, so it returns a localhost client
// logged in with the new password.
func setPasswordLocal(t *testing.T, srv *testServer, pw string) *http.Client {
	t.Helper()
	r := do(t, httpClient(), "PUT", srv.url("/api/password"), fmt.Sprintf(`{"new": %q}`, pw))
	if r.code != 204 {
		t.Fatalf("set password: %d %s", r.code, r.body)
	}
	c := jarClient()
	login(t, srv, c, pw)
	return c
}

// lanServer starts in localhost mode, sets a password, then turns LAN mode on
// through PUT /api/settings, as the UI would.
func lanServer(t *testing.T, pw string) *testServer {
	t.Helper()
	srv := startServer(t, nil, UIConfig{})
	c := setPasswordLocal(t, srv, pw)
	old := srv.State()
	srv.b.mu.Lock()
	s := srv.b.settings
	srv.b.mu.Unlock()
	s.UI.LANMode, s.UI.Port = true, 0
	body, _ := json.Marshal(s)
	if r := do(t, c, "PUT", srv.url("/api/settings"), string(body)); r.code != 200 {
		t.Fatalf("enable LAN: %d %s", r.code, r.body)
	}
	srv.waitRestart(t, true, old)
	return srv
}

func login(t *testing.T, srv *testServer, c *http.Client, pw string) SessionInfo {
	t.Helper()
	r := do(t, c, "POST", srv.url("/api/login"), fmt.Sprintf(`{"password": %q}`, pw))
	if r.code != 200 {
		t.Fatalf("login: %d %s", r.code, r.body)
	}
	var info SessionInfo
	if err := json.Unmarshal(r.body, &info); err != nil {
		t.Fatal(err)
	}
	if info.SessionSecret == "" {
		t.Fatalf("login gave no session secret: %s", r.body)
	}
	withSecret(c, info.SessionSecret)
	return info
}

// headerTransport adds the session secret header to every request, as the UI
// does from localStorage.
type headerTransport struct {
	base   http.RoundTripper
	secret string
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(SessionHeader, h.secret)
	return h.base.RoundTrip(r)
}

// withSecret makes c send secret in SessionHeader (replacing an earlier one).
func withSecret(c *http.Client, secret string) {
	base := c.Transport
	if h, ok := base.(headerTransport); ok {
		base = h.base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	c.Transport = headerTransport{base: base, secret: secret}
}

// withoutSecret is c's transport without the session header.
func withoutSecret(c *http.Client) *http.Client {
	cc := *c
	if h, ok := c.Transport.(headerTransport); ok {
		cc.Transport = h.base
	}
	return &cc
}

func TestLANRefusesWithoutPassword(t *testing.T) {
	// Directly: start refuses.
	s, err := New(Options{Backend: newFakeBackend(), DataDir: t.TempDir(), Static: testStatic, argon: testArgon, identity: testIdentity, lanAddr: "127.0.0.1", Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.start(UIConfig{LANMode: true}); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("start = %v, want ErrNoPassword", err)
	}
	if _, err := os.Stat(filepath.Join(s.dataDir, CertFile)); !os.IsNotExist(err) {
		t.Error("a certificate was generated without a password")
	}

	// Through Run: it falls back to localhost mode and says why.
	srv := startServer(t, nil, UIConfig{LANMode: true})
	st := srv.State()
	if st.LANMode || !errors.Is(st.Err, ErrNoPassword) || !strings.HasPrefix(st.Addr, "127.0.0.1:") {
		t.Fatalf("state %+v", st)
	}
	if r := do(t, httpClient(), "GET", srv.url("/api/status"), ""); r.code != 200 {
		t.Fatalf("fallback server: %d", r.code)
	}
	// No TLS on the port.
	conn, err := tls.Dial("tcp", st.Addr, &tls.Config{InsecureSkipVerify: true})
	if err == nil {
		conn.Close()
		t.Fatal("TLS handshake succeeded without a password")
	}
	found := false
	for _, e := range srv.b.eventTexts() {
		if strings.Contains(e, "LAN mode didn't start") && strings.HasPrefix(e, "ui/error") {
			found = true
		}
	}
	if !found {
		t.Errorf("no fallback event: %v", srv.b.eventTexts())
	}
	// The session route tells the UI.
	m := do(t, httpClient(), "GET", srv.url("/api/session"), "").json(t)
	if m["mode"] != "localhost" || m["password_set"] != false || m["auth_required"] != false {
		t.Errorf("session %v", m)
	}
}

func TestLANLoginLogoutSession(t *testing.T) {
	const pw = "correct horse battery"
	srv := lanServer(t, pw)
	c := tlsClient(t, srv.dataDir)

	// Unauthenticated: only login, session, healthz and the static UI.
	wantError(t, do(t, c, "GET", srv.url("/api/status"), ""), 401, "Log in")
	wantError(t, do(t, c, "GET", srv.url("/api/live"), ""), 401, "")
	wantError(t, do(t, c, "GET", srv.url("/api/search"), ""), 401, "")
	wantError(t, do(t, c, "PATCH", srv.url("/api/devices/10.1.0.1"), `{"name":"x"}`), 401, "")
	wantError(t, do(t, c, "GET", srv.url("/api/nope"), ""), 401, "")
	for _, p := range []string{"/healthz", "/", "/assets/index-BHAE32ir.js", "/devices"} {
		if r := do(t, c, "GET", srv.url(p), ""); r.code != 200 {
			t.Errorf("%s unauthenticated: %d", p, r.code)
		}
	}
	m := do(t, c, "GET", srv.url("/api/session"), "").json(t)
	if m["mode"] != "lan" || m["auth_required"] != true || m["authenticated"] != false || m["password_set"] != true {
		t.Fatalf("session before login: %v", m)
	}
	if _, ok := m["csrf_token"]; ok {
		t.Error("csrf_token is gone from the contract")
	}

	// A wrong password.
	wantError(t, do(t, c, "POST", srv.url("/api/login"), `{"password":"nope"}`), 401, "Wrong password")

	// Login sets a strict cookie and returns the session secret, once.
	r := do(t, c, "POST", srv.url("/api/login"), fmt.Sprintf(`{"password": %q}`, pw))
	if r.code != 200 {
		t.Fatalf("login %d %s", r.code, r.body)
	}
	var cookie string
	for _, v := range r.header.Values("Set-Cookie") {
		if strings.HasPrefix(v, SessionCookie+"=") {
			cookie = v
		}
	}
	if cookie == "" {
		t.Fatalf("Set-Cookie %v", r.header.Values("Set-Cookie"))
	}
	for _, want := range []string{SessionCookie + "=", "Path=/", "HttpOnly", "Secure", "SameSite=Strict"} {
		if !strings.Contains(cookie, want) {
			t.Errorf("cookie %q lacks %q", cookie, want)
		}
	}
	if strings.Contains(cookie, "Domain=") {
		t.Errorf("cookie has a Domain: %q", cookie)
	}
	var info SessionInfo
	_ = json.Unmarshal(r.body, &info)
	if !info.Authenticated || len(info.SessionSecret) < 32 {
		t.Fatalf("login body %s", r.body)
	}
	cookieVal := strings.SplitN(strings.TrimPrefix(cookie, SessionCookie+"="), ";", 2)[0]
	if cookieVal == info.SessionSecret || strings.Contains(cookie, info.SessionSecret) {
		t.Error("the secret is the cookie")
	}
	secretHdr := SessionHeader + ": " + info.SessionSecret

	// Cookie alone: 401, for reads and writes alike (a cookie caught by another
	// server on this host is useless). Header alone: 401. Both: 200.
	u := srv.url("/api/devices/10.1.0.1")
	plain := tlsClient(t, srv.dataDir)
	plain.Jar = nil
	cookieHdr := "Cookie: " + SessionCookie + "=" + cookieVal
	wantError(t, do(t, plain, "GET", srv.url("/api/status"), "", cookieHdr), 401, "")
	wantError(t, do(t, plain, "GET", srv.url("/api/live"), "", cookieHdr), 401, "")
	wantError(t, do(t, plain, "PATCH", u, `{"name":"x"}`, cookieHdr), 401, "")
	wantError(t, do(t, plain, "GET", srv.url("/api/status"), "", secretHdr), 401, "")
	wantError(t, do(t, plain, "GET", srv.url("/api/status"), "", cookieHdr, SessionHeader+": wrong"), 401, "")
	if r := do(t, plain, "GET", srv.url("/api/status"), "", cookieHdr, secretHdr); r.code != 200 {
		t.Fatalf("cookie and header: %d %s", r.code, r.body)
	}
	if m := do(t, plain, "GET", srv.url("/api/session"), "", cookieHdr).json(t); m["authenticated"] != false {
		t.Errorf("session with the cookie only: %v", m)
	}
	if m := do(t, plain, "GET", srv.url("/api/session"), "", cookieHdr, secretHdr).json(t); m["authenticated"] != true {
		t.Errorf("session with both: %v", m)
	}
	withSecret(c, info.SessionSecret)
	// The live tail takes the header too (the UI streams it with fetch()).
	{
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.url("/api/live"), nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Errorf("live with both: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		resp.Body.Close()
		cancel()
	}
	if r := do(t, c, "PATCH", u, `{"name":"x"}`); r.code != 200 {
		t.Errorf("PATCH with both: %d %s", r.code, r.body)
	}
	if r := do(t, c, "POST", srv.url("/api/listeners/retry"), ""); r.code != 204 {
		t.Errorf("retry with both: %d", r.code)
	}
	// The secret is never sent back.
	for _, p := range []string{"/api/session", "/api/status", "/api/settings", "/api/devices", "/api/stats", "/api/exports"} {
		if r := do(t, c, "GET", srv.url(p), ""); strings.Contains(string(r.body), info.SessionSecret) {
			t.Errorf("%s returns the secret", p)
		}
	}
	// Cross-origin is still refused with a valid session.
	wantError(t, do(t, c, "PATCH", u, `{"name":"y"}`, "Sec-Fetch-Site: cross-site"), 403, "Cross-origin")

	// A second login gets a new session: the old cookie and secret stop working.
	info2 := login(t, srv, c, pw)
	if info2.SessionSecret == info.SessionSecret {
		t.Error("secret reused across logins")
	}
	wantError(t, do(t, plain, "GET", srv.url("/api/status"), "", cookieHdr, secretHdr), 401, "")

	// A cookie alone (a stolen one) can't end the session.
	var cookie2 string
	for _, ck := range c.Jar.Cookies(mustURL(t, srv.url("/"))) {
		if ck.Name == SessionCookie {
			cookie2 = ck.Value
		}
	}
	both2 := []string{"Cookie: " + SessionCookie + "=" + cookie2, SessionHeader + ": " + info2.SessionSecret}
	if r := do(t, plain, "POST", srv.url("/api/logout"), "", both2[0]); r.code != 204 {
		t.Fatalf("cookie-only logout: %d", r.code)
	}
	if r := do(t, plain, "GET", srv.url("/api/status"), "", both2...); r.code != 200 {
		t.Fatalf("a cookie-only logout ended the session: %d", r.code)
	}

	// Logout with both ends the session and clears the cookie; neither the
	// cookie nor the secret works afterwards.
	r = do(t, c, "POST", srv.url("/api/logout"), "")
	if r.code != 204 {
		t.Fatalf("logout %d %s", r.code, r.body)
	}
	if sc := r.header.Get("Set-Cookie"); !strings.Contains(sc, "Max-Age=0") {
		t.Errorf("logout cookie %q", sc)
	}
	wantError(t, do(t, plain, "GET", srv.url("/api/status"), "", both2...), 401, "")
	wantError(t, do(t, c, "GET", srv.url("/api/status"), ""), 401, "")
	if m := do(t, c, "GET", srv.url("/api/session"), "").json(t); m["authenticated"] != false {
		t.Errorf("session after logout: %v", m)
	}
	// Logout without a session is harmless.
	if r := do(t, c, "POST", srv.url("/api/logout"), ""); r.code != 204 {
		t.Errorf("logout without session: %d", r.code)
	}
}

func TestLANSessionExpiry(t *testing.T) {
	srv := lanServer(t, "password-1")
	c := tlsClient(t, srv.dataDir)
	login(t, srv, c, "password-1")
	srv.clock.Add(sessionIdle - time.Minute)
	if r := do(t, c, "GET", srv.url("/api/status"), ""); r.code != 200 {
		t.Fatalf("before idle timeout: %d", r.code)
	}
	srv.clock.Add(sessionIdle + time.Minute)
	wantError(t, do(t, c, "GET", srv.url("/api/status"), ""), 401, "")
}

// TestLANBackoff: failures from one address back off exponentially, and the
// right password works as soon as the wait is over (no hard lockout).
func TestLANBackoff(t *testing.T) {
	const pw = "the-real-password"
	srv := lanServer(t, pw)
	c := tlsClient(t, srv.dataDir)
	var waits []string
	for i := 0; i < lockoutFailures; i++ {
		r := do(t, c, "POST", srv.url("/api/login"), `{"password":"guess"}`)
		for r.code == 429 {
			ra, _ := strconv.Atoi(r.header.Get("Retry-After"))
			if ra < 1 {
				t.Fatalf("Retry-After %q", r.header.Get("Retry-After"))
			}
			if strings.Contains(string(r.body), "failed attempts") {
				waits = append(waits, r.header.Get("Retry-After"))
			}
			srv.clock.Add(time.Duration(ra) * time.Second)
			r = do(t, c, "POST", srv.url("/api/login"), `{"password":"guess"}`)
		}
		if r.code != 401 {
			t.Fatalf("attempt %d: %d %s", i+1, r.code, r.body)
		}
	}
	// The waits after failures 5..9 double: 1, 2, 4, 8, 16 s.
	if strings.Join(waits, ",") != "1,2,4,8,16" {
		t.Errorf("backoff waits %v", waits)
	}
	r := do(t, c, "POST", srv.url("/api/login"), fmt.Sprintf(`{"password": %q}`, pw))
	wantError(t, r, 429, "Try again in 32 seconds")
	found := false
	for _, e := range srv.b.eventTexts() {
		if strings.HasPrefix(e, "auth/warn") && strings.Contains(e, "from 127.0.0.1 slowed down") {
			found = true
		}
	}
	if !found {
		t.Errorf("no backoff event: %v", srv.b.eventTexts())
	}
	srv.clock.Add(32 * time.Second)
	login(t, srv, c, pw) // the right password, once the wait is over
	// Success clears the backoff.
	if r := do(t, c, "POST", srv.url("/api/login"), `{"password":"guess"}`); r.code != 401 {
		t.Errorf("after success: %d", r.code)
	}
}

func TestLoginBurstLimit(t *testing.T) {
	const pw = "the-real-password"
	srv := lanServer(t, pw)
	c := tlsClient(t, srv.dataDir)
	c.Jar = nil                             // no remembered-client cookie: that would skip the bucket
	srv.clock.Add(loginBurst * loginRefill) // lanServer's own login took a token
	// Even right passwords: loginBurst quick attempts, then one per loginRefill.
	for i := range loginBurst {
		if r := do(t, c, "POST", srv.url("/api/login"), fmt.Sprintf(`{"password": %q}`, pw)); r.code != 200 {
			t.Fatalf("attempt %d: %d", i+1, r.code)
		}
	}
	r := do(t, c, "POST", srv.url("/api/login"), fmt.Sprintf(`{"password": %q}`, pw))
	wantError(t, r, 429, "Wait a moment")
	srv.clock.Add(loginRefill)
	if r := do(t, c, "POST", srv.url("/api/login"), fmt.Sprintf(`{"password": %q}`, pw)); r.code != 200 {
		t.Fatalf("after refill: %d", r.code)
	}
}

func TestLimiterKeys(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.1:5555":             "192.0.2.1/32",
		"[2001:db8:1:2:3:4:5:6]:443": "2001:db8:1:2::/64",
		"[2001:db8:1:2:ffff::1]:1":   "2001:db8:1:2::/64",
		"[::ffff:192.0.2.9]:80":      "192.0.2.9/32",
		"[fe80::1%en0]:80":           "fe80::/64",
	} {
		if got := clientKey(in).String(); got != want {
			t.Errorf("clientKey(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestPasswordChange(t *testing.T) {
	srv := lanServer(t, "first-password")
	c := tlsClient(t, srv.dataDir)
	info := login(t, srv, c, "first-password")
	other := tlsClient(t, srv.dataDir)
	login(t, srv, other, "first-password")

	withSecret(c, info.SessionSecret) // other's login replaced nothing on c, but be explicit
	wantError(t, do(t, c, "PUT", srv.url("/api/password"), `{"current":"wrong","new":"second-password"}`), 403, "Wrong password")
	r := do(t, c, "PUT", srv.url("/api/password"), `{"current":"first-password","new":"short"}`)
	if r.code != 400 || !strings.Contains(string(r.body), `"new"`) {
		t.Fatalf("short password: %d %s", r.code, r.body)
	}
	if r := do(t, c, "PUT", srv.url("/api/password"), `{"current":"first-password","new":"second-password"}`); r.code != 204 {
		t.Fatalf("change: %d %s", r.code, r.body)
	}
	// This session continues; the other one ended.
	if r := do(t, c, "GET", srv.url("/api/status"), ""); r.code != 200 {
		t.Errorf("own session after change: %d", r.code)
	}
	wantError(t, do(t, other, "GET", srv.url("/api/status"), ""), 401, "")
	srv.clock.Add(time.Minute)
	wantError(t, do(t, other, "POST", srv.url("/api/login"), `{"password":"first-password"}`), 401, "")
	login(t, srv, other, "second-password")
	// Unauthenticated password changes are refused in LAN mode.
	wantError(t, do(t, tlsClient(t, srv.dataDir), "PUT", srv.url("/api/password"), `{"current":"second-password","new":"third-password"}`), 401, "")
}

func TestPasswordFile(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	wantError(t, do(t, httpClient(), "PUT", srv.url("/api/password"), `{"new":"1234567"}`), 400, "At least")
	wantError(t, do(t, httpClient(), "PUT", srv.url("/api/password"), `{"new":"        "}`), 400, "")
	setPasswordLocal(t, srv, "a-long-password")
	path := filepath.Join(srv.dataDir, PasswordFile)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), "$argon2id$v=19$") || strings.Contains(string(b), "a-long-password") {
		t.Errorf("hash file %q", b)
	}
	if ok, err := verifyPassword("a-long-password", string(b)); !ok || err != nil {
		t.Errorf("verify: %v %v", ok, err)
	}
	if ok, _ := verifyPassword("a-long-passwore", string(b)); ok {
		t.Error("wrong password verified")
	}
	// No stray temp files.
	entries, _ := os.ReadDir(srv.dataDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
	// A new server reads it back.
	s2, err := New(Options{Backend: newFakeBackend(), DataDir: srv.dataDir, Static: testStatic})
	if err != nil {
		t.Fatal(err)
	}
	if !s2.auth.passwordSet() {
		t.Error("password not read back")
	}
}

func TestDefaultArgonParams(t *testing.T) {
	h, err := hashPassword("x-password", defaultArgon)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("hash %s", h)
	}
	if ok, err := verifyPassword("x-password", h); !ok || err != nil {
		t.Errorf("verify %v %v", ok, err)
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5", "$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5", "plain"} {
		if _, err := verifyPassword("x", bad); err == nil {
			t.Errorf("accepted bad hash %q", bad)
		}
	}
}

func TestCertGeneration(t *testing.T) {
	dir := t.TempDir()
	now := ts("2026-11-10T21:00:00Z")
	ips := []netip.Addr{netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("10.1.0.50"), netip.MustParseAddr("fd00::50")}
	identity := func() (hostIdentity, error) {
		return hostIdentity{Names: []string{"localhost", "logbox"}, IPs: slices.Clone(ips)}, nil
	}
	clock := now
	generated := 0
	cs := &certSource{dataDir: dir, now: func() time.Time { return clock }, identity: identity, onGenerate: func(string) { generated++ }}
	if _, err := cs.load(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{CertFile, KeyFile} {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", f, fi.Mode().Perm())
		}
	}
	leaf := readLeaf(t, dir)
	if leaf.PublicKeyAlgorithm != x509.ECDSA {
		t.Errorf("key algorithm %v", leaf.PublicKeyAlgorithm)
	}
	if !slices.Equal(leaf.DNSNames, []string{"localhost", "logbox"}) {
		t.Errorf("DNS SANs %v", leaf.DNSNames)
	}
	var got []string
	for _, ip := range leaf.IPAddresses {
		got = append(got, ip.String())
	}
	if !slices.Equal(got, []string{"127.0.0.1", "10.1.0.50", "fd00::50"}) {
		t.Errorf("IP SANs %v", got)
	}
	if v := leaf.NotAfter.Sub(now); v < certValidity-time.Hour || v > certValidity+time.Hour {
		t.Errorf("validity %v", v)
	}
	if !leaf.NotBefore.Before(now) {
		t.Errorf("NotBefore %v", leaf.NotBefore)
	}
	if !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) || leaf.IsCA {
		t.Errorf("usage %v CA %v", leaf.ExtKeyUsage, leaf.IsCA)
	}
	for _, h := range []string{"logbox", "localhost", "10.1.0.50", "fd00::50"} {
		if err := leaf.VerifyHostname(h); err != nil {
			t.Errorf("VerifyHostname(%s): %v", h, err)
		}
	}

	// A fresh source reuses the stored certificate while the IPs are the same.
	cs2 := &certSource{dataDir: dir, now: func() time.Time { return clock }, identity: identity, onGenerate: func(string) { generated++ }}
	if _, err := cs2.load(); err != nil {
		t.Fatal(err)
	}
	if generated != 1 || readLeaf(t, dir).SerialNumber.Cmp(leaf.SerialNumber) != 0 {
		t.Fatalf("regenerated with the same IPs (generated %d)", generated)
	}

	// A new IP regenerates it, after the recheck interval.
	ips = append(ips, netip.MustParseAddr("192.168.7.20"))
	if _, err := cs2.load(); err != nil {
		t.Fatal(err)
	}
	if generated != 1 {
		t.Fatal("rechecked before the interval")
	}
	clock = clock.Add(certRecheck + time.Second)
	if _, err := cs2.load(); err != nil {
		t.Fatal(err)
	}
	if generated != 2 {
		t.Fatal("not regenerated after an IP change")
	}
	if err := readLeaf(t, dir).VerifyHostname("192.168.7.20"); err != nil {
		t.Error(err)
	}

	// Near expiry regenerates too.
	clock = clock.Add(certValidity - certRenewBefore + time.Hour)
	if _, err := cs2.load(); err != nil {
		t.Fatal(err)
	}
	if generated != 3 {
		t.Fatal("not regenerated near expiry")
	}
}

func readLeaf(t *testing.T, dir string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, CertFile))
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		t.Fatal("no PEM")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLocalIdentity(t *testing.T) {
	id, err := localIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(id.Names, "localhost") || !slices.Contains(id.IPs, netip.MustParseAddr("127.0.0.1")) {
		t.Errorf("identity %+v", id)
	}
}

func TestUserCertificate(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM, err := generateCert(hostIdentity{Names: []string{"user.example"}, IPs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cf, kf := filepath.Join(dir, "my.crt"), filepath.Join(dir, "my.key")
	_ = os.WriteFile(cf, certPEM, 0o600)
	_ = os.WriteFile(kf, keyPEM, 0o600)

	srv := startServer(t, nil, UIConfig{})
	setPasswordLocal(t, srv, "password-123")
	old := srv.State()
	srv.Reconfigure(UIConfig{LANMode: true, CertFile: cf, KeyFile: kf})
	st := srv.waitRestart(t, true, old)
	if _, err := os.Stat(filepath.Join(srv.dataDir, CertFile)); !os.IsNotExist(err) {
		t.Error("generated a certificate despite a user one")
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	conn, err := tls.Dial("tcp", st.Addr, &tls.Config{RootCAs: pool, ServerName: "user.example"})
	if err != nil {
		t.Fatalf("handshake with the user certificate: %v", err)
	}
	conn.Close()

	// A bad user certificate falls back to localhost mode.
	old = srv.State()
	srv.Reconfigure(UIConfig{LANMode: true, CertFile: cf + ".missing", KeyFile: kf})
	st = srv.waitRestart(t, false, old)
	if st.Err == nil || !strings.Contains(st.Err.Error(), "certificate") {
		t.Errorf("state %+v", st)
	}
}

func TestLANRedirectsPlainHTTP(t *testing.T) {
	srv := lanServer(t, "password-123")
	st := srv.State()
	r := do(t, httpClient(), "GET", "http://"+st.Addr+"/api/status?x=1", "")
	if r.code != http.StatusMovedPermanently || r.header.Get("Location") != "https://"+st.Addr+"/api/status" {
		t.Fatalf("redirect %d %q", r.code, r.header.Get("Location"))
	}
	// A Host that isn't this machine is not reflected: the redirect goes to the
	// machine's own name on the listening port.
	_, port, _ := net.SplitHostPort(st.Addr)
	for _, h := range []string{"evil.example", "evil.example:" + port, "[2001:db8::66]:1"} {
		r := do(t, httpClient(), "GET", "http://"+st.Addr+"/x/../api/status?next=//evil.example", "", "Host: "+h)
		if loc := r.header.Get("Location"); r.code != 301 || loc != "https://testhost:"+port+"/x/../api/status" &&
			loc != "https://testhost:"+port+"/api/status" {
			t.Errorf("Host %s: %d %q", h, r.code, loc)
		}
	}
	checkSecurityHeaders(t, "redirect", r.header)
	wantError(t, do(t, httpClient(), "POST", "http://"+st.Addr+"/api/login", `{"password":"x"}`), 400, "HTTPS")
	// TLS still works on the same port.
	if r := do(t, tlsClient(t, srv.dataDir), "GET", srv.url("/healthz"), ""); r.code != 200 {
		t.Errorf("https: %d", r.code)
	}
	if !strings.HasPrefix(st.URL, "https://testhost:") {
		t.Errorf("URL %q", st.URL)
	}
}

func TestLANNoHostCheck(t *testing.T) {
	// LAN mode serves any name the user reaches it by.
	srv := lanServer(t, "password-123")
	c := tlsClient(t, srv.dataDir)
	if r := do(t, c, "GET", srv.url("/healthz"), "", "Host: logbox.lan:8514"); r.code != 200 {
		t.Errorf("%d", r.code)
	}
}

func TestModeSwitchRestarts(t *testing.T) {
	srv := lanServer(t, "password-123")
	c := tlsClient(t, srv.dataDir)
	login(t, srv, c, "password-123")
	lanState := srv.State()

	// Back to localhost mode through the settings PUT.
	srv.b.mu.Lock()
	s := srv.b.settings
	srv.b.mu.Unlock()
	s.UI.LANMode, s.UI.RequireLoginLocal = false, false // open localhost again
	body, _ := json.Marshal(s)
	r := do(t, c, "PUT", srv.url("/api/settings"), string(body))
	if r.code != 200 {
		t.Fatalf("PUT: %d %s", r.code, r.body)
	}
	st := srv.waitRestart(t, false, lanState)
	if !strings.HasPrefix(st.Addr, "127.0.0.1:") || !strings.HasPrefix(st.URL, "http://localhost:") {
		t.Errorf("state %+v", st)
	}
	if r := do(t, httpClient(), "GET", srv.url("/api/status"), ""); r.code != 200 {
		t.Errorf("localhost after switch: %d", r.code)
	}
	// The old LAN listener is gone (port 0 picked a new port, so the old one is free).
	if conn, err := net.DialTimeout("tcp", lanState.Addr, time.Second); err == nil {
		conn.Close()
		if lanState.Addr != st.Addr {
			t.Error("old listener still open")
		}
	}
	// An unchanged UI section doesn't restart.
	before := srv.State()
	if r := do(t, httpClient(), "PUT", srv.url("/api/settings"), string(body)); r.code != 200 {
		t.Fatal(r.code)
	}
	time.Sleep(100 * time.Millisecond)
	if srv.State() != before {
		t.Error("restarted without a UI change")
	}
}

func TestBindFailureRetries(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	b := newFakeBackend()
	s, err := New(Options{Backend: b, DataDir: t.TempDir(), Static: testStatic, UI: UIConfig{Port: port}, retryAfter: 20 * time.Millisecond, Logger: discardLogger()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(5 * time.Second)
	for s.State().Err == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if st := s.State(); st.Listening || st.Err == nil {
		t.Fatalf("state %+v", st)
	}
	ln.Close()
	for !s.State().Listening && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !s.State().Listening {
		t.Fatal("didn't recover once the port was free")
	}
	n := 0
	for _, e := range b.eventTexts() {
		if strings.Contains(e, "can't listen") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d bind-error events, want 1 (no repeats): %v", n, b.eventTexts())
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
