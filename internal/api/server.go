package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"woodpile/web"
)

// DefaultPort is the UI port (SPEC §5).
const DefaultPort = 8514

// UIConfig is how the UI server listens.
type UIConfig struct {
	// LANMode serves HTTPS on all interfaces with login. Off: plain HTTP on
	// 127.0.0.1 with no login.
	LANMode bool
	// Port is the UI port (0 picks a free one, for tests).
	Port int
	// CertFile and KeyFile optionally replace the self-signed certificate in
	// LAN mode. Both or neither.
	CertFile, KeyFile string
}

// Options configure a Server.
type Options struct {
	Backend Backend
	// DataDir holds the password hash and the self-signed certificate.
	DataDir string
	UI      UIConfig
	// Static is the UI to serve; nil means the embedded web.Dist.
	Static fs.FS
	// Logger defaults to slog.Default().
	Logger *slog.Logger

	// Test hooks (zero values are the real thing).
	now        func() time.Time
	argon      argonParams
	identity   func() (hostIdentity, error)
	lanAddr    string // host to bind in LAN mode ("" = all interfaces)
	retryAfter time.Duration
}

// ErrNoPassword is the reason LAN mode didn't start: no password is set.
var ErrNoPassword = errors.New("LAN mode needs a UI password; set one first")

// State describes what the UI server is doing, for `woodpile status`.
type State struct {
	Listening bool
	Addr      string // the bound address, e.g. "127.0.0.1:8514"
	URL       string // where to open the UI
	LANMode   bool   // serving LAN mode right now (not just configured)
	Err       error  // the last start error, if the server isn't listening or fell back
}

// Server is the HTTP server for the API and the UI. Create it with New and run it
// with Run.
type Server struct {
	backend Backend
	dataDir string
	log     *slog.Logger
	now     func() time.Time
	auth    *auth
	static  *staticUI
	opts    Options
	handler http.Handler

	reconf chan struct{}

	mu    sync.Mutex
	want  UIConfig // the configured UI
	state State

	lan  atomic.Bool  // the running server is in LAN mode
	port atomic.Int64 // the running server's port
}

// New prepares a Server. It reads the password hash and the UI files but doesn't
// listen; call Run.
func New(opts Options) (*Server, error) {
	if opts.Backend == nil {
		return nil, errors.New("api: no backend")
	}
	if opts.DataDir == "" {
		return nil, errors.New("api: no data dir")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.argon == (argonParams{}) {
		opts.argon = defaultArgon
	}
	if opts.identity == nil {
		opts.identity = localIdentity
	}
	if opts.retryAfter == 0 {
		opts.retryAfter = 10 * time.Second
	}
	if opts.Static == nil {
		sub, err := fs.Sub(web.Dist, "dist")
		if err != nil {
			return nil, err
		}
		opts.Static = sub
	}
	st, err := newStaticUI(opts.Static)
	if err != nil {
		return nil, fmt.Errorf("api: load the UI: %w", err)
	}
	s := &Server{
		backend: opts.Backend,
		dataDir: opts.DataDir,
		log:     opts.Logger,
		now:     opts.now,
		static:  st,
		opts:    opts,
		reconf:  make(chan struct{}, 1),
		want:    opts.UI,
	}
	s.auth, err = newAuth(opts.DataDir, opts.argon, opts.now, func(e Event) { s.event(e.Level, e.Kind, e.Text) })
	if err != nil {
		return nil, fmt.Errorf("api: %w", err)
	}
	s.handler = s.buildHandler()
	return s, nil
}

// Handler is the full handler chain (headers, guards, auth, routes, UI), for
// tests and embedding.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) buildHandler() http.Handler {
	api := s.apiRoutes()
	root := http.NewServeMux()
	root.Handle("/api/", api)
	root.Handle("/api", api)
	root.Handle("/healthz", api)
	root.Handle("/", s.static)
	var h http.Handler = root
	h = s.requireSession(h)
	h = crossOriginGuard(h)
	h = s.hostGuard(h)
	h = recoverer(s.log, h)
	h = securityHeaders(h)
	return h
}

func (s *Server) lanActive() bool { return s.lan.Load() }
func (s *Server) boundPort() int  { return int(s.port.Load()) }

func (s *Server) currentUI() UIConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.want
}

// State returns what the UI server is doing now.
func (s *Server) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Reconfigure applies a new UI config: the server stops listening, ends open
// requests (live tails included) and starts again with cfg. It returns at once.
func (s *Server) Reconfigure(cfg UIConfig) {
	s.mu.Lock()
	s.want = cfg
	s.mu.Unlock()
	select {
	case s.reconf <- struct{}{}:
	default:
	}
}

func (s *Server) event(level, kind, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.backend.RecordEvent(ctx, Event{Level: level, Kind: kind, Text: text}); err != nil {
		s.log.Warn("api: could not record an event", "err", err)
	}
	switch level {
	case LevelError:
		s.log.Error("api: " + text)
	case LevelWarn:
		s.log.Warn("api: " + text)
	default:
		s.log.Info("api: " + text)
	}
}

// Run serves until ctx is done, restarting on Reconfigure. If the port can't be
// bound it logs the error, records an event, reports it in State, and retries.
// LAN mode without a password falls back to localhost mode (and says so).
func (s *Server) Run(ctx context.Context) error {
	var lastErr, lastLANErr string // repeated failures are recorded once
	for {
		cfg := s.currentUI()
		inst, err := s.start(cfg)
		var lanErr error
		if err != nil && cfg.LANMode && !isListenError(err) {
			// Don't leave the machine without a UI: serve on localhost and say why.
			lanErr = err
			if msg := "LAN mode didn't start: " + err.Error() + ". Serving the UI on localhost only."; msg != lastLANErr {
				s.event(LevelError, EventKindUI, msg)
				lastLANErr = msg
			}
			fallback := cfg
			fallback.LANMode = false
			inst, err = s.start(fallback)
		}
		if err != nil {
			msg := fmt.Sprintf("UI server can't listen on port %d: %v", cfg.Port, err)
			if msg != lastErr {
				s.event(LevelError, EventKindUI, msg)
				lastErr = msg
			}
			s.mu.Lock()
			s.state = State{Err: err}
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil
			case <-s.reconf:
			case <-time.After(s.opts.retryAfter):
			}
			continue
		}
		lastErr = ""
		if lanErr == nil {
			lastLANErr = ""
		}
		s.mu.Lock()
		s.state = State{Listening: true, Addr: inst.addr, URL: inst.url, LANMode: inst.lan, Err: lanErr}
		s.mu.Unlock()
		mode := "localhost mode"
		if inst.lan {
			mode = "LAN mode"
		}
		s.event(LevelInfo, EventKindUI, fmt.Sprintf("UI serving at %s (%s).", inst.url, mode))

		select {
		case <-ctx.Done():
			inst.stop()
			s.mu.Lock()
			s.state = State{}
			s.mu.Unlock()
			return nil
		case <-s.reconf:
			inst.stop()
		case err := <-inst.done:
			// The listener failed underneath us; start again.
			s.log.Error("api: UI server stopped", "err", err)
			inst.stop()
		}
	}
}

// isListenError reports whether err came from binding the port (as opposed to
// LAN-mode setup such as the password or certificate).
func isListenError(err error) bool {
	var oe *net.OpError
	return errors.As(err, &oe) && oe.Op == "listen"
}

// instance is one running HTTP server.
type instance struct {
	lan      bool
	addr     string
	url      string
	ln       net.Listener
	servers  []*http.Server
	cancel   context.CancelFunc
	done     chan error
	stopOnce sync.Once
}

// start binds and serves cfg.
func (s *Server) start(cfg UIConfig) (*instance, error) {
	inst := &instance{lan: cfg.LANMode, done: make(chan error, 2)}
	var tlsCfg *tls.Config
	host := "127.0.0.1"
	if cfg.LANMode {
		if !s.auth.passwordSet() {
			return nil, ErrNoPassword
		}
		cs := &certSource{
			dataDir:  s.dataDir,
			userCert: cfg.CertFile,
			userKey:  cfg.KeyFile,
			now:      s.now,
			identity: s.opts.identity,
			onGenerate: func(reason string) {
				s.event(LevelInfo, EventKindUI, "Generated a self-signed UI certificate ("+reason+").")
			},
		}
		if _, err := cs.load(); err != nil { // fail now, not on the first handshake
			return nil, err
		}
		tlsCfg = cs.tlsConfig()
		host = s.opts.lanAddr
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return nil, err
	}
	inst.ln = ln
	port := ln.Addr().(*net.TCPAddr).Port
	inst.addr = ln.Addr().String()
	baseCtx, cancel := context.WithCancel(context.Background())
	inst.cancel = cancel

	// Switch modes before the first request arrives on the new listener.
	s.lan.Store(cfg.LANMode)
	s.port.Store(int64(port))

	newServer := func(h http.Handler) *http.Server {
		return &http.Server{
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			IdleTimeout:       2 * time.Minute,
			MaxHeaderBytes:    64 << 10,
			BaseContext:       func(net.Listener) context.Context { return baseCtx },
			ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
		}
	}
	serve := func(srv *http.Server, l net.Listener) {
		inst.servers = append(inst.servers, srv)
		go func() {
			if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				inst.done <- err
			}
		}()
	}
	if !cfg.LANMode {
		inst.url = fmt.Sprintf("http://localhost:%d/", port)
		serve(newServer(s.handler), ln)
		return inst, nil
	}
	// LAN mode: TLS and plain HTTP share the port. TLS connections get the UI;
	// plain HTTP ones get a redirect to https, so an open http:// tab finds its
	// way after the switch.
	inst.url = fmt.Sprintf("https://%s/", net.JoinHostPort(displayHost(s.opts.identity), strconv.Itoa(port)))
	sp := newSplitListener(ln)
	serve(newServer(s.handler), tls.NewListener(sp.tls, tlsCfg))
	serve(newServer(securityHeaders(http.HandlerFunc(redirectToHTTPS))), sp.plain)
	return inst, nil
}

// displayHost is the host name for the UI URL in LAN mode.
func displayHost(identity func() (hostIdentity, error)) string {
	if id, err := identity(); err == nil {
		for _, n := range id.Names {
			if n != "localhost" {
				return n
			}
		}
	}
	return "localhost"
}

// stop closes the listener, cancels every request context (ending live tails),
// and waits briefly for handlers to finish.
func (inst *instance) stop() {
	inst.stopOnce.Do(func() {
		inst.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		for _, srv := range inst.servers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := srv.Shutdown(ctx); err != nil {
					_ = srv.Close()
				}
			}()
		}
		wg.Wait()
		_ = inst.ln.Close()
	})
}

func redirectToHTTPS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusBadRequest, "This server uses HTTPS in LAN mode.")
		return
	}
	if r.Host == "" {
		writeError(w, http.StatusBadRequest, "This server uses HTTPS in LAN mode.")
		return
	}
	http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusMovedPermanently)
}

// splitListener sorts accepted connections into TLS and plain by their first
// byte (0x16 starts a TLS handshake).
type splitListener struct {
	tls, plain *chanListener
}

func newSplitListener(ln net.Listener) *splitListener {
	closed := make(chan struct{})
	sp := &splitListener{
		tls:   &chanListener{addr: ln.Addr(), conns: make(chan net.Conn), closed: closed, parent: ln},
		plain: &chanListener{addr: ln.Addr(), conns: make(chan net.Conn), closed: closed, parent: ln},
	}
	var once sync.Once
	sp.tls.closeAll = func() { once.Do(func() { close(closed) }) }
	sp.plain.closeAll = sp.tls.closeAll
	go func() {
		defer sp.tls.closeAll()
		for {
			c, err := ln.Accept()
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				return
			}
			go sp.route(c)
		}
	}()
	return sp
}

func (sp *splitListener) route(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var b [1]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	pc := &peekedConn{Conn: c, first: b[0], has: true}
	target := sp.plain
	if b[0] == 0x16 {
		target = sp.tls
	}
	select {
	case target.conns <- pc:
	case <-target.closed:
		c.Close()
	}
}

// peekedConn replays one byte already read.
type peekedConn struct {
	net.Conn
	first byte
	has   bool
}

func (c *peekedConn) Read(p []byte) (int, error) {
	if c.has && len(p) > 0 {
		p[0] = c.first
		c.has = false
		if len(p) == 1 {
			return 1, nil
		}
		n, err := c.Conn.Read(p[1:])
		return n + 1, err
	}
	return c.Conn.Read(p)
}

// chanListener is a net.Listener fed by splitListener.
type chanListener struct {
	addr     net.Addr
	conns    chan net.Conn
	closed   chan struct{}
	closeAll func()
	parent   net.Listener
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error {
	l.closeAll()
	return l.parent.Close()
}

func (l *chanListener) Addr() net.Addr { return l.addr }
