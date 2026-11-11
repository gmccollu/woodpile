package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// Session cookies and CSRF header. In LAN mode (HTTPS) the __Host- prefix makes
// the browser insist on Secure, Path=/ and no Domain. With require_login_local
// the UI is plain http://localhost, where browsers don't all keep Secure
// cookies, so that cookie is HttpOnly and SameSite=Strict but not Secure.
const (
	SessionCookie      = "__Host-woodpile_session"
	LocalSessionCookie = "woodpile_session"
	CSRFHeader         = "X-CSRF-Token"
)

// Session lifetimes and limits.
const (
	sessionIdle     = 12 * time.Hour
	sessionAbsolute = 7 * 24 * time.Hour
	maxSessions     = 256
)

// Login limits (SPEC §5, security review L2). Three layers:
//   - A rate bucket per client network (an IPv4 address, an IPv6 /64, since a
//     host can pick any address in its /64): loginBurst quick attempts, then one
//     per loginRefill.
//   - Exponential backoff per full address (zone included, so link-local
//     clients, which all share fe80::/64, don't slow each other): after
//     backoffAfter failures within failWindow, each further failure doubles the
//     wait before the next attempt, up to backoffMax. There's no hard lockout:
//     once the wait is over, the right password works.
//   - A global budget: more than globalFailures failures a minute across all
//     clients spaces every attempt globalSpacing apart.
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
	globalFailures  = 200
	globalSpacing   = 5 * time.Second
	maxClients      = 4096
	maxHashers      = 2 // concurrent Argon2id verifications (each uses Memory KiB)
)

type session struct {
	csrf     string
	created  time.Time
	lastSeen time.Time
}

// bucket is a client network's rate bucket.
type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// addrState is one full address's failures and backoff.
type addrState struct {
	failures    int
	firstFail   time.Time
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
	hash     string                // cached copy of the hash file; "" when unset
	sessions map[[32]byte]*session // keyed by SHA-256 of the cookie value
	buckets  map[netip.Prefix]*bucket
	addrs    map[netip.Addr]*addrState
	recent   []time.Time // failure times in the last minute, all clients (at most globalFailures+1 kept)
	globalAt time.Time   // when the global budget next allows an attempt
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
		buckets:  map[netip.Prefix]*bucket{},
		addrs:    map[netip.Addr]*addrState{},
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

// lookup returns the request's live session and its key. Sessions from either
// cookie are valid in either mode: they're the same table, and a session
// can't be had without the password.
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

func (a *auth) newSession() (token string, s *session) {
	token = randomToken()
	now := a.now()
	s = &session{csrf: randomToken(), created: now, lastSeen: now}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sessions) >= maxSessions {
		a.evictSessionsLocked(now)
	}
	a.sessions[tokenKey(token)] = s
	return token, s
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

// pruneRecentLocked drops global failure times older than a minute.
func (a *auth) pruneRecentLocked(now time.Time) {
	i := 0
	for i < len(a.recent) && now.Sub(a.recent[i]) > time.Minute {
		i++
	}
	a.recent = a.recent[i:]
}

// allow decides whether an attempt from remoteAddr may go ahead, taking a
// token from its network's bucket if so.
func (a *auth) allow(remoteAddr string) limitResult {
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

// fail records a failed attempt from remoteAddr. It reports the address's
// failure count in the window, so the caller can record an event once.
func (a *auth) fail(remoteAddr string) int {
	now := a.now()
	addr := clientAddr(remoteAddr)
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.addrs[addr]
	if st == nil {
		evictOldest(a.addrs, func(s *addrState) time.Time { return s.lastSeen })
		st = &addrState{}
		a.addrs[addr] = st
	}
	st.lastSeen = now
	if st.failures == 0 || now.Sub(st.firstFail) > failWindow {
		st.failures, st.firstFail = 0, now
	}
	st.failures++
	if st.failures >= backoffAfter {
		wait := backoffMax
		if n := st.failures - backoffAfter; n < 20 {
			wait = min(time.Second<<n, backoffMax)
		}
		st.nextAllowed = now.Add(wait)
	}
	a.pruneRecentLocked(now)
	if len(a.recent) <= globalFailures {
		a.recent = append(a.recent, now)
	} else {
		a.recent = append(a.recent[1:], now)
	}
	return st.failures
}

// succeed clears an address's failures after a correct password.
func (a *auth) succeed(remoteAddr string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.addrs, clientAddr(remoteAddr))
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

// isStateChanging reports whether a method can change state.
func isStateChanging(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

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

// requireSession enforces auth when authActive: a session for every non-public
// route, and the session's CSRF token on every state-changing request that has
// one. Login carries no session yet; it relies on the cross-origin check and
// the SameSite=Strict cookie.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authActive() {
			next.ServeHTTP(w, r)
			return
		}
		sess, _, ok := s.auth.lookup(r)
		if !ok {
			if publicInLAN(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusUnauthorized, "Log in first.")
			return
		}
		if isStateChanging(r.Method) && r.URL.Path != "/api/login" {
			got := r.Header.Get(CSRFHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.csrf)) != 1 {
				writeError(w, http.StatusForbidden, "Missing or invalid CSRF token (send the "+CSRFHeader+" header from GET /api/session).")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---- handlers ----

// SessionInfo is the body of GET /api/session and POST /api/login.
type SessionInfo struct {
	Mode          string `json:"mode"`          // "localhost" or "lan"
	AuthRequired  bool   `json:"auth_required"` // true in LAN mode, and on localhost with require_login_local
	Authenticated bool   `json:"authenticated"` // always true when no login is required
	PasswordSet   bool   `json:"password_set"`
	CSRFToken     string `json:"csrf_token"` // send as X-CSRF-Token on state-changing requests; "" when not needed
}

func (s *Server) sessionInfo(r *http.Request) SessionInfo {
	info := SessionInfo{Mode: "localhost", Authenticated: true, PasswordSet: s.auth.passwordSet()}
	if s.lanActive() {
		info.Mode = "lan"
	}
	if s.authActive() {
		info.AuthRequired, info.Authenticated = true, false
		if sess, _, ok := s.auth.lookup(r); ok {
			info.Authenticated, info.CSRFToken = true, sess.csrf
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
	if l := s.auth.allow(r.RemoteAddr); !l.ok {
		writeLimited(w, l)
		return false
	}
	ok, err := s.auth.check(r.Context(), password)
	if err != nil {
		s.backendError(w, r, err)
		return false
	}
	if !ok {
		if n := s.auth.fail(r.RemoteAddr); n == lockoutFailures {
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
	tok, sess := s.auth.newSession()
	s.setSessionCookie(w, tok)
	info := s.sessionInfo(r)
	info.Authenticated, info.CSRFToken = true, sess.csrf
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if _, k, ok := s.auth.lookup(r); ok {
		s.auth.endSession(k)
	}
	s.clearSessionCookie(w)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// putPassword sets or changes the UI password. Once a password is set,
// changing it needs the current one in both modes (security review M5: on a
// shared machine any local user can reach localhost mode); with auth active it
// also needs a session (requireSession). `woodpile reset-password` on the
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
		_, k, _ := s.auth.lookup(r)
		s.auth.endOtherSessions(k)
		s.event(LevelInfo, EventKindAuth, "UI password changed.")
	} else {
		s.event(LevelInfo, EventKindAuth, "UI password set.")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
