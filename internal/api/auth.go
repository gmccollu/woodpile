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

// Session cookie and CSRF header. The __Host- prefix makes the browser insist on
// Secure, Path=/ and no Domain.
const (
	SessionCookie = "__Host-woodpile_session"
	CSRFHeader    = "X-CSRF-Token"
)

// Session lifetimes and limits.
const (
	sessionIdle     = 12 * time.Hour
	sessionAbsolute = 7 * 24 * time.Hour
	maxSessions     = 256
)

// Login limits (SPEC §5): per client, a small token bucket against bursts, and a
// lockout after lockoutFailures failures within lockoutWindow, lasting
// lockoutFor. IPv6 clients are keyed by their /64, since a host can pick any
// address in it.
const (
	lockoutFailures = 10
	lockoutWindow   = 15 * time.Minute
	lockoutFor      = 15 * time.Minute
	loginBurst      = 5
	loginRefill     = time.Second // one attempt per second after the burst
	maxClients      = 4096        // tracked clients; beyond this new clients are refused until entries expire
	maxHashers      = 2           // concurrent Argon2id verifications (each uses Memory KiB)
)

type session struct {
	csrf     string
	created  time.Time
	lastSeen time.Time
}

type client struct {
	failures    int
	firstFail   time.Time
	lockedUntil time.Time
	tokens      float64
	lastRefill  time.Time
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
	clients  map[netip.Prefix]*client
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
		clients:  map[netip.Prefix]*client{},
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

// lookup returns the request's live session and its key.
func (a *auth) lookup(r *http.Request) (*session, [32]byte, bool) {
	c, err := r.Cookie(SessionCookie)
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

// clientKey is the limiter key for a remote address: the IPv4 address, or the
// IPv6 /64.
func clientKey(remoteAddr string) netip.Prefix {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Prefix{}
	}
	ip = ip.Unmap().WithZone("")
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
	locked     bool
}

// allow takes a token for an attempt from key, unless the client is locked out
// or out of tokens.
func (a *auth) allow(key netip.Prefix) limitResult {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.clients[key]
	if c == nil {
		if len(a.clients) >= maxClients {
			a.pruneClientsLocked(now)
			if len(a.clients) >= maxClients {
				return limitResult{retryAfter: time.Minute}
			}
		}
		c = &client{tokens: loginBurst, lastRefill: now}
		a.clients[key] = c
	}
	if now.Before(c.lockedUntil) {
		return limitResult{retryAfter: c.lockedUntil.Sub(now), locked: true}
	}
	c.tokens += float64(now.Sub(c.lastRefill)) / float64(loginRefill)
	if c.tokens > loginBurst {
		c.tokens = loginBurst
	}
	c.lastRefill = now
	if c.tokens < 1 {
		return limitResult{retryAfter: time.Duration((1 - c.tokens) * float64(loginRefill))}
	}
	c.tokens--
	return limitResult{ok: true}
}

// fail records a failed attempt and reports whether it started a lockout.
func (a *auth) fail(key netip.Prefix) bool {
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.clients[key]
	if c == nil {
		c = &client{lastRefill: now}
		a.clients[key] = c
	}
	if c.failures == 0 || now.Sub(c.firstFail) > lockoutWindow {
		c.failures, c.firstFail = 0, now
	}
	c.failures++
	if c.failures >= lockoutFailures {
		c.lockedUntil = now.Add(lockoutFor)
		c.failures = 0
		return true
	}
	return false
}

// succeed clears a client's failures after a correct password.
func (a *auth) succeed(key netip.Prefix) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.clients[key]; c != nil {
		c.failures = 0
	}
}

func (a *auth) pruneClientsLocked(now time.Time) {
	for k, c := range a.clients {
		idle := now.Sub(c.lastRefill) > lockoutWindow && now.Sub(c.firstFail) > lockoutWindow
		if now.After(c.lockedUntil) && idle {
			delete(a.clients, k)
		}
	}
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

// requireSession enforces LAN-mode auth: a session for every non-public route,
// and the session's CSRF token on every state-changing request that has one.
// Login carries no session yet; it relies on the cross-origin check and the
// SameSite=Strict cookie.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.lanActive() {
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
	AuthRequired  bool   `json:"auth_required"` // true in LAN mode
	Authenticated bool   `json:"authenticated"` // always true in localhost mode
	PasswordSet   bool   `json:"password_set"`
	CSRFToken     string `json:"csrf_token"` // send as X-CSRF-Token on state-changing requests; "" when not needed
}

func (s *Server) sessionInfo(r *http.Request) SessionInfo {
	info := SessionInfo{Mode: "localhost", Authenticated: true, PasswordSet: s.auth.passwordSet()}
	if s.lanActive() {
		info.Mode, info.AuthRequired, info.Authenticated = "lan", true, false
		if sess, _, ok := s.auth.lookup(r); ok {
			info.Authenticated, info.CSRFToken = true, sess.csrf
		}
	}
	return info
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.sessionInfo(r))
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func writeLimited(w http.ResponseWriter, l limitResult) {
	secs := int((l.retryAfter + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	if l.locked {
		mins := (secs + 59) / 60
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("Too many failed attempts. Try again in %d minute%s.", mins, plural(mins)))
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
	key := clientKey(r.RemoteAddr)
	if l := s.auth.allow(key); !l.ok {
		writeLimited(w, l)
		return false
	}
	ok, err := s.auth.check(r.Context(), password)
	if err != nil {
		s.backendError(w, r, err)
		return false
	}
	if !ok {
		if s.auth.fail(key) {
			s.event(LevelWarn, EventKindAuth, fmt.Sprintf("UI login locked out for %s after %d failed attempts (%d minutes).",
				key, lockoutFailures, int(lockoutFor/time.Minute)))
		}
		writeError(w, wrongStatus, "Wrong password.")
		return false
	}
	s.auth.succeed(key)
	return true
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.lanActive() {
		writeError(w, http.StatusBadRequest, "Login is only used in LAN mode.")
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
	setSessionCookie(w, tok)
	info := s.sessionInfo(r)
	info.Authenticated, info.CSRFToken = true, sess.csrf
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if _, k, ok := s.auth.lookup(r); ok {
		s.auth.endSession(k)
	}
	clearSessionCookie(w)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// putPassword sets or changes the UI password. In LAN mode it needs a session
// (enforced by requireSession) and the current password; in localhost mode
// whoever can reach the UI is trusted, as for every other setting.
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
	lan := s.lanActive()
	if lan {
		if len(in.Current) > maxPasswordLen {
			writeError(w, http.StatusBadRequest, "Password too long.")
			return
		}
		if !s.verifyLimited(w, r, in.Current, http.StatusForbidden) {
			return
		}
	}
	had := s.auth.passwordSet()
	if err := s.auth.setPassword(in.New); err != nil {
		s.backendError(w, r, err)
		return
	}
	if lan {
		_, k, _ := s.auth.lookup(r)
		s.auth.endOtherSessions(k)
	}
	if had {
		s.event(LevelInfo, EventKindAuth, "UI password changed.")
	} else {
		s.event(LevelInfo, EventKindAuth, "UI password set.")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
