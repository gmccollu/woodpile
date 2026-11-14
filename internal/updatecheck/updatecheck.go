// Package updatecheck is Woodshed's opt-in update check (SPEC §10.2): once a
// day, while updates.check is "on", one HTTPS GET of the project's public
// releases file, and a banner when a newer version is out. It never downloads
// or installs anything.
//
// It is the only package in Woodshed allowed to connect to a host Woodshed
// picks itself (a module-wide test enforces it), so everything about the
// request lives here: the URL (a constant, not configurable), its own transport
// and dialer, the limits and the schedule. While the setting is "ask" or "off",
// and in a build that isn't a release, nothing here touches the network.
package updatecheck

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gmccollu/woodshed/internal/atomicfile"
)

// URL is the releases file the check reads. It can't be configured: the site
// keeps this path answering (redirects aren't followed).
const URL = "https://" + Host + "/woodshed/releases.json"

// Host is the one host the check connects to (or names in a proxy's CONNECT).
const Host = "gmccollu.github.io"

// StateFile is the checker's state in the data folder: the last check and its
// result, and the dismissed version.
const StateFile = "update-check.json"

// Settings values (config.UpdateCheck*; repeated here so this package stands alone).
const (
	CheckAsk = "ask"
	CheckOn  = "on"
	CheckOff = "off"
)

// Schedule (SPEC §10.2).
const (
	startMin, startMax = 2 * time.Minute, 10 * time.Minute // first check after start-up
	day, dayJitter     = 24 * time.Hour, time.Hour         // then every 24 h ± 1 h
	retryMin, retryMax = time.Hour, 2 * time.Hour          // one retry after a failure
	turnOnMin          = 5 * time.Second                   // turning it on checks within a minute
	turnOnMax          = 30 * time.Second
	manualEvery        = time.Minute // Check now, at most once a minute
)

// PromptText is the update_prompt banner's text.
const PromptText = "Check for new Woodshed versions once a day? It fetches one public file from " + Host +
	" and sends nothing about this server."

// Errors from CheckNow.
var (
	ErrUnavailable = errors.New("the update check isn't available in this build")
	ErrNotOn       = errors.New("the update check is off: turn it on first")
)

// TooSoonError is CheckNow's answer within a minute of the last Check now.
type TooSoonError struct{ RetryAfter time.Duration }

func (e *TooSoonError) Error() string { return "a check ran less than a minute ago" }

// Options configure a Checker.
type Options struct {
	Version     string // the running build's version
	ReleasesURL string // the build's releases page; empty means the check is unavailable
	DataDir     string // holds StateFile
	Check       string // updates.check at start: "ask", "on" or "off"
	Logger      *slog.Logger

	// Testing replaces the network, the clock and the randomness, for tests
	// only: nothing in the config, the command line or the environment sets it.
	Testing *TestHooks
}

// TestHooks are the test-only replacements (see Options.Testing and the
// uctest package).
type TestHooks struct {
	// DialContext replaces the checker's dialer (the URL stays the same, so
	// TLS still checks the certificate for Host, against RootCAs).
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	RootCAs     *x509.CertPool
	Clock       Clock
	// Rand returns a number in [0, 1) for the schedule's random picks.
	Rand func() float64
}

// Banner is one of the check's banners in /api/status.
type Banner struct {
	Kind   string // "update_prompt" or "update_available"
	Level  string // "info"
	Text   string
	Action string // "update_choice" or "update_dismiss"
	URL    string // the release page, for update_available
}

// Info is GET /api/updates.
type Info struct {
	Check            string
	Available        bool
	URL              string
	Current          string
	LastCheck        *time.Time
	LastResult       string // "", "ok" or "error"
	LastError        string
	LastErrorKind    string
	NextCheck        *time.Time
	Latest           *Latest
	UpdateAvailable  bool
	DismissedVersion string
}

// state is the content of StateFile.
type state struct {
	LastCheck        *time.Time `json:"last_check"`
	LastResult       string     `json:"last_result"`
	LastError        string     `json:"last_error"`
	LastErrorKind    string     `json:"last_error_kind"`
	Latest           *Latest    `json:"latest"`
	DismissedVersion string     `json:"dismissed_version"`
}

// Checker runs the schedule. Create it with New, start Run once, and tell it
// about setting changes with SetCheck. Its methods may be called concurrently.
type Checker struct {
	log         *slog.Logger
	current     Version
	available   bool
	version     string
	releasesURL string
	statePath   string
	clock       Clock
	rand        func() float64
	transport   *http.Transport

	wake chan struct{}

	mu         sync.Mutex
	check      string
	st         state
	next       time.Time // zero unless check is "on"
	retried    bool      // the check due at next is the one retry after a failure
	failedAt   time.Time // when the cycle's first check failed
	lastManual time.Time
	cancel     context.CancelFunc // cancels a check in flight
	gen        int                // bumped by SetCheck, so a check in flight knows it's stale
}

// New loads the state file (a missing or unreadable one is a fresh start) and
// schedules the first check. It doesn't touch the network; Run does, and only
// while the setting is "on".
func New(o Options) *Checker {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	c := &Checker{
		log:         o.Logger,
		version:     o.Version,
		releasesURL: o.ReleasesURL,
		statePath:   filepath.Join(o.DataDir, StateFile),
		clock:       realClock{},
		rand:        rand.Float64,
		wake:        make(chan struct{}, 1),
		check:       o.Check,
	}
	if h := o.Testing; h != nil {
		if h.Clock != nil {
			c.clock = h.Clock
		}
		if h.Rand != nil {
			c.rand = h.Rand
		}
	}
	c.current, c.available = ReleaseBuild(o.Version)
	c.available = c.available && o.ReleasesURL != ""
	if !c.available {
		return c // no transport, no state: nothing here ever runs
	}
	c.transport = newTransport(o.Testing)
	c.st = c.load()
	if c.check == CheckOn {
		now := c.clock.Now()
		if lc := c.st.LastCheck; lc != nil && now.Sub(*lc) < day && !lc.After(now) {
			c.next = lc.Add(day)
		} else {
			c.next = now.Add(c.between(startMin, startMax))
		}
	}
	return c
}

// load reads StateFile. A latest entry that doesn't link into this build's
// releases page is dropped.
func (c *Checker) load() state {
	var st state
	b, err := atomicfile.ReadFile(c.statePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			c.log.Warn("update check: couldn't read its state file; starting fresh", "path", c.statePath, "err", err)
		}
		return state{}
	}
	if err := json.Unmarshal(b, &st); err != nil {
		c.log.Warn("update check: its state file is malformed; starting fresh", "path", c.statePath, "err", err)
		return state{}
	}
	if l := st.Latest; l != nil {
		v, ok := ParseVersion(l.Version)
		if _, linkOK := releaseLink(c.releasesURL, "v"+l.Version, l.URL); !ok || !linkOK || v.String() != l.Version {
			st.Latest = nil
		}
	}
	if _, ok := ParseVersion(st.DismissedVersion); !ok {
		st.DismissedVersion = ""
	}
	return st
}

// save writes StateFile (mode 0640). Call with mu held.
func (c *Checker) save() {
	b, err := json.MarshalIndent(c.st, "", "  ")
	if err != nil {
		return
	}
	if err := atomicfile.WriteFile(c.statePath, append(b, '\n'), 0o640); err != nil {
		c.log.Warn("update check: couldn't write its state file", "path", c.statePath, "err", err)
	}
}

// Available reports whether this build can check (a release version with a
// releases page).
func (c *Checker) Available() bool { return c.available }

// between picks a random duration in [lo, hi).
func (c *Checker) between(lo, hi time.Duration) time.Duration {
	return lo + time.Duration(c.rand()*float64(hi-lo))
}

func (c *Checker) daily() time.Duration { return c.between(day-dayJitter, day+dayJitter) }

func (c *Checker) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Run runs the schedule until ctx is done. It returns at once in a build that
// can't check.
func (c *Checker) Run(ctx context.Context) {
	if !c.available {
		return
	}
	c.mu.Lock()
	c.log.Info("update check", "setting", c.check, "url", URL)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		next, gen := c.next, c.gen
		c.mu.Unlock()
		var due <-chan time.Time
		var timer Timer
		if !next.IsZero() {
			timer = c.clock.NewTimer(max(next.Sub(c.clock.Now()), 0))
			due = timer.C()
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-c.wake:
			if timer != nil {
				timer.Stop()
			}
		case <-due:
			c.runCheck(ctx, gen)
		}
	}
}

// runCheck makes one check and schedules the next.
func (c *Checker) runCheck(ctx context.Context, gen int) {
	c.mu.Lock()
	if c.gen != gen || c.check != CheckOn {
		c.mu.Unlock()
		return
	}
	cctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.mu.Unlock()

	f, cerr := fetch(cctx, c.transport)
	var latest *Latest
	if cerr == nil {
		latest, cerr = evaluate(f, c.current, c.releasesURL)
	}
	cancel()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancel = nil
	if c.gen != gen || c.check != CheckOn || ctx.Err() != nil {
		return // turned off (or stopping) while it ran: drop the result
	}
	now := c.clock.Now().UTC()
	c.st.LastCheck = &now
	if cerr == nil {
		c.st.LastResult, c.st.LastError, c.st.LastErrorKind = "ok", "", ""
		c.st.Latest = latest
		c.next, c.retried = now.Add(c.daily()), false
		args := []any{"result", "ok", "latest", latest.Version, "current", c.current.String()}
		if latest.newerThan(c.current) {
			args = append(args, "update_available", true)
		}
		c.log.Info("update check", args...)
	} else {
		c.st.LastResult, c.st.LastError, c.st.LastErrorKind = "error", cerr.Msg, cerr.Kind
		detail := ""
		if cerr.err != nil {
			detail = cerr.err.Error()
		}
		if !c.retried {
			c.failedAt = now
			c.next, c.retried = now.Add(c.between(retryMin, retryMax)), true
		} else {
			c.next, c.retried = c.failedAt.Add(c.daily()), false
		}
		c.log.Warn("update check failed", "kind", cerr.Kind, "error", cerr.Msg, "detail", detail, "next", c.next.UTC().Format(time.RFC3339))
	}
	c.save()
}

func (l *Latest) newerThan(v Version) bool {
	if l == nil {
		return false
	}
	lv, ok := ParseVersion(l.Version)
	return ok && lv.Compare(v) > 0
}

// SetCheck applies a new updates.check. Turning it on checks within a minute;
// turning it off (or back to "ask") cancels the next check, and a check in
// flight, and clears the banner.
func (c *Checker) SetCheck(v string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v == c.check {
		return
	}
	old := c.check
	c.check = v
	c.gen++
	if !c.available {
		return
	}
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.retried = false
	if v == CheckOn {
		c.next = c.clock.Now().Add(c.between(turnOnMin, turnOnMax))
	} else {
		c.next = time.Time{}
		if c.st.Latest != nil {
			c.st.Latest = nil
			c.save()
		}
	}
	c.log.Info("update check setting changed", "from", old, "to", v)
	c.poke()
}

// CheckNow runs a check now (Settings' Check now): ErrUnavailable in a build
// that can't check, ErrNotOn unless the setting is "on", a *TooSoonError within
// a minute of the last one.
func (c *Checker) CheckNow() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.available {
		return ErrUnavailable
	}
	if c.check != CheckOn {
		return ErrNotOn
	}
	now := c.clock.Now()
	if !c.lastManual.IsZero() {
		if wait := manualEvery - now.Sub(c.lastManual); wait > 0 {
			return &TooSoonError{RetryAfter: wait}
		}
	}
	c.lastManual = now
	if c.cancel != nil {
		return nil // a check is running right now: that one answers
	}
	c.next, c.retried = now, false
	c.poke()
	return nil
}

// Dismiss hides the update_available banner for version, for everyone, until
// a newer version is out. It reports false for a string that isn't a version.
func (c *Checker) Dismiss(version string) bool {
	v, ok := ParseVersion(version)
	if !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.st.DismissedVersion = v.String()
	if c.available {
		c.save()
	}
	return true
}

// Info is GET /api/updates.
func (c *Checker) Info() Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	in := Info{
		Check:            c.check,
		Available:        c.available,
		URL:              URL,
		Current:          c.version,
		LastResult:       c.st.LastResult,
		LastError:        c.st.LastError,
		LastErrorKind:    c.st.LastErrorKind,
		DismissedVersion: c.st.DismissedVersion,
	}
	if c.st.LastCheck != nil {
		t := c.st.LastCheck.UTC()
		in.LastCheck = &t
	}
	if c.check == CheckOn && !c.next.IsZero() {
		t := c.next.UTC()
		in.NextCheck = &t
	}
	if c.check == CheckOn && c.st.Latest != nil {
		l := *c.st.Latest
		in.Latest = &l
		in.UpdateAvailable = l.newerThan(c.current)
	}
	return in
}

// Banners are the check's banners for /api/status: update_prompt while the
// setting is "ask", and update_available while it's "on" and a newer version
// than this one, and than the dismissed one, is out.
func (c *Checker) Banners() []Banner {
	if !c.available {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.check {
	case CheckAsk:
		return []Banner{{Kind: "update_prompt", Level: "info", Text: PromptText, Action: "update_choice"}}
	case CheckOn:
		l := c.st.Latest
		if !l.newerThan(c.current) || l.Version == c.st.DismissedVersion {
			return nil
		}
		return []Banner{{
			Kind: "update_available", Level: "info", Action: "update_dismiss", URL: l.URL,
			Text: "Woodshed " + l.Version + " is available (this is " + c.current.String() + ").",
		}}
	}
	return nil
}

// ReadState reads the last check from a data folder's StateFile, for
// `woodshed status`. ok is false when there's none (or it can't be read).
func ReadState(dataDir string) (lastCheck *time.Time, result, errMsg string, latest *Latest, ok bool) {
	b, err := atomicfile.ReadFile(filepath.Join(dataDir, StateFile))
	if err != nil {
		return nil, "", "", nil, false
	}
	var st state
	if json.Unmarshal(b, &st) != nil {
		return nil, "", "", nil, false
	}
	return st.LastCheck, st.LastResult, st.LastError, st.Latest, true
}
