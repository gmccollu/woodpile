package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gmccollu/woodpile/internal/api"
	"github.com/gmccollu/woodpile/internal/config"
	"github.com/gmccollu/woodpile/internal/server"
	"github.com/gmccollu/woodpile/internal/winacl"
)

// Portable mode: download, double-click, and it runs, with nothing installed. The
// data folder is woodpile-data/ next to the binary (or ~/Woodpile when that isn't
// writable), the config is created there with the defaults, and the console shows
// a short banner, then one line per new device.

const (
	portableDirName = "woodpile-data"
	portableHomeDir = "Woodpile"
	// uiFallbackPorts is how many ports after the configured UI port are tried
	// when it's taken (8514 -> 8515..8530). The syslog port never falls back.
	uiFallbackPorts = 16
	// portableShutdownTimeout fits Windows' ~5 s grace after a console close,
	// logoff or shutdown.
	portableShutdownTimeout = 4 * time.Second
	portableNote            = "Starting in portable mode; `woodpile help` lists commands."
)

type portableOpts struct {
	dir       string // explicit data folder ("" = next to the binary, else ~/Woodpile)
	port      int    // syslog port for UDP and TCP (0 = the config's)
	listen    string // syslog bind address ("" = the config's)
	uiPort    int    // UI port to try first (0 = the config's)
	noBrowser bool
	// launched says how the process was started (double-click or terminal).
	launched launch
}

// startPortable runs portable mode for `woodpile` with no arguments. A variable so
// tests can stub it.
var startPortable = func(stdout, stderr io.Writer) int {
	l := detectLaunch()
	if l == launchTerminal {
		fmt.Fprintln(stdout, portableNote)
	}
	return runPortable(portableOpts{launched: l}, stdout, stderr)
}

func cmdPortable(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("portable", "", `
Run Woodpile right here, with nothing installed: it keeps its config and logs in
woodpile-data/ next to this program (or in ~/Woodpile if that folder can't be
written), listens for syslog on UDP and TCP 514, and opens the UI in your
browser. Stop it with Ctrl-C or by closing the window; everything is saved.

Running "woodpile" with no command, or double-clicking it, does the same.`, stderr)
	dir := fs.String("dir", "", "data `folder` (default: woodpile-data next to the program)")
	port := fs.Int("port", 0, "syslog `port` for UDP and TCP (default: the config's, 514)")
	listen := fs.String("listen", "", "syslog bind `address` (default: the config's, every interface)")
	uiPort := fs.Int("ui-port", 0, "UI `port` to try first (default: the config's, 8514); the next 16 are tried if it's taken")
	noBrowser := fs.Bool("no-browser", false, "don't open the UI in a browser")
	if code, done := parse(fs, args, stdout); done {
		return code
	}
	if *port < 0 || *port > 65535 || *uiPort < 0 || *uiPort > 65535 {
		fmt.Fprintf(stderr, "woodpile portable: ports must be between 1 and 65535\n")
		return 2
	}
	return runPortable(portableOpts{dir: *dir, port: *port, listen: *listen, uiPort: *uiPort, noBrowser: *noBrowser,
		launched: detectLaunch()}, stdout, stderr)
}

// ---- the data folder ----

// dirEnv is what chooseDataDir looks at (faked in tests).
type dirEnv struct {
	exe      func() (string, error)
	home     func() (string, error)
	writable func(dir string) error
}

func realDirEnv() dirEnv {
	return dirEnv{
		exe: func() (string, error) {
			p, err := os.Executable()
			if err != nil {
				return "", err
			}
			if r, err := filepath.EvalSymlinks(p); err == nil {
				p = r
			}
			return p, nil
		},
		home:     os.UserHomeDir,
		writable: makeWritable,
	}
}

// makeWritable creates dir if needed and checks a file can be written in it. A
// folder it creates is, on Windows, given a protected ACL for this user, SYSTEM
// and Administrators (security review M3), whatever the parent allows.
func makeWritable(dir string) error {
	_, statErr := os.Stat(dir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if errors.Is(statErr, os.ErrNotExist) {
		if err := winacl.ProtectForCurrentUser(dir); err != nil {
			return err
		}
	}
	return checkWritable(dir)
}

// chooseDataDir picks the portable data folder: explicit if given (no fallback),
// else woodpile-data next to the binary, else ~/Woodpile. note says why it fell
// back, for the banner.
func chooseDataDir(explicit string, e dirEnv) (dir, note string, err error) {
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", "", err
		}
		if err := e.writable(abs); err != nil {
			return "", "", fmt.Errorf("can't use %s as the data folder: %w", abs, err)
		}
		return abs, "", nil
	}
	var why string
	exe, err := e.exe()
	if err == nil {
		next := filepath.Join(filepath.Dir(exe), portableDirName)
		switch {
		case strings.Contains(exe, "/AppTranslocation/"):
			// macOS runs a downloaded app from a read-only randomized copy.
			why = "macOS is running this copy from a temporary read-only location"
		default:
			if werr := e.writable(next); werr == nil {
				return next, "", nil
			} else {
				why = fmt.Sprintf("%s can't be written (%v)", filepath.Dir(exe), shortErr(werr))
			}
		}
	} else {
		why = fmt.Sprintf("this program's folder is unknown (%v)", err)
	}
	home, err := e.home()
	if err != nil {
		return "", "", fmt.Errorf("%s, and there's no home folder to use instead: %w", why, err)
	}
	fallback := filepath.Join(home, portableHomeDir)
	if err := e.writable(fallback); err != nil {
		return "", "", fmt.Errorf("%s, and %s can't be written either: %w", why, fallback, err)
	}
	return fallback, fmt.Sprintf("Using %s because %s.", fallback, why), nil
}

func shortErr(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}

// ---- the UI port ----

// pickUIPort returns want if it's free, else the first free one of the next
// uiFallbackPorts, and whether it fell back.
func pickUIPort(want int, free func(port int) bool) (int, bool, error) {
	if free(want) {
		return want, false, nil
	}
	for p := want + 1; p <= want+uiFallbackPorts && p <= 65535; p++ {
		if free(p) {
			return p, true, nil
		}
	}
	return 0, false, fmt.Errorf("the UI port %d and the %d after it are all in use", want, uiFallbackPorts)
}

func tcpPortFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// ---- running ----

func runPortable(o portableOpts, stdout, stderr io.Writer) (code int) {
	defer func() {
		// A double-clicked console window closes as soon as we exit, so the user
		// would never see why it failed.
		if code != 0 && o.launched == launchDoubleClick && runtime.GOOS == "windows" {
			fmt.Fprint(stdout, "\nPress Enter to close this window.")
			_, _ = fmt.Fscanln(os.Stdin)
		}
	}()
	failf := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "Woodpile can't start: "+format+"\n", args...)
		return 1
	}

	dir, dirNote, err := chooseDataDir(o.dir, realDirEnv())
	if err != nil {
		return failf("%v", err)
	}
	cfgPath := filepath.Join(dir, config.FileName)
	// An existing folder, config or password hash must be this user's (re-test N13):
	// someone else's woodpile-data could hold a config or password of theirs.
	for _, p := range []string{dir, cfgPath, filepath.Join(dir, api.PasswordFile)} {
		if err := checkOwnedByMe(p); err != nil {
			return failf("%v", err)
		}
	}
	if note, err := tightenPortableDir(dir); err != nil {
		return failf("%v", err)
	} else if note != "" {
		fmt.Fprintln(stdout, note)
	}

	// Another copy already running on this folder? Say so before touching the config.
	if inUse, _ := server.DataDirInUse(dir); inUse {
		return failf("%s", alreadyRunning(dir))
	}

	cfg, _, err := server.LoadConfig(cfgPath)
	if err != nil {
		return failf("%v", err)
	}
	// The folder may have moved (a USB stick, a copied folder): the data is here.
	cfg.Storage.DataDir = dir
	if o.port != 0 {
		cfg.Listeners.UDP.Port, cfg.Listeners.TCP.Port = o.port, o.port
	}
	if o.listen != "" {
		cfg.Listeners.UDP.Addr, cfg.Listeners.TCP.Addr = o.listen, o.listen
	}
	if o.uiPort != 0 {
		cfg.UI.Port = o.uiPort
	}
	wanted := cfg.UI.Port
	port, fellBack, err := pickUIPort(wanted, tcpPortFree)
	if err != nil {
		return failf("%v", err)
	}
	cfg.UI.Port = port
	if errs := cfg.Validate(nil); len(errs) > 0 {
		return failf("%s: %v", cfgPath, errs)
	}
	// Saved so the settings page and `woodpile status` agree with what's running.
	if err := config.Save(cfgPath, cfg); err != nil {
		return failf("%v", err)
	}

	logPath := filepath.Join(dir, "woodpile.log")
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		return failf("%v", err)
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(logFile, nil))

	srv, err := server.Open(cfgPath, server.Options{
		Version: version, Commit: commit, ReleasesURL: releasesURL,
		Logger: log, ShutdownTimeout: portableShutdownTimeout, FindPeer: findPeer,
	})
	if errors.Is(err, server.ErrLocked) {
		return failf("%s", alreadyRunning(dir))
	}
	if err != nil {
		return failf("%v", err)
	}

	// Ctrl-C; and on Windows closing the console window, logging off and shutting
	// down, which the Go runtime turns into SIGTERM through SetConsoleCtrlHandler
	// (it holds the handler until we exit, within Windows' ~5 s grace).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	st, stErr := waitForUI(ctx, srv, done)
	if stErr != nil {
		stop()
		err := <-done
		if errors.Is(stErr, errStoppedEarly) && err != nil {
			return failf("%v", err)
		}
		return failf("%v (details in %s)", stErr, logPath)
	}
	url := srv.UIState().URL
	if url == "" {
		url = fmt.Sprintf("http://localhost:%d/", port)
	}
	info := bannerInfo{
		Version: version, DataDir: dir, URL: url, GOOS: runtime.GOOS, DirNote: dirNote,
		Listeners: st.Listeners, HostIPs: st.HostIPs, Config: cfg,
	}
	if fellBack {
		info.UIFellBackFrom = wanted
	}
	fmt.Fprint(stdout, bannerText(info))
	if runtime.GOOS == "windows" && cfg.Listeners.UDP.Enabled {
		fmt.Fprint(stdout, windowsFirewallNotes(cfg.Listeners.UDP.Port))
	}
	if w := srv.PeerWarning(ctx); w != "" {
		fmt.Fprintln(stdout, "\nWarning: "+w)
	}
	fmt.Fprintf(stdout, "\nPress Ctrl-C or close this window to stop. Your logs stay in %s\n\n", dir)

	if !o.noBrowser && hasDisplay() {
		if err := openBrowser(url); err != nil {
			log.Warn("couldn't open the browser", "err", err)
		}
	}
	go watchDevices(ctx, srv, stdout)

	err = <-done
	fmt.Fprintln(stdout)
	if err != nil {
		fmt.Fprintf(stderr, "Woodpile stopped with an error: %v (details in %s)\n", err, logPath)
		return 1
	}
	fmt.Fprintf(stdout, "Woodpile stopped; everything is saved in %s\n", dir)
	return 0
}

func alreadyRunning(dir string) string {
	msg := fmt.Sprintf("Woodpile is already running on %s. Use the window that's already open", dir)
	if rs, err := server.ReadState(dir); err == nil && rs.UI.URL != "" {
		msg += ", or its UI at " + rs.UI.URL
	}
	return msg + "; to run a second copy, give it its own folder with `woodpile portable --dir <folder>`."
}

var errStoppedEarly = errors.New("the server stopped while starting")

// waitForUI waits until the UI is listening and returns the server's status.
// Both come from the server in this process, not over HTTP, so they work
// whatever the UI's login settings (require_login_local).
func waitForUI(ctx context.Context, srv *server.Server, done chan error) (api.Status, error) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			done <- err
			return api.Status{}, errStoppedEarly
		case <-ctx.Done():
			return api.Status{}, errors.New("stopped before the UI came up")
		case <-time.After(100 * time.Millisecond):
		}
		if !srv.UIState().Listening {
			continue
		}
		return srv.LocalStatus(ctx)
	}
	if e := srv.UIState().Err; e != nil {
		return api.Status{}, fmt.Errorf("the UI didn't start: %v", e)
	}
	return api.Status{}, errors.New("the UI didn't start within 20 s")
}

// deviceLister is the part of the server watchDevices needs.
type deviceLister interface {
	LocalDevices(ctx context.Context) ([]api.Device, error)
}

// watchDevices prints a line for each device that sends its first message
// while we run. It asks the server in this process (the index learns of a
// device within the store's few-second index interval).
func watchDevices(ctx context.Context, srv deviceLister, out io.Writer) {
	seen := map[string]bool{}
	first := true
	for {
		if ds, err := srv.LocalDevices(ctx); err == nil {
			for _, d := range ds {
				if seen[d.ID] {
					continue
				}
				seen[d.ID] = true
				if !first {
					fmt.Fprintln(out, newDeviceLine(d))
				}
			}
			first = false
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func newDeviceLine(d api.Device) string {
	name := d.IP
	if d.Name != "" {
		name = d.Name + " (" + d.IP + ")"
	}
	return fmt.Sprintf("New device %s (first message %s)", name, d.FirstSeen.Local().Format("15:04:05"))
}

// ---- the banner ----

type bannerInfo struct {
	Version        string
	DataDir        string
	DirNote        string // why the data folder isn't next to the binary
	URL            string
	UIFellBackFrom int // the UI port that was taken, or 0
	Listeners      []api.Listener
	HostIPs        []string
	Config         config.Config
	GOOS           string
}

func bannerText(b bannerInfo) string {
	var s strings.Builder
	fmt.Fprintf(&s, "\nWoodpile %s, portable mode\n", b.Version)
	fmt.Fprintf(&s, "  Data folder:  %s\n", b.DataDir)
	if b.DirNote != "" {
		fmt.Fprintf(&s, "                (%s)\n", b.DirNote)
	}
	s.WriteString(listeningLine(b.Listeners))
	ips := b.HostIPs
	if addrs := listenerAddrs(b.Listeners); len(addrs) == 1 && !isWildcard(addrs[0]) {
		ips = addrs // bound to one address: that's the only one devices can use
	}
	if len(ips) == 0 {
		ips = []string{"(no network address found)"}
	}
	fmt.Fprintf(&s, "  Send logs to: %s\n", strings.Join(ips, ", "))
	fmt.Fprintf(&s, "  Web UI:       %s\n", b.URL)
	if b.UIFellBackFrom != 0 {
		fmt.Fprintf(&s, "                (port %d is in use, maybe by an installed Woodpile, so this copy uses %s)\n",
			b.UIFellBackFrom, portOf(b.URL))
	}
	s.WriteString(listenersHelp(b.Listeners, b.GOOS))
	return s.String()
}

func portOf(url string) string {
	if i := strings.LastIndex(url, ":"); i >= 0 {
		return strings.TrimSuffix(url[i+1:], "/")
	}
	return url
}

// listeningLine is "Listening on UDP 514 and TCP 514" when both are up on one
// port, else a line per listener.
func listeningLine(ls []api.Listener) string {
	if len(ls) == 0 {
		return "  Listening:    nothing (both listeners are turned off in the config)\n"
	}
	var ok []string
	allOK := true
	for _, l := range ls {
		if l.OK {
			ok = append(ok, fmt.Sprintf("%s %d", strings.ToUpper(l.Proto), l.Port))
		} else {
			allOK = false
		}
	}
	where := ""
	if addrs := listenerAddrs(ls); len(addrs) == 1 && !isWildcard(addrs[0]) {
		where = " on " + addrs[0]
	}
	if allOK {
		return "  Listening on " + strings.Join(ok, " and ") + where + "\n"
	}
	var s strings.Builder
	for _, l := range ls {
		if l.OK {
			fmt.Fprintf(&s, "  Listening on %s %d%s\n", strings.ToUpper(l.Proto), l.Port, where)
		} else {
			fmt.Fprintf(&s, "  NOT listening on %s %d: %s\n", strings.ToUpper(l.Proto), l.Port, l.Error)
		}
	}
	return s.String()
}

func listenerAddrs(ls []api.Listener) []string {
	var out []string
	for _, l := range ls {
		if !slices.Contains(out, l.Addr) {
			out = append(out, l.Addr)
		}
	}
	return out
}

func isWildcard(a string) bool { return a == "0.0.0.0" || a == "::" || a == "" }

// listenersHelp explains the listeners that didn't start, and how to fix it:
// once for listeners that failed the same way on the same port.
func listenersHelp(ls []api.Listener, goos string) string {
	type group struct {
		protos []string
		l      api.Listener
	}
	var groups []*group
	byKey := map[string]*group{}
	for _, l := range ls {
		if l.OK {
			continue
		}
		key := fmt.Sprintf("%s|%d|%s", failureClass(l.Error), l.Port, l.Holder)
		g := byKey[key]
		if g == nil {
			g = &group{l: l}
			byKey[key] = g
			groups = append(groups, g)
		}
		g.protos = append(g.protos, strings.ToUpper(l.Proto))
	}
	var s strings.Builder
	for _, g := range groups {
		s.WriteString(listenerHelp(strings.Join(g.protos, " and "), g.l, goos))
	}
	return s.String()
}

func failureClass(e string) string {
	e = strings.ToLower(e)
	switch {
	case strings.Contains(e, "permission denied") || strings.Contains(e, "access is denied") || strings.Contains(e, "forbidden by its access permissions"):
		return "denied"
	case strings.Contains(e, "in use") || strings.Contains(e, "only one usage"):
		return "inuse"
	}
	return "other"
}

// listenerHelp explains one failure; what is "UDP", "TCP" or "UDP and TCP".
func listenerHelp(what string, l api.Listener, goos string) string {
	var s strings.Builder
	switch failureClass(l.Error) {
	case "denied":
		if goos == "linux" && l.Port < 1024 {
			fmt.Fprintf(&s, "\n  %s %d need extra rights on Linux. Allow this program to use the port once, then run it\n", what, l.Port)
			s.WriteString("  as yourself:\n      sudo setcap cap_net_bind_service=+ep ./woodpile\n")
			s.WriteString("  or run it as root for this time only:\n      sudo ./woodpile\n")
		} else {
			fmt.Fprintf(&s, "\n  %s %d: the system refused the port (%s).\n", what, l.Port, l.Error)
		}
	case "inuse":
		holder := "another program"
		if l.Holder != "" {
			holder = l.Holder
		}
		fmt.Fprintf(&s, "\n  %s %d: in use by %s.\n", what, l.Port, holder)
		if strings.Contains(strings.ToLower(l.Holder), "woodpile") {
			s.WriteString("  That's probably an installed Woodpile service: use it (http://localhost:8514/), or stop it first.\n")
		} else {
			s.WriteString("  It's usually the system's own syslog service. Stop it, then press Retry in the web UI.\n")
		}
	default:
		fmt.Fprintf(&s, "\n  %s %d didn't start: %s. Fix it, then press Retry in the web UI.\n", what, l.Port, l.Error)
	}
	return s.String()
}

// ---- the browser ----

func hasDisplay() bool {
	switch runtime.GOOS {
	case "windows":
		return true
	case "darwin":
		return os.Getenv("SSH_CONNECTION") == "" && os.Getenv("SSH_TTY") == ""
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// browserCommand is the OS's "open this URL in the default browser" command.
func browserCommand(goos, url string) (string, []string) {
	switch goos {
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	case "darwin":
		return "open", []string{url}
	}
	return "xdg-open", []string{url}
}
