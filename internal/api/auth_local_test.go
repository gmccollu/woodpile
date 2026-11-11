package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"os"
	"path/filepath"
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
	c := setPasswordLocal(t, srv, "first password")
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

// TestRequireLoginLocalOnlyOnFirstPassword: an install that already has a
// password keeps its require_login_local setting when the password changes.
func TestRequireLoginLocalOnlyOnFirstPassword(t *testing.T) {
	b := newFakeBackend()
	dir := t.TempDir()
	// An existing install: a password, and require_login_local off.
	pre, err := newAuth(dir, testArgon, time.Now, func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := pre.setPassword("existing password"); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t, b, UIConfig{}, func(o *Options) { o.DataDir = dir })
	if r := do(t, httpClient(), "GET", srv.url("/api/status"), ""); r.code != 200 {
		t.Fatalf("existing install now needs a login: %d", r.code)
	}
	r := do(t, httpClient(), "PUT", srv.url("/api/password"), `{"current": "existing password", "new": "changed password"}`)
	if r.code != 204 {
		t.Fatalf("change %d %s", r.code, r.body)
	}
	b.mu.Lock()
	on := b.settings.UI.RequireLoginLocal
	b.mu.Unlock()
	if on || srv.requireLocal.Load() {
		t.Error("a password change turned require_login_local on")
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
	// The first password turns it on by itself (docs/api.md, M5 default), at
	// once, with no restart.
	before := srv.State()
	setPasswordLocal(t, srv, "local secret pw")
	srv.b.mu.Lock()
	on := srv.b.settings.UI.RequireLoginLocal
	srv.b.mu.Unlock()
	if !on {
		t.Fatal("require_login_local not saved on")
	}
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

	// Log in: the localhost cookie, then the same session rules as LAN mode.
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
	if !info.Authenticated || info.SessionSecret == "" {
		t.Fatalf("login info %+v", info)
	}
	// The cookie alone is useless: another local user's server on another
	// localhost port receives this cookie, but never the secret (localStorage
	// is per origin). Replay it the way such a server would.
	stolen := "Cookie: " + LocalSessionCookie + "=" + strings.SplitN(strings.TrimPrefix(ck, LocalSessionCookie+"="), ";", 2)[0]
	wantError(t, do(t, anon, "GET", srv.url("/api/status"), "", stolen), 401, "")
	wantError(t, do(t, anon, "GET", srv.url("/api/search?q=x"), "", stolen), 401, "")
	wantError(t, do(t, anon, "POST", srv.url("/api/exports"), `{}`, stolen), 401, "")
	wantError(t, do(t, anon, "PUT", srv.url("/api/password"), `{"current":"x","new":"taken over!"}`, stolen), 401, "")
	if m := do(t, anon, "GET", srv.url("/api/session"), "", stolen).json(t); m["authenticated"] != false {
		t.Errorf("session with a stolen cookie: %v", m)
	}
	withSecret(c, info.SessionSecret)
	if r := do(t, c, "GET", srv.url("/api/status"), ""); r.code != 200 {
		t.Fatalf("status with cookie and secret: %d", r.code)
	}
	if r := do(t, c, "POST", srv.url("/api/listeners/retry"), ""); r.code != 204 {
		t.Errorf("retry with cookie and secret: %d %s", r.code, r.body)
	}
	// Turning it off again needs that session too, then localhost is open.
	srv.b.mu.Lock()
	s := srv.b.settings
	srv.b.mu.Unlock()
	s.UI.RequireLoginLocal = false
	body, _ := json.Marshal(s)
	if r := do(t, c, "PUT", srv.url("/api/settings"), string(body)); r.code != 200 {
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

// attemptsIn runs a guesser against a for d of fake time, as fast as the
// limiter allows, every attempt wrong, and returns how many it got. addr gives
// the source address of attempt i (to rotate addresses).
func attemptsIn(a *auth, clock *fakeClock, d time.Duration, addr func(i int) string) int {
	end := clock.Now().Add(d)
	n := 0
	for i := 0; clock.Now().Before(end); i++ {
		src := addr(i)
		if l := a.allow(src, false); l.ok {
			a.fail(src, false)
			n++
			clock.Add(time.Millisecond)
		} else {
			clock.Add(max(l.retryAfter, time.Millisecond))
		}
	}
	return n
}

// TestLimiterRotatingSlash64 reproduces security review N8: rotating through
// a /64 (a new address every attempt) got 3,600 guesses an hour with only the
// per-address backoff; the per-network backoff brings it under the old 40.
func TestLimiterRotatingSlash64(t *testing.T) {
	a, clock := newTestAuth(t)
	n := attemptsIn(a, clock, time.Hour, func(i int) string { return fmt.Sprintf("[2001:db8:1:2:%x:%x::1]:1", i>>16, i&0xffff) })
	t.Logf("rotating /64: %d attempts in an hour", n)
	if n > 40 {
		t.Errorf("%d attempts an hour from one rotating /64 (the review measured 3,600; before L2, 40)", n)
	}
	// One fixed address does no better.
	a2, clock2 := newTestAuth(t)
	if n := attemptsIn(a2, clock2, time.Hour, func(int) string { return "192.0.2.66:1" }); n > 40 {
		t.Errorf("one address: %d attempts an hour", n)
	}
}

// TestLimiterNeighbourCantStarveKnownAdmin reproduces N8's second finding: a
// neighbour in the admin's /64 guessing 10 times a second kept the admin out.
// A remembered admin browser skips the network limits.
func TestLimiterNeighbourCantStarveKnownAdmin(t *testing.T) {
	a, clock := newTestAuth(t)
	admin, neighbour := "[2001:db8:1:2::1]:443", "[2001:db8:1:2::bad]:4000"
	var adminOK, adminTries, unknownOK int
	for step := 0; step < 10*60*10; step++ { // 10 minutes in 100 ms steps
		a.allow(neighbour, false) // 10 attempts a second, all wrong when allowed
		a.fail(neighbour, false)
		if step%300 == 0 { // the admin tries every 30 s with the right password
			adminTries++
			if l := a.allow(admin, true); l.ok {
				adminOK++
				a.succeed(admin)
			}
			if l := a.allow("[2001:db8:1:2::2]:443", false); l.ok { // an admin browser that isn't remembered
				unknownOK++
				a.succeed("[2001:db8:1:2::2]:443")
			}
		}
		clock.Add(100 * time.Millisecond)
	}
	if adminOK != adminTries {
		t.Errorf("the remembered admin got in %d of %d times", adminOK, adminTries)
	}
	t.Logf("remembered admin %d/%d; a browser that isn't remembered %d/%d", adminOK, adminTries, unknownOK, adminTries)
	// A remembered client still has its own address's backoff.
	for i := 0; i < backoffAfter; i++ {
		a.allow(admin, true)
		a.fail(admin, true)
	}
	if l := a.allow(admin, true); l.ok || !l.locked {
		t.Errorf("remembered client without its own backoff: %+v", l)
	}
}

func TestLimiterBackoffIsPerFullAddress(t *testing.T) {
	a, clock := newTestAuth(t)
	// Link-local clients all share fe80::/64; with the zone kept, a remembered
	// client on another zone or address isn't held back by another's failures.
	for i := 0; i < backoffAfter; i++ {
		clock.Add(2 * time.Second)
		a.allow("[fe80::bad%en0]:1", true)
		a.fail("[fe80::bad%en0]:1", true)
	}
	if l := a.allow("[fe80::bad%en0]:1", true); l.ok {
		t.Error("no backoff for the failing address")
	}
	for _, other := range []string{"[fe80::1%en0]:1", "[fe80::bad%en1]:1"} {
		if l := a.allow(other, true); !l.ok {
			t.Errorf("%s refused: %+v", other, l)
		}
	}
	// The rate bucket is per /64: a burst from one /64 is shared.
	a2, _ := newTestAuth(t)
	for i := 0; i < loginBurst; i++ {
		a2.allow(fmt.Sprintf("[2001:db8:9:9::%x]:1", i+1), false)
	}
	if l := a2.allow("[2001:db8:9:9::99]:1", false); l.ok || l.locked {
		t.Errorf("burst not shared across the /64: %+v", l)
	}
}

func TestLimiterGlobalBudget(t *testing.T) {
	a, clock := newTestAuth(t)
	// 51 failures within an hour, each from its own network.
	for i := 0; i <= globalFailures; i++ {
		addr := fmt.Sprintf("198.51.%d.%d:1", i/250, 1+i%250)
		if l := a.allow(addr, false); !l.ok {
			t.Fatalf("attempt %d refused early: %+v", i, l)
		}
		a.fail(addr, false)
		clock.Add(time.Minute)
	}
	// Now every attempt, from anywhere new, is 60 s apart...
	if l := a.allow("203.0.113.5:1", false); !l.ok {
		t.Fatalf("first attempt under the budget refused: %+v", l)
	}
	if l := a.allow("203.0.113.6:1", false); l.ok || l.retryAfter <= 0 || l.retryAfter > globalSpacing {
		t.Errorf("second attempt within 60 s: %+v", l)
	}
	// ...except a remembered client's.
	if l := a.allow("203.0.113.7:1", true); !l.ok {
		t.Errorf("remembered client held by the global budget: %+v", l)
	}
	clock.Add(globalSpacing)
	if l := a.allow("203.0.113.6:1", false); !l.ok {
		t.Errorf("after 60 s: %+v", l)
	}
	// A quiet hour ends it.
	clock.Add(globalWindow + time.Second)
	for i := 0; i < 3; i++ {
		if l := a.allow(fmt.Sprintf("203.0.113.%d:1", 10+i), false); !l.ok {
			t.Errorf("after the budget recovered: %+v", l)
		}
	}
}

func TestLimiterTableFullEvictsOldest(t *testing.T) {
	a, clock := newTestAuth(t)
	for i := 0; i < maxClients+50; i++ {
		addr := fmt.Sprintf("10.%d.%d.%d:1", i>>16&255, i>>8&255, i&255)
		clock.Add(time.Millisecond)
		if l := a.allow(addr, false); !l.ok { // fills the buckets
			t.Fatalf("client %d refused with the table full: %+v", i, l)
		}
		a.fail(addr, true) // fills the address table (known: no global budget)
	}
	if len(a.buckets) > maxClients || len(a.addrs) > maxClients || len(a.nets) > maxClients {
		t.Errorf("tables grew to %d buckets, %d addresses, %d networks", len(a.buckets), len(a.addrs), len(a.nets))
	}
	if _, ok := a.addrs[netip.MustParseAddr("10.0.0.0")]; ok {
		t.Error("the oldest address wasn't evicted")
	}
}

// TestKnownClientCookie: a login sets the remembered-client cookie (hashed on
// the server, kept across a restart), and an attempt that carries it isn't held
// by the network or global limits.
func TestKnownClientCookie(t *testing.T) {
	srv := startServer(t, nil, UIConfig{})
	c := setPasswordLocal(t, srv, "known client pw")
	var tok string
	for _, ck := range c.Jar.Cookies(mustURL(t, srv.url("/"))) {
		if ck.Name == knownClientCookie {
			tok = ck.Value
		}
	}
	if tok == "" {
		t.Fatal("no remembered-client cookie after login")
	}
	b, err := os.ReadFile(filepath.Join(srv.dataDir, knownClientsFile))
	if err != nil || strings.Contains(string(b), tok) || len(b) == 0 {
		t.Fatalf("stored %q (%v)", b, err)
	}
	if again := readKnownClients(srv.dataDir, srv.clock.Now()); len(again) != 1 {
		t.Errorf("after a restart: %d remembered", len(again))
	}
	// Exhaust the global budget from other networks; the remembered browser
	// still gets in, an unknown one is spaced out.
	for i := 0; i <= globalFailures; i++ {
		addr := fmt.Sprintf("198.51.100.%d:1", i)
		srv.auth.allow(addr, false)
		srv.auth.fail(addr, false)
	}
	srv.auth.allow("203.0.113.1:1", false) // uses the next global slot
	if r := do(t, httpClient(), "POST", srv.url("/api/login"), `{"password": "known client pw"}`); r.code != 429 {
		t.Errorf("unknown browser during the budget: %d", r.code)
	}
	if r := do(t, c, "POST", srv.url("/api/login"), `{"password": "known client pw"}`); r.code != 200 {
		t.Errorf("remembered browser during the budget: %d %s", r.code, r.body)
	}
}

func TestDownloadTickets(t *testing.T) {
	b := newFakeBackend()
	b.exports = map[string]ExportInfo{
		"exp_00000000000000aa": {ID: "exp_00000000000000aa", State: "ready", From: ts("2026-11-10T00:00:00Z"), To: ts("2026-11-10T01:00:00Z")},
		"exp_00000000000000bb": {ID: "exp_00000000000000bb", State: "ready", From: ts("2026-11-10T00:00:00Z"), To: ts("2026-11-10T01:00:00Z")},
	}
	b.zip = []byte("PK\x03\x04 the zip")
	srv := startServer(t, b, UIConfig{})
	a := setPasswordLocal(t, srv, "ticket password")
	other := jarClient()
	login(t, srv, other, "ticket password")
	ticketFor := func(c *http.Client, id string) string {
		t.Helper()
		r := do(t, c, "POST", srv.url("/api/exports/"+id+"/ticket"), "")
		if r.code != 200 {
			t.Fatalf("ticket: %d %s", r.code, r.body)
		}
		u := r.json(t)["url"].(string)
		if !strings.HasPrefix(u, "/api/exports/"+id+"/download?ticket=") {
			t.Fatalf("url %q", u)
		}
		return u
	}
	// What a browser's <a href download> sends: the cookie, no header.
	browser := withoutSecret(a)
	get := func(c *http.Client, u string) resp { return do(t, c, "GET", srv.url(u), "") }

	// Without a ticket the cookie alone isn't enough (the original N3 failure).
	wantError(t, get(browser, "/api/exports/exp_00000000000000aa/download"), 401, "")
	// A ticket works once.
	u := ticketFor(a, "exp_00000000000000aa")
	if r := get(browser, u); r.code != 200 || string(r.body) != "PK\x03\x04 the zip" {
		t.Fatalf("download with ticket: %d %q", r.code, r.body)
	}
	wantError(t, get(browser, u), 401, "already used")
	// Expired after 60 s.
	u = ticketFor(a, "exp_00000000000000aa")
	srv.clock.Add(ticketTTL + time.Second)
	wantError(t, get(browser, u), 401, "")
	// Another session's ticket.
	u = ticketFor(other, "exp_00000000000000aa")
	wantError(t, get(browser, u), 401, "")
	// Another export's ticket.
	u = ticketFor(a, "exp_00000000000000bb")
	wantError(t, get(browser, strings.Replace(u, "exp_00000000000000bb", "exp_00000000000000aa", 1)), 401, "")
	// A ticket without the cookie.
	u = ticketFor(a, "exp_00000000000000aa")
	wantError(t, get(httpClient(), u), 401, "")
	// Unknown export: no ticket.
	wantError(t, do(t, a, "POST", srv.url("/api/exports/exp_00000000000000cc/ticket"), ""), 404, "")
	// Asking for a ticket needs the header like any route.
	wantError(t, do(t, browser, "POST", srv.url("/api/exports/exp_00000000000000aa/ticket"), ""), 401, "")
	// Tickets are kept hashed.
	srv.auth.mu.Lock()
	for k := range srv.auth.tickets {
		if strings.Contains(u, string(k[:])) {
			t.Error("ticket stored in the clear")
		}
	}
	srv.auth.mu.Unlock()
}
