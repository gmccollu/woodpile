// Package server assembles Woodpile's parts into the running service: the config,
// the index, the store, the syslog listeners, the live hub and the HTTP API.
//
// Open loads (or creates) the config, validates it, takes the data dir's lock and
// opens the index and the store. Run starts the listeners and the UI and serves
// until its context ends, then shuts down in order: listeners, live hub, UI, store
// (flushed and fsynced), index.
//
// A syslog port that can't be bound never stops the service (SPEC §2, "Port 514 in
// use"): that listener stays down, the UI starts anyway and shows a banner, and
// Retry rebinds it.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/gmccollu/woodpile/internal/api"
	"github.com/gmccollu/woodpile/internal/config"
	"github.com/gmccollu/woodpile/internal/export"
	"github.com/gmccollu/woodpile/internal/hub"
	"github.com/gmccollu/woodpile/internal/index"
	"github.com/gmccollu/woodpile/internal/listen"
	"github.com/gmccollu/woodpile/internal/search"
	"github.com/gmccollu/woodpile/internal/store"
)

// DefaultShutdownTimeout bounds the whole shutdown. It stays under the 20 s the
// Windows service manager waits by default.
const DefaultShutdownTimeout = 15 * time.Second

// Options configures a Server. Zero fields take defaults.
type Options struct {
	Version     string // shown in /api/status and the About page
	Commit      string // the build (short commit hash)
	ReleasesURL string // build-time constant; empty until the project has a public home

	Logger          *slog.Logger  // default slog.Default()
	ShutdownTimeout time.Duration // default DefaultShutdownTimeout
}

// Server is an opened Woodpile service. Call Run once.
type Server struct {
	opts    Options
	log     *slog.Logger
	cfgPath string
	created bool // Open created the config file

	lock    *DirLock
	idx     *index.DB
	store   *store.Store
	search  *search.Engine
	exports *export.Manager
	hub     *hub.Hub
	api     *api.Server
	be      *backend

	ran bool
}

// Open loads the config at cfgPath (creating it with defaults if it's missing),
// validates it, takes the data dir's lock and opens the index and the store. The
// listeners and the UI start in Run. On error nothing is left open.
func Open(cfgPath string, opts Options) (*Server, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = DefaultShutdownTimeout
	}
	if opts.Version == "" {
		opts.Version = "0.0.0-dev"
	}
	if opts.Commit == "" {
		opts.Commit = "none"
	}
	log := opts.Logger

	abs, err := filepath.Abs(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("config path %q: %w", cfgPath, err)
	}
	cfg, created, err := LoadConfig(abs)
	if err != nil {
		return nil, err
	}
	if created {
		log.Info("created a default config", "path", abs, "data_dir", cfg.Storage.DataDir)
	}
	if errs := cfg.Validate(nil); len(errs) > 0 {
		return nil, fmt.Errorf("%s has invalid settings: %w", abs, errs)
	}
	dataDir := cfg.Storage.DataDir
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create the data folder: %w", err)
	}

	s := &Server{opts: opts, log: log, cfgPath: abs, created: created}
	ok := false
	defer func() {
		if !ok {
			s.closeOpened()
		}
	}()

	if s.lock, err = LockDataDir(dataDir); err != nil {
		if errors.Is(err, ErrLocked) {
			return nil, fmt.Errorf("%w: stop it first (`woodpile status` shows what's running)", err)
		}
		return nil, fmt.Errorf("data folder %s: %w", dataDir, err)
	}
	if s.idx, err = index.Open(filepath.Join(dataDir, index.FileName)); err != nil {
		return nil, fmt.Errorf("open the index: %w", err)
	}
	// Search and exports read the files through the index. The export queue
	// opens before the store so the store's low-disk guard can delete exports
	// before any log.
	s.search = search.New(search.Options{DataDir: dataDir, Index: s.idx, Logger: log})
	if s.exports, err = export.Open(export.Options{DataDir: dataDir, Index: s.idx, Search: s.search, Version: opts.Version, Logger: log}); err != nil {
		return nil, fmt.Errorf("open the exports folder: %w", err)
	}
	// store.Open repairs the data dir after a crash and reconciles the index with
	// the files on disk.
	t0 := time.Now()
	if s.store, err = store.Open(store.Options{
		DataDir:        dataDir,
		Index:          s.idx,
		PartBytes:      cfg.Storage.PartBytes,
		RetentionDays:  cfg.Storage.RetentionDays,
		RetentionBytes: cfg.Storage.RetentionBytes,
		Reclaim:        s.exports.Reclaim,
		Logger:         log,
	}); err != nil {
		return nil, fmt.Errorf("open the log store: %w", err)
	}
	log.Info("store open", "data_dir", dataDir, "took", time.Since(t0).Round(time.Millisecond))

	s.be = newBackend(backendDeps{
		cfgPath: abs,
		cfg:     cfg,
		idx:     s.idx,
		store:   s.store,
		log:     log,
		version: opts.Version,
		commit:  opts.Commit,
		release: opts.ReleasesURL,
		search:  s.search,
		exports: s.exports,
	})
	s.hub = hub.New(hub.Options{GroupOf: s.be.groupOf})
	s.be.hub = s.hub
	if err := s.be.refreshGroups(context.Background()); err != nil {
		return nil, fmt.Errorf("read devices from the index: %w", err)
	}

	if s.api, err = api.New(api.Options{
		Backend: s.be,
		DataDir: dataDir,
		UI:      uiConfig(cfg.UI),
		Logger:  log,
	}); err != nil {
		return nil, fmt.Errorf("prepare the UI server: %w", err)
	}
	ok = true
	return s, nil
}

// LoadConfig reads the config at path, creating it with defaults when it's
// missing, and reports whether it did. A new config at the OS's default path
// gets the OS's default data dir; one anywhere else keeps its data next to it
// (the data dir is the config file's folder), so `woodpile run --config
// ./x/woodpile.toml` is self-contained. LoadConfig doesn't validate.
func LoadConfig(path string) (config.Config, bool, error) {
	def := config.Default()
	if !samePath(path, config.DefaultConfigPath()) {
		def.Storage.DataDir = filepath.Dir(path)
	}
	c, created, err := config.LoadOrCreate(path, def)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return c, false, fmt.Errorf("%w (run as the service's user or an administrator, or pass --config)", err)
		}
		return c, false, err
	}
	return c, created, nil
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return equalFold(a, b)
	}
	return a == b
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

func uiConfig(u config.UI) api.UIConfig {
	return api.UIConfig{LANMode: u.LANMode, Port: u.Port, CertFile: u.CertFile, KeyFile: u.KeyFile}
}

// ConfigPath returns the absolute config path.
func (s *Server) ConfigPath() string { return s.cfgPath }

// DataDir returns the data dir in use.
func (s *Server) DataDir() string { return s.be.config().Storage.DataDir }

// UIState reports what the UI server is doing.
func (s *Server) UIState() api.State { return s.api.State() }

// Run starts the listeners and the UI and serves until ctx is done, then shuts
// down cleanly. It returns nil after a clean shutdown, or an error if the
// shutdown didn't finish within the timeout (the store may then have lost up to
// its last few seconds of data).
func (s *Server) Run(ctx context.Context) error {
	if s.ran {
		return errors.New("server: Run called twice")
	}
	s.ran = true
	log := s.log
	cfg := s.be.config()
	started := time.Now().UTC()
	s.be.startedAt = started
	s.be.event(index.LevelInfo, index.KindStart,
		fmt.Sprintf("Woodpile %s (%s) started on %s/%s; data folder %s.", s.opts.Version, s.opts.Commit, runtime.GOOS, runtime.GOARCH, cfg.Storage.DataDir))

	ln, err := listen.Start(cfg.Listeners, listen.Options{
		Sink:    s.be.sink,
		OnEvent: s.be.listenEvent,
		Logger:  log,
	})
	if err != nil {
		// Only an invalid allowlist gets here, and Validate already checked it.
		s.shutdown(nil, nil, nil)
		return fmt.Errorf("start the listeners: %w", err)
	}
	s.be.setListeners(ln)
	s.exports.Start()
	// The listeners log what they bound and the UDP buffer the OS granted.
	for _, l := range ln.Status().Listeners {
		if !l.OK {
			log.Error("listener didn't start; the UI shows a banner with Retry", "proto", l.Proto, "addr", l.Addr,
				"port", l.Port, "err", l.Error, "holder", l.Holder)
		}
	}

	apiCtx, apiCancel := context.WithCancel(context.Background())
	apiDone := make(chan error, 1)
	go func() { apiDone <- s.api.Run(apiCtx) }()

	stateDone := make(chan struct{})
	stateStop := make(chan struct{})
	go func() {
		defer close(stateDone)
		s.watchState(started, stateStop)
	}()

	select {
	case <-ctx.Done():
	case err := <-apiDone:
		// api.Run returns only when its context ends; treat anything else as fatal.
		log.Error("UI server stopped unexpectedly", "err", err)
		apiDone <- err
	}
	log.Info("shutting down")
	close(stateStop)
	<-stateDone
	return s.shutdown(ln, apiCancel, apiDone)
}

// shutdown stops everything in order within the timeout: the listeners (no more
// messages), the live hub (ends live tails), the UI server, the store (flush,
// fsync, index update) and the index. ln and the api parts may be nil when Run
// failed early.
func (s *Server) shutdown(ln *listen.Server, apiCancel context.CancelFunc, apiDone chan error) error {
	timeout := s.opts.ShutdownTimeout
	var mu sync.Mutex
	step, stepStart := "starting", time.Now()
	setStep := func(v string) {
		mu.Lock()
		if d := time.Since(stepStart); d > time.Second {
			s.log.Warn("shutdown step was slow", "step", step, "took", d.Round(time.Millisecond))
		}
		step, stepStart = v, time.Now()
		mu.Unlock()
	}

	done := make(chan error, 1)
	go func() {
		var errs []error
		if ln != nil {
			setStep("stopping the listeners")
			st := ln.Status()
			if err := ln.Close(); err != nil {
				errs = append(errs, fmt.Errorf("listeners: %w", err))
			}
			s.log.Info("listeners stopped", "received", st.Received, "truncated", st.Truncated, "rejected", st.Rejected)
		}
		setStep("closing the live hub")
		s.hub.Close()
		if apiCancel != nil {
			setStep("stopping the UI server")
			apiCancel()
			<-apiDone
		}
		setStep("stopping the export queue")
		s.exports.Close() // a build in progress is cancelled (marked failed)
		setStep("flushing and closing the store")
		before := s.store.Usage().Written
		if err := s.store.Close(); err != nil {
			errs = append(errs, fmt.Errorf("store: %w", err))
		}
		u := s.store.Usage()
		s.log.Info("store closed", "written", u.Written, "dropped", u.Dropped, "flushed_at_stop", u.Written-before)
		level := index.LevelInfo
		text := "Woodpile stopped."
		if len(errs) > 0 {
			level, text = index.LevelError, "Woodpile stopped with errors: "+errors.Join(errs...).Error()
		}
		s.be.event(level, index.KindStop, text)
		setStep("closing the index")
		if err := s.idx.Close(); err != nil {
			errs = append(errs, fmt.Errorf("index: %w", err))
		}
		setStep("done")
		removeState(s.be.config().Storage.DataDir)
		if err := s.lock.Unlock(); err != nil {
			errs = append(errs, fmt.Errorf("unlock: %w", err))
		}
		done <- errors.Join(errs...)
	}()

	select {
	case err := <-done:
		if err != nil {
			s.log.Error("shutdown finished with errors", "err", err)
			return err
		}
		s.log.Info("stopped cleanly")
		return nil
	case <-time.After(timeout):
		mu.Lock()
		at := step
		mu.Unlock()
		err := fmt.Errorf("shutdown didn't finish within %v (stuck %s)", timeout, at)
		s.log.Error("shutdown timed out", "err", err)
		return err
	}
}

// closeOpened closes what Open managed to open, after an Open error.
func (s *Server) closeOpened() {
	if s.store != nil {
		_ = s.store.Close()
	}
	if s.exports != nil {
		s.exports.Close()
	}
	if s.hub != nil {
		s.hub.Close()
	}
	if s.idx != nil {
		_ = s.idx.Close()
	}
	if s.lock != nil {
		_ = s.lock.Unlock()
	}
}

// Close releases everything Open opened, for a Server that never ran.
func (s *Server) Close() error {
	if s.ran {
		return nil
	}
	s.ran = true
	s.closeOpened()
	return nil
}

// watchState keeps the run-state file (read by `woodpile status`) current until
// stop closes.
func (s *Server) watchState(started time.Time, stop <-chan struct{}) {
	dataDir := s.be.config().Storage.DataDir
	var last RunState
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for first := true; ; first = false {
		ui := s.api.State()
		rs := RunState{
			PID:       os.Getpid(),
			Version:   s.opts.Version,
			StartedAt: started,
			Config:    s.cfgPath,
			DataDir:   dataDir,
			UI:        RunStateUI{Listening: ui.Listening, URL: ui.URL, LANMode: ui.LANMode},
		}
		if ui.Err != nil {
			rs.UI.Error = ui.Err.Error()
		}
		if first || rs != last {
			if err := writeState(dataDir, rs); err != nil {
				s.log.Warn("couldn't write the run-state file", "err", err)
			}
			if rs.UI != last.UI {
				if rs.UI.Listening {
					s.log.Info("UI ready", "url", rs.UI.URL)
				} else if rs.UI.Error != "" {
					s.log.Error("UI isn't listening", "err", rs.UI.Error)
				}
			}
			last = rs
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}
