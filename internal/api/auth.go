package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A session is two secrets (docs/api.md, "Session secret"): a cookie, and a
// secret the page keeps in localStorage and sends as SessionHeader. Every
// authenticated request needs both. The cookie isn't port-scoped, so another
// server on the same host (another local user's) could receive it; localStorage
// is scoped to the exact origin, and a custom header can't be sent cross-site,
// so the header also does a CSRF token's job.
//
// In LAN mode (HTTPS) the cookie's __Host- prefix makes the browser insist on
// Secure, Path=/ and no Domain. With require_login_local the UI is plain
// http://localhost, where browsers don't all keep Secure cookies, so that
// cookie is HttpOnly and SameSite=Strict but not Secure.
const (
	SessionCookie      = "__Host-woodshed_session"
	LocalSessionCookie = "woodshed_session"
	SessionHeader      = "X-Woodshed-Session"
)

// Session lifetimes and limits.
const (
	sessionIdle     = 12 * time.Hour
	sessionAbsolute = 7 * 24 * time.Hour
	maxSessions     = 256
)

// Login limits (SPEC §5, security review L2 and N8):
//   - A rate bucket per client network (an IPv4 address, an IPv6 /64, since a
//     host can pick any address in its /64): loginBurst quick attempts, then one
//     per loginRefill.
//   - Exponential backoff, after backoffAfter failures within failWindow, both
//     per network (so rotating through a /64 doesn't buy attempts) and per full
//     address (zone included: link-local clients all share fe80::/64). Each
//     further failure doubles the wait, up to backoffMax. There's no hard
//     lockout: once the wait is over, the right password works.
//   - A global budget: more than globalFailures failures an hour across all
//     clients spaces every attempt globalSpacing apart.
//   - A remembered client (the knownClientCookie a successful login sets, kept
//     knownClientTTL) skips the network and global limits, but not its own
//     address's backoff, so a neighbour or a flood can't keep the admin out.
//
// Each table keeps at most maxClients entries and evicts the least recently
// seen when full.
const (
	loginBurst      = 5
	loginRefill     = time.Second // one attempt per second after the burst
	backoffAfter    = 5
	backoffMax      = 15 * time.Minute
	failWindow      = 15 * time.Minute
	lockoutFailures = 10 // failures that record a warning event
	globalFailures  = 50
	globalWindow    = time.Hour
	globalSpacing   = 60 * time.Second
	maxClients      = 4096
	maxHashers      = 2 // concurrent Argon2id verifications (each uses Memory KiB)

	knownClientCookie = "woodshed_known_client"
	knownClientTTL    = 90 * 24 * time.Hour
	maxKnownClients   = 1000
	knownClientsFile  = "ui-known-clients"
)

type session struct {
	secret   [32]byte // SHA-256 of the session secret (the header); the secret itself isn't kept
	created  time.Time
	lastSeen time.Time
}

// bucket is a client network's rate bucket.
type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// addrState is one address's or network's failures and backoff.
type addrState struct {
	failures    int
	lastFail    time.Time
	nextAllowed time.Time
	lastSeen    time.Time
}

// auth holds the password state, the sessions and the login limiter. It
// outlives UI server restarts.
type auth struct {
	dataDir string
	argon   argonParams
	now     func() time.Time
	record  func(Event)

	hashers chan struct{}

	mu       sync.Mutex
	tickets  map[[32]byte]*ticket  // download tickets, by SHA-256 of the token
	hash     string                // cached copy of the hash file; "" when unset
	sessions map[[32]byte]*session // keyed by SHA-256 of the cookie value
	buckets  map[netip.Prefix]*bucket
	nets     map[netip.Prefix]*addrState // backoff per network
	addrs    map[netip.Addr]*addrState   // backoff per full address
	recent   []time.Time                 // failure times in the last globalWindow, all clients (at most globalFailures+1 kept)
	globalAt time.Time                   // when the global budget next allows an attempt
	known    map[[32]byte]time.Time      // remembered clients: SHA-256 of the token -> expiry
}

func newAuth(dataDir string, p argonParams, now func() time.Time, record func(Event)) (*auth, error) {
	h, err := readHash(dataDir)
	if err != nil {
		return nil, fmt.Errorf("read the UI password: %w", err)
	}
	return &auth{
		dataDir:  dataDir,
		argon:    p,
		now:      now,
		record:   record,
		hashers:  make(chan struct{}, maxHashers),
		hash:     h,
		sessions: map[[32]byte]*session{},
		tickets:  map[[32]byte]*ticket{},
		buckets:  map[netip.Prefix]*bucket{},
		nets:     map[netip.Prefix]*addrState{},
		addrs:    map[netip.Addr]*addrState{},
		known:    readKnownClients(dataDir, now()),
	}, nil
}

func (a *auth) passwordSet() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hash != ""
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func tokenKey(tok string) [32]byte { return sha256.Sum256([]byte(tok)) }

// Download tickets (docs/api.md, Exports): a browser download can't send the
// session header, so the page first asks for a ticket and navigates to the
// download URL with it. A ticket is single-use, lives ticketTTL, is bound to the
// session and the export, and only its hash is kept. The cookie is still
// required.
const (
	ticketTTL  = 60 * time.Second
	maxTickets = 1024
)

type ticket struct {
	session [32]byte // the session's cookie key ({} when no login is required)
	export  string
	expires time.Time
}

// newTicket issues a ticket for export, bound to the session with cookie key sess.
func (a *auth) newTicket(sess [32]byte, export string) string {
	tok := randomToken()
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, t := range a.tickets {
		if !now.Before(t.expires) {
			delete(a.tickets, k)
		}
	}
	if len(a.tickets) >= maxTickets {
		evictOldest(a.tickets, func(t *ticket) time.Time { return t.expires })
	}
	a.tickets[tokenKey(tok)] = &ticket{session: sess, export: export, expires: now.Add(ticketTTL)}
	return tok
}

// useTicket spends a ticket: true if tok is a live ticket for this session and
// export. Any ticket presented is used up, valid or not.
func (a *auth) useTicket(tok string, sess [32]byte, export string) bool {
	if tok == "" || len(tok) > 128 {
		return false
	}
	k := tokenKey(tok)
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.tickets[k]
	if t == nil {
		return false
	}
	delete(a.tickets, k)
	return now.Before(t.expires) && t.export == export &&
		subtle.ConstantTimeCompare(t.session[:], sess[:]) == 1
}

// authenticate returns the request's session when it carries both the session
// cookie and the matching SessionHeader; either alone is no session.
func (a *auth) authenticate(r *http.Request) (*session, [32]byte, bool) {
	sess, k, ok := a.lookup(r)
	if !ok {
		return nil, k, false
	}
	h := r.Header.Get(SessionHeader)
	if h == "" || len(h) > 128 {
		return nil, k, false
	}
	got := tokenKey(h)
	if subtle.ConstantTimeCompare(got[:], sess.secret[:]) != 1 {
		return nil, k, false
	}
	return sess, k, true
}

// lookup returns the session the request's cookie names (the cookie alone:
// use authenticate for access). Sessions from either cookie are valid in
// either mode: they're the same table, and a session can't be had without the
// password.
func (a *auth) lookup(r *http.Request) (*session, [32]byte, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		c, err = r.Cookie(LocalSessionCookie)
	}
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return nil, [32]byte{}, false
	}
	k := tokenKey(c.Value)
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[k]
	if !ok {
		return nil, k, false
	}
	if now.Sub(s.lastSeen) > sessionIdle || now.Sub(s.created) > sessionAbsolute {
		delete(a.sessions, k)
		return nil, k, false
	}
	s.lastSeen = now
	return s, k, true
}

// newSession makes a session and returns its cookie token and its secret
// (for the SessionHeader). Only hashes of both are kept.
func (a *auth) newSession() (token, secret string) {
	token, secret = randomToken(), randomToken()
	now := a.now()
	s := &session{secret: tokenKey(secret), created: now, lastSeen: now}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sessions) >= maxSessions {
		a.evictSessionsLocked(now)
	}
	a.sessions[tokenKey(token)] = s
	return token, secret
}

// evictSessionsLocked drops expired sessions and, if still full, the least
// recently used one.
func (a *auth) evictSessionsLocked(now time.Time) {
	var oldestK [32]byte
	var oldest *session
	for k, s := range a.sessions {
		if now.Sub(s.lastSeen) > sessionIdle || now.Sub(s.created) > sessionAbsolute {
			delete(a.sessions, k)
			continue
		}
		if oldest == nil || s.lastSeen.Before(oldest.lastSeen) {
			oldest, oldestK = s, k
		}
	}
	if len(a.sessions) >= maxSessions && oldest != nil {
		delete(a.sessions, oldestK)
	}
}

func (a *auth) endSession(k [32]byte) {
	a.mu.Lock()
	delete(a.sessions, k)
	a.mu.Unlock()
}

// endOtherSessions ends every session but keep (after a password change).
func (a *auth) endOtherSessions(keep [32]byte) {
	a.mu.Lock()
	for k := range a.sessions {
		if k != keep {
			delete(a.sessions, k)
		}
	}
	a.mu.Unlock()
}

// clientAddr is a remote address's IP, unmapped, zone kept.
func clientAddr(remoteAddr string) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return ip.Unmap()
}

// clientKey is the rate-bucket key for a remote address: the IPv4 address, or
// the IPv6 /64.
func clientKey(remoteAddr string) netip.Prefix {
	ip := clientAddr(remoteAddr)
	if !ip.IsValid() {
		return netip.Prefix{}
	}
	ip = ip.WithZone("")
	bits := 32
	if ip.Is6() {
		bits = 64
	}
	p, _ := ip.Prefix(bits)
	return p
}

// limitResult says whether a login attempt may go ahead.
type limitResult struct {
	ok         bool
	retryAfter time.Duration
	locked     bool // backing off after failures (vs the burst limit)
}

// evictOldest removes m's least recently seen entry when it's full.
func evictOldest[K comparable, V any](m map[K]V, seen func(V) time.Time) {
	if len(m) < maxClients {
		return
	}
	var oldK K
	var oldT time.Time
	first := true
	for k, v := range m {
		if t := seen(v); first || t.Before(oldT) {
			oldK, oldT, first = k, t, false
		}
	}
	delete(m, oldK)
}

// pruneRecentLocked drops global failure times older than globalWindow.
func (a *auth) pruneRecentLocked(now time.Time) {
	i := 0
	for i < len(a.recent) && now.Sub(a.recent[i]) > globalWindow {
		i++
	}
	a.recent = a.recent[i:]
}

// allow decides whether an attempt from remoteAddr may go ahead, taking a
// token from its network's bucket if so. known (a remembered client) skips the
// network and global limits.
func (a *auth) allow(remoteAddr string, known bool) limitResult {
	now := a.now()
	addr, key := clientAddr(remoteAddr), clientKey(remoteAddr)
	a.mu.Lock()
	defer a.mu.Unlock()
	if st := a.addrs[addr]; st != nil {
		st.lastSeen = now
		if now.Before(st.nextAllowed) {
			return limitResult{retryAfter: st.nextAllowed.Sub(now), locked: true}
		}
	}
	if known {
		return limitResult{ok: true}
	}
	if st := a.nets[key]; st != nil {
		st.lastSeen = now
		if now.Before(st.nextAllowed) {
			return limitResult{retryAfter: st.nextAllowed.Sub(now), locked: true}
		}
	}
	a.pruneRecentLocked(now)
	if len(a.recent) > globalFailures {
		if now.Before(a.globalAt) {
			return limitResult{retryAfter: a.globalAt.Sub(now)}
		}
		a.globalAt = now.Add(globalSpacing)
	}
	b := a.buckets[key]
	if b == nil {
		evictOldest(a.buckets, func(b *bucket) time.Time { return b.lastRefill })
		b = &bucket{tokens: loginBurst, lastRefill: now}
		a.buckets[key] = b
	}
	b.tokens = min(b.tokens+float64(now.Sub(b.lastRefill))/float64(loginRefill), loginBurst)
	b.lastRefill = now
	if b.tokens < 1 {
		return limitResult{retryAfter: time.Duration((1 - b.tokens) * float64(loginRefill))}
	}
	b.tokens--
	return limitResult{ok: true}
}

// backoffLocked counts a failure in st and sets its next allowed attempt.
func backoffLocked(st *addrState, now time.Time) int {
	// The count resets after failWindow without a failure (not from the first
	// one: a steady guesser would otherwise start over every window).
	if st.failures == 0 || now.Sub(st.lastFail) > failWindow {
		st.failures = 0
	}
	st.lastSeen, st.lastFail = now, now
	st.failures++
	if st.failures >= backoffAfter {
		wait := backoffMax
		if n := st.failures - backoffAfter; n < 20 {
			wait = min(time.Second<<n, backoffMax)
		}
		st.nextAllowed = now.Add(wait)
	}
	return st.failures
}

// fail records a failed attempt from remoteAddr: its address's backoff, and
// unless known its network's backoff and the global budget. It reports the
// address's failure count in the window, so the caller can record an event once.
func (a *auth) fail(remoteAddr string, known bool) int {
	now := a.now()
	addr, key := clientAddr(remoteAddr), clientKey(remoteAddr)
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.addrs[addr]
	if st == nil {
		evictOldest(a.addrs, func(s *addrState) time.Time { return s.lastSeen })
		st = &addrState{}
		a.addrs[addr] = st
	}
	n := backoffLocked(st, now)
	if known {
		return n
	}
	ns := a.nets[key]
	if ns == nil {
		evictOldest(a.nets, func(s *addrState) time.Time { return s.lastSeen })
		ns = &addrState{}
		a.nets[key] = ns
	}
	backoffLocked(ns, now)
	a.pruneRecentLocked(now)
	if len(a.recent) <= globalFailures {
		a.recent = append(a.recent, now)
	} else {
		a.recent = append(a.recent[1:], now)
	}
	return n
}

// succeed clears an address's failures after a correct password. Its network's
// backoff stays (another host in the /64 may be the one guessing) and wears off
// with failWindow.
func (a *auth) succeed(remoteAddr string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.addrs, clientAddr(remoteAddr))
}

// isKnown reports whether r carries a live remembered-client token.
func (a *auth) isKnown(r *http.Request) bool {
	c, err := r.Cookie(knownClientCookie)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return false
	}
	k := tokenKey(c.Value)
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.known[k]
	return ok && now.Before(exp)
}

// remember issues a remembered-client token, stored hashed (also on disk, so a
// restart doesn't forget the admin's browser).
func (a *auth) remember() string {
	tok := randomToken()
	now := a.now()
	a.mu.Lock()
	for k, exp := range a.known {
		if !now.Before(exp) {
			delete(a.known, k)
		}
	}
	if len(a.known) >= maxKnownClients { // drop the one expiring soonest
		var oldK [32]byte
		var oldT time.Time
		first := true
		for k, exp := range a.known {
			if first || exp.Before(oldT) {
				oldK, oldT, first = k, exp, false
			}
		}
		delete(a.known, oldK)
	}
	a.known[tokenKey(tok)] = now.Add(knownClientTTL)
	data := knownClientsData(a.known)
	a.mu.Unlock()
	_ = writeFileAtomic(filepath.Join(a.dataDir, knownClientsFile), data, 0o600)
	return tok
}

func knownClientsData(m map[[32]byte]time.Time) []byte {
	var b []byte
	for k, exp := range m {
		b = fmt.Appendf(b, "%s %d\n", hex.EncodeToString(k[:]), exp.Unix())
	}
	return b
}

// readKnownClients loads the remembered-client hashes that haven't expired.
func readKnownClients(dataDir string, now time.Time) map[[32]byte]time.Time {
	out := map[[32]byte]time.Time{}
	b, err := os.ReadFile(filepath.Join(dataDir, knownClientsFile))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		h, e, ok := strings.Cut(strings.TrimSpace(line), " ")
		raw, err := hex.DecodeString(h)
		exp, err2 := strconv.ParseInt(e, 10, 64)
		if !ok || err != nil || err2 != nil || len(raw) != 32 || !now.Before(time.Unix(exp, 0)) {
			continue
		}
		out[[32]byte(raw)] = time.Unix(exp, 0)
	}
	return out
}

// check verifies password against the stored hash, limiting how many
// verifications run at once.
func (a *auth) check(ctx context.Context, password string) (bool, error) {
	a.mu.Lock()
	h := a.hash
	a.mu.Unlock()
	if h == "" {
		return false, nil
	}
	select {
	case a.hashers <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-a.hashers }()
	return verifyPassword(password, h)
}

func (a *auth) setPassword(pw string) error {
	h, err := hashPassword(pw, a.argon)
	if err != nil {
		return err
	}
	if err := writeHash(a.dataDir, h); err != nil {
		return err
	}
	a.mu.Lock()
	a.hash = h
	a.mu.Unlock()
	return nil
}

// ---- middleware ----

// publicInLAN reports whether a path is reachable without a session in LAN mode:
// login, logout (it only clears the cookie), session, the health check and the
// static UI (everything outside /api/). Every other API route needs a session.
func publicInLAN(path string) bool {
	switch path {
	case "/api/login", "/api/logout", "/api/session", "/healthz":
		return true
	}
	return !(path == "/api" || len(path) >= 5 && path[:5] == "/api/")
}

// authActive reports whether the UI needs a login: in LAN mode, and in
// localhost mode when require_login_local is on and a password is set.
func (s *Server) authActive() bool {
	return s.lanActive() || (s.requireLocal.Load() && s.auth.passwordSet())
}

// requireSession enforces auth when authActive: every non-public route needs
// the session cookie and its secret header (authenticate). This also stands
// in for a CSRF token: a cross-site request can't set the header.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authActive() || publicInLAN(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// An export download may carry a ticket instead of the header (with the
		// cookie).
		if id, ok := downloadPath(r.URL.Path); ok && r.Method == http.MethodGet && r.URL.Query().Has("ticket") {
			if _, k, ok := s.auth.lookup(r); ok && s.auth.useTicket(r.URL.Query().Get("ticket"), k, id) {
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusUnauthorized, "This download link has expired or was already used; download again from the Export page.")
			return
		}
		if _, _, ok := s.auth.authenticate(r); !ok {
			writeError(w, http.StatusUnauthorized, "Log in first (send the session cookie and the "+SessionHeader+" header).")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// setRequireLocal saves ui.require_login_local through the backend and applies it.
func (s *Server) setRequireLocal(ctx context.Context, on bool) error {
	// Read-modify-write under the ETag, so a concurrent save isn't undone;
	// on a collision, read again.
	for attempt := 0; ; attempt++ {
		st, err := s.backend.Settings(ctx)
		if err != nil {
			return err
		}
		if st.UI.RequireLoginLocal == on {
			break
		}
		etag := SettingsETag(st)
		st.UI.RequireLoginLocal = on
		_, err = s.backend.SaveSettings(ctx, st, etag)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrSettingsChanged) || attempt == 4 {
			return err
		}
	}
	s.mu.Lock()
	s.want.RequireLoginLocal = on
	s.mu.Unlock()
	s.requireLocal.Store(on)
	return nil
}

// downloadPath returns the export ID of an /api/exports/{id}/download path.
func downloadPath(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, "/api/exports/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/download")
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

// postTicket serves POST /api/exports/{id}/ticket.
func (s *Server) postTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := exportID(w, r)
	if !ok {
		return
	}
	if _, err := s.backend.Export(r.Context(), id); err != nil {
		s.requestOrBackendError(w, r, err, fmt.Sprintf("No export with id %q.", id))
		return
	}
	var sess [32]byte
	if s.authActive() {
		_, k, ok := s.auth.authenticate(r) // requireSession already checked
		if !ok {
			writeError(w, http.StatusUnauthorized, "Log in first.")
			return
		}
		sess = k
	}
	tok := s.auth.newTicket(sess, id)
	writeJSON(w, http.StatusOK, map[string]string{"url": "/api/exports/" + id + "/download?ticket=" + tok})
}

// ---- handlers ----

// SessionInfo is the body of GET /api/session and POST /api/login.
type SessionInfo struct {
	Mode          string `json:"mode"`          // "localhost" or "lan"
	AuthRequired  bool   `json:"auth_required"` // true in LAN mode, and on localhost with require_login_local
	Authenticated bool   `json:"authenticated"` // always true when no login is required
	PasswordSet   bool   `json:"password_set"`
	// SessionSecret is set only in POST /api/login's response: the value of the
	// X-Woodshed-Session header for this session. Never returned again.
	SessionSecret string `json:"session_secret,omitempty"`
}

func (s *Server) sessionInfo(r *http.Request) SessionInfo {
	info := SessionInfo{Mode: "localhost", Authenticated: true, PasswordSet: s.auth.passwordSet()}
	if s.lanActive() {
		info.Mode = "lan"
	}
	if s.authActive() {
		info.AuthRequired, info.Authenticated = true, false
		if _, _, ok := s.auth.authenticate(r); ok {
			info.Authenticated = true
		}
	}
	return info
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.sessionInfo(r))
}

// setSessionCookie sets the session cookie for the mode in use.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	c := &http.Cookie{Name: SessionCookie, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
	if !s.lanActive() {
		c.Name, c.Secure = LocalSessionCookie, false
	}
	http.SetCookie(w, c)
}

// clearSessionCookie clears both session cookies.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true,
		SameSite: http.SameSiteStrictMode})
	if !s.lanActive() {
		http.SetCookie(w, &http.Cookie{Name: LocalSessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
			SameSite: http.SameSiteStrictMode})
	}
}

func writeLimited(w http.ResponseWriter, l limitResult) {
	secs := int((l.retryAfter + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	if l.locked {
		wait := fmt.Sprintf("%d second%s", secs, plural(secs))
		if secs >= 120 {
			mins := (secs + 59) / 60
			wait = fmt.Sprintf("%d minute%s", mins, plural(mins))
		}
		writeError(w, http.StatusTooManyRequests, "Too many failed attempts. Try again in "+wait+".")
		return
	}
	writeError(w, http.StatusTooManyRequests, "Too many attempts. Wait a moment and try again.")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// verifyLimited checks a password under the per-client limiter, recording the
// outcome. It writes the response and returns false when the attempt is refused
// or wrong.
func (s *Server) verifyLimited(w http.ResponseWriter, r *http.Request, password string, wrongStatus int) bool {
	known := s.auth.isKnown(r)
	if l := s.auth.allow(r.RemoteAddr, known); !l.ok {
		writeLimited(w, l)
		return false
	}
	ok, err := s.auth.check(r.Context(), password)
	if err != nil {
		s.backendError(w, r, err)
		return false
	}
	if !ok {
		if n := s.auth.fail(r.RemoteAddr, known); n == lockoutFailures {
			s.event(LevelWarn, EventKindAuth, fmt.Sprintf("UI login from %s slowed down after %d failed attempts (each further "+
				"failure doubles the wait, up to %d minutes).", clientAddr(r.RemoteAddr), n, int(backoffMax/time.Minute)))
		}
		writeError(w, wrongStatus, "Wrong password.")
		return false
	}
	s.auth.succeed(r.RemoteAddr)
	return true
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.authActive() {
		writeError(w, http.StatusBadRequest, "No login is needed: the UI is in localhost mode without require_login_local.")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.Password) > maxPasswordLen {
		writeError(w, http.StatusBadRequest, "Password too long.")
		return
	}
	if !s.verifyLimited(w, r, in.Password, http.StatusUnauthorized) {
		return
	}
	// A fresh session on every login (no fixation); drop the old one if any.
	if _, k, ok := s.auth.lookup(r); ok {
		s.auth.endSession(k)
	}
	tok, secret := s.auth.newSession()
	s.setSessionCookie(w, tok)
	// Remember this browser, so throttling aimed at its network or at everyone
	// doesn't keep it out next time.
	http.SetCookie(w, &http.Cookie{Name: knownClientCookie, Value: s.auth.remember(), Path: "/", MaxAge: int(knownClientTTL / time.Second),
		Secure: s.lanActive(), HttpOnly: true, SameSite: http.SameSiteStrictMode})
	info := s.sessionInfo(r)
	info.Authenticated, info.SessionSecret = true, secret
	writeJSON(w, http.StatusOK, info)
}

// logout ends the session when the request proves it (cookie and header; a
// cookie alone, perhaps a stolen one, can't end it) and clears the cookie.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if _, k, ok := s.auth.authenticate(r); ok {
		s.auth.endSession(k)
	}
	s.clearSessionCookie(w)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// putPassword sets or changes the UI password. Once a password is set,
// changing it needs the current one in both modes (security review M5: on a
// shared machine any local user can reach localhost mode); with auth active it
// also needs a session (requireSession). `woodshed reset-password` on the
// machine is the only way to replace a password without knowing it.
func (s *Server) putPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := checkNewPassword(in.New); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "new: " + msg, Errors: map[string]string{"new": msg}})
		return
	}
	had := s.auth.passwordSet()
	if had {
		if in.Current == "" {
			writeJSON(w, http.StatusBadRequest, errorBody{Error: "current: The current password is required.",
				Errors: map[string]string{"current": "The current password is required."}})
			return
		}
		if len(in.Current) > maxPasswordLen {
			writeError(w, http.StatusBadRequest, "Password too long.")
			return
		}
		if !s.verifyLimited(w, r, in.Current, http.StatusForbidden) {
			return
		}
	}
	if err := s.auth.setPassword(in.New); err != nil {
		s.backendError(w, r, err)
		return
	}
	if had {
		_, k, _ := s.auth.authenticate(r)
		s.auth.endOtherSessions(k)
		s.event(LevelInfo, EventKindAuth, "UI password changed.")
	} else {
		s.event(LevelInfo, EventKindAuth, "UI password set.")
		// A first password also protects localhost (docs/api.md, M5 default):
		// require_login_local turns on; the user can turn it off. Installs that
		// already had a password keep their setting.
		if err := s.setRequireLocal(r.Context(), true); err != nil {
			s.backendError(w, r, fmt.Errorf("the password is set, but turning on require_login_local failed: %w", err))
			return
		}
		s.event(LevelInfo, EventKindAuth, "Login now required on localhost too (require_login_local on; turn it off in Settings).")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
