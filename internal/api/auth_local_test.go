package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func jarClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	c := httpClient()
	c.Jar = jar
	return c
}

// TestPasswordChangeNeedsCurrentOnLocalhost: security review M5. Any local user
// can reach localhost mode, so once a password is set, changing it needs the
// current one there too.
func TestPasswordChangeNeedsCurrentOnLocalhost(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	c := httpClient()
	if r := do(t, c, "PUT", srv.url("/api/password"), `{"new": "first password"}`); r.code != 204 {
		t.Fatalf("first set: %d %s", r.code, r.body)
	}
	r := do(t, c, "PUT", srv.url("/api/password"), `{"new": "second password"}`)
	wantError(t, r, 400, "current")
	if m := r.json(t); m["errors"].(map[string]any)["current"] == nil {
		t.Errorf("no errors.current: %s", r.body)
	}
	wantError(t, do(t, c, "PUT", srv.url("/api/password"), `{"current": "wrong", "new": "second password"}`), 403, "Wrong")
	if r := do(t, c, "PUT", srv.url("/api/password"), `{"current": "first password", "new": "second password"}`); r.code != 204 {
		t.Fatalf("change with current: %d %s", r.code, r.body)
	}
	if ok, _ := srv.auth.check(t.Context(), "second password"); !ok {
		t.Error("password not changed")
	}
}

func TestRequireLoginLocal(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	c := jarClient()
	putUI := func(require bool) resp {
		srv.b.mu.Lock()
		s := srv.b.settings
		srv.b.mu.Unlock()
		s.UI.RequireLoginLocal = require
		body, _ := json.Marshal(s)
		return do(t, c, "PUT", srv.url("/api/settings"), string(body))
	}
	// Not without a password.
	r := putUI(true)
	wantError(t, r, 400, "")
	if m := r.json(t); m["errors"].(map[string]any)["ui.require_login_local"] == nil {
		t.Fatalf("errors: %s", r.body)
	}
	setPasswordLocal(t, srv, "local secret pw")
	before := srv.State()
	if r := putUI(true); r.code != 200 {
		t.Fatalf("enable: %d %s", r.code, r.body)
	}
	// Applied at once, with no restart.
	if st := srv.State(); st.Addr != before.Addr || !st.Listening {
		t.Fatalf("restarted: %+v -> %+v", before, st)
	}
	anon := httpClient()
	wantError(t, do(t, anon, "GET", srv.url("/api/status"), ""), 401, "Log in")
	wantError(t, do(t, anon, "GET", srv.url("/api/search?q=x"), ""), 401, "")
	wantError(t, do(t, anon, "GET", srv.url("/api/live"), ""), 401, "")
	wantError(t, do(t, anon, "PUT", srv.url("/api/password"), `{"current": "local secret pw", "new": "taken over!"}`), 401, "")
	var info SessionInfo
	json.Unmarshal(do(t, anon, "GET", srv.url("/api/session"), "").body, &info)
	if info.Mode != "localhost" || !info.AuthRequired || info.Authenticated || !info.PasswordSet {
		t.Fatalf("session %+v", info)
	}
	if r := do(t, anon, "GET", srv.url("/healthz"), ""); r.code != 200 {
		t.Errorf("healthz %d", r.code)
	}
	if r := do(t, anon, "GET", srv.url("/"), ""); r.code != 200 {
		t.Errorf("static UI %d", r.code)
	}

	// Log in: the localhost cookie, then the same session and CSRF rules as LAN mode.
	wantError(t, do(t, c, "POST", srv.url("/api/login"), `{"password": "nope"}`), 401, "Wrong")
	r = do(t, c, "POST", srv.url("/api/login"), `{"password": "local secret pw"}`)
	if r.code != 200 {
		t.Fatalf("login %d %s", r.code, r.body)
	}
	ck := r.header.Get("Set-Cookie")
	if !strings.HasPrefix(ck, LocalSessionCookie+"=") || strings.Contains(ck, "Secure") || !strings.Contains(ck, "HttpOnly") ||
		!strings.Contains(ck, "SameSite=Strict") {
		t.Errorf("cookie %q", ck)
	}
	json.Unmarshal(r.body, &info)
	if !info.Authenticated || info.CSRFToken == "" {
		t.Fatalf("login info %+v", info)
	}
	if r := do(t, c, "GET", srv.url("/api/status"), ""); r.code != 200 {
		t.Fatalf("status with a session: %d", r.code)
	}
	wantError(t, do(t, c, "POST", srv.url("/api/listeners/retry"), ""), 403, "CSRF")
	if r := do(t, c, "POST", srv.url("/api/listeners/retry"), "", CSRFHeader+": "+info.CSRFToken); r.code != 204 {
		t.Errorf("retry with CSRF: %d %s", r.code, r.body)
	}
	// Turning it off again needs that session too, then localhost is open.
	srv.b.mu.Lock()
	s := srv.b.settings
	srv.b.mu.Unlock()
	s.UI.RequireLoginLocal = false
	body, _ := json.Marshal(s)
	if r := do(t, c, "PUT", srv.url("/api/settings"), string(body), CSRFHeader+": "+info.CSRFToken); r.code != 200 {
		t.Fatalf("disable: %d %s", r.code, r.body)
	}
	if r := do(t, anon, "GET", srv.url("/api/status"), ""); r.code != 200 {
		t.Errorf("after disabling: %d", r.code)
	}
	// Login without require_login_local is refused in localhost mode.
	wantError(t, do(t, anon, "POST", srv.url("/api/login"), `{"password": "local secret pw"}`), 400, "No login")
}

func newTestAuth(t *testing.T) (*auth, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	a, err := newAuth(t.TempDir(), testArgon, clock.Now, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	return a, clock
}

// failUntilBackoff fails from addr until it's backing off, stepping past the
// rate bucket.
func failUntilBackoff(t *testing.T, a *auth, clock *fakeClock, addr string) {
	t.Helper()
	for i := 0; i < backoffAfter; i++ {
		clock.Add(2 * time.Second)
		if l := a.allow(addr); !l.ok {
			t.Fatalf("%s attempt %d refused: %+v", addr, i, l)
		}
		a.fail(addr)
	}
	if l := a.allow(addr); l.ok || !l.locked {
		t.Fatalf("%s not backing off: %+v", addr, l)
	}
}

func TestLimiterBackoffIsPerFullAddress(t *testing.T) {
	a, clock := newTestAuth(t)
	// A neighbour in the same /64 doesn't slow the admin down...
	failUntilBackoff(t, a, clock, "[2001:db8:1:2::bad]:4000")
	if l := a.allow("[2001:db8:1:2::1]:443"); !l.ok {
		t.Errorf("admin in the same /64 refused: %+v", l)
	}
	// ...and every link-local client shares fe80::/64, so the zone counts too.
	failUntilBackoff(t, a, clock, "[fe80::bad%en0]:1")
	for _, other := range []string{"[fe80::1%en0]:1", "[fe80::bad%en1]:1"} {
		if l := a.allow(other); !l.ok {
			t.Errorf("%s refused: %+v", other, l)
		}
	}
	// The rate bucket is still per /64: a burst from one /64 is shared.
	a2, _ := newTestAuth(t)
	for i := 0; i < loginBurst; i++ {
		a2.allow(fmt.Sprintf("[2001:db8:9:9::%x]:1", i+1))
	}
	if l := a2.allow("[2001:db8:9:9::99]:1"); l.ok || l.locked {
		t.Errorf("burst not shared across the /64: %+v", l)
	}
}

func TestLimiterGlobalBudget(t *testing.T) {
	a, clock := newTestAuth(t)
	// 201 failures within a minute, from 201 addresses (each under its own limits).
	for i := 0; i <= globalFailures; i++ {
		addr := fmt.Sprintf("198.51.%d.%d:1", i/250, 1+i%250)
		if l := a.allow(addr); !l.ok {
			t.Fatalf("attempt %d refused early: %+v", i, l)
		}
		a.fail(addr)
		clock.Add(100 * time.Millisecond)
	}
	// Now everyone, including a fresh address, gets one attempt per 5 s.
	if l := a.allow("203.0.113.5:1"); !l.ok {
		t.Fatalf("first attempt under the budget refused: %+v", l)
	}
	if l := a.allow("203.0.113.6:1"); l.ok || l.retryAfter <= 0 || l.retryAfter > globalSpacing {
		t.Errorf("second attempt within 5 s: %+v", l)
	}
	clock.Add(globalSpacing)
	if l := a.allow("203.0.113.6:1"); !l.ok {
		t.Errorf("after 5 s: %+v", l)
	}
	// A quiet minute ends it.
	clock.Add(time.Minute + time.Second)
	for i := 0; i < 3; i++ {
		if l := a.allow(fmt.Sprintf("203.0.113.%d:1", 10+i)); !l.ok {
			t.Errorf("after the budget recovered: %+v", l)
		}
	}
}

func TestLimiterTableFullEvictsOldest(t *testing.T) {
	a, clock := newTestAuth(t)
	for i := 0; i < maxClients+50; i++ {
		addr := fmt.Sprintf("10.%d.%d.%d:1", i>>16&255, i>>8&255, i&255)
		clock.Add(301 * time.Millisecond) // under the global budget (200 failures a minute)
		if l := a.allow(addr); !l.ok {
			t.Fatalf("client %d refused with the table full: %+v", i, l)
		}
		a.fail(addr)
	}
	if len(a.buckets) > maxClients || len(a.addrs) > maxClients {
		t.Errorf("tables grew to %d buckets, %d addresses", len(a.buckets), len(a.addrs))
	}
	if _, ok := a.addrs[netip.MustParseAddr("10.0.0.0")]; ok {
		t.Error("the oldest address wasn't evicted")
	}
}
