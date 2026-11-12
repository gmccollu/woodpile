package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gmccollu/woodshed/internal/api"
	"github.com/gmccollu/woodshed/internal/config"
	"github.com/gmccollu/woodshed/internal/server"
	"github.com/gmccollu/woodshed/internal/service"
)

// Commands that change the UI's password and mode from the machine itself, for
// servers without a browser (set-password, lan-mode; reset-password is in
// admin.go). They need the same rights as editing the config and the data
// folder, and apply the change to a running Woodshed through applyToRunning.

// The terminal and the run-state file, through these so tests can stand in.
var (
	stdin      io.Reader = os.Stdin
	readSecret           = readSecretTerminal
	// waitForRestart waits for a restarted server's run-state file: one written
	// by a different process than before, or one started later.
	waitForRestart = waitForRunState
)

// ---- apply to the running server ----

// applied says what applyToRunning did.
type applied int

const (
	notRunning  applied = iota // nothing runs on the data folder: the change applies when it starts
	restarted                  // the service was restarted
	foreground                 // a foreground copy runs on it: the user restarts it
	applyFailed                // the service couldn't be restarted (already reported)
)

// applyToRunning makes a running Woodshed pick up what was just written to its
// config and data folder (password hash, LAN mode, port). The server reads both
// only at start, so the service is restarted through its service manager (a
// second or two without listeners); a foreground copy can't be, so the caller
// says how. Restarting also ends every UI session, which a password change
// should.
func applyToRunning(cmd, dataDir string, stderr io.Writer) applied {
	inUse, _ := server.DataDirInUse(dataDir)
	if !inUse {
		return notRunning
	}
	if st, _ := serviceStatus(); st != service.StateRunning {
		return foreground
	}
	if err := serviceRestart(); err != nil {
		fmt.Fprintf(stderr, "woodshed %s: couldn't restart the service: %v\nRestart it yourself to apply the change.\n", cmd, err)
		return applyFailed
	}
	return restarted
}

// applyHint prints what happens next for anything but a restart, and returns
// the exit code for the result.
func applyHint(stdout io.Writer, a applied, when string) int {
	switch a {
	case notRunning:
		fmt.Fprintf(stdout, "Woodshed isn't running; %s when it starts.\n", when)
	case foreground:
		fmt.Fprintf(stdout, "Woodshed is running in the foreground: stop it (Ctrl-C) and start it again, and %s.\n", when)
	case applyFailed:
		return 1
	}
	return 0
}

// asAdmin is how to run a woodshed command with administrator rights here.
func asAdmin(cmd string) string {
	if runtime.GOOS == "windows" {
		return "woodshed " + cmd + " (from an administrator prompt)"
	}
	return "sudo woodshed " + cmd
}

// ---- set-password ----

func cmdSetPassword(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("set-password", "", `
Set the UI password on the Woodshed machine, for servers without a browser.
It asks twice (nothing is shown as you type), or reads one line from stdin with
--stdin. Run it as an administrator (sudo): it writes the password's hash into
the data folder. The first password also turns on ui.require_login_local, as
setting it in the UI does: the UI then asks for it on localhost too. A running
service is restarted to use it.

Then turn LAN mode on with `+"`woodshed lan-mode on`"+`.`, stderr)
	cfgPath := configFlag(fs)
	fromStdin := fs.Bool("stdin", false, "read the password from the first line of stdin")
	if code, done := parse(fs, args, stdout); done {
		return code
	}
	cfg, err := readConfig(*cfgPath)
	if err != nil {
		return fail(stderr, "set-password", err)
	}
	abs, _ := filepath.Abs(*cfgPath)
	dataDir := cfg.Storage.DataDir
	for _, dir := range []string{dataDir, filepath.Dir(abs)} {
		if err := checkWritable(dir); err != nil {
			return fail(stderr, "set-password", adminErr(err))
		}
	}
	had, err := api.PasswordSet(dataDir)
	if err != nil {
		return fail(stderr, "set-password", adminErr(err))
	}

	pw, err := newPassword(*fromStdin, stderr)
	if err != nil {
		return fail(stderr, "set-password", err)
	}
	if msg := api.CheckNewPassword(pw); msg != "" {
		return fail(stderr, "set-password", errors.New("the password isn't accepted: "+msg))
	}
	phc, err := api.HashPassword(pw)
	if err != nil {
		return fail(stderr, "set-password", err)
	}
	if err := writeOwnedLikeDir(dataDir, api.PasswordFile, []byte(phc+"\n"), 0o600); err != nil {
		return fail(stderr, "set-password", adminErr(err))
	}
	if had {
		fmt.Fprintln(stdout, "Changed the UI password.")
	} else {
		fmt.Fprintln(stdout, "Set the UI password.")
	}
	// A first password also protects localhost, as in the UI (PUT /api/password).
	if !had && !cfg.UI.RequireLoginLocal {
		cfg.UI.RequireLoginLocal = true
		if err := saveConfigKeepingOwner(abs, cfg); err != nil {
			return fail(stderr, "set-password", fmt.Errorf("the password is set, but turning on require_login_local failed: %w", adminErr(err)))
		}
		fmt.Fprintf(stdout, "Turned on ui.require_login_local: the UI asks for the password on localhost too.\n"+
			"To turn it off, set require_login_local = false under [ui] in %s (or untick it in Settings) and restart Woodshed.\n", abs)
	}

	a := applyToRunning("set-password", dataDir, stderr)
	if a == restarted {
		fmt.Fprintln(stdout, "Restarted the service to use it (UI sessions were ended).")
	}
	if code := applyHint(stdout, a, "it uses the new password"); code != 0 {
		return code
	}
	if !cfg.UI.LANMode {
		fmt.Fprintln(stdout, "To reach the UI from other machines: "+asAdmin("lan-mode on"))
	}
	return 0
}

// newPassword reads the new password: one line of stdin, or twice from the
// terminal without echo.
func newPassword(fromStdin bool, stderr io.Writer) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !(errors.Is(err, io.EOF) && line != "") {
			if errors.Is(err, io.EOF) {
				return "", errors.New("--stdin: no password on stdin")
			}
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	pw, err := readSecret("New UI password: ", stderr)
	if err != nil {
		return "", err
	}
	if msg := api.CheckNewPassword(pw); msg != "" {
		return "", errors.New("the password isn't accepted: " + msg)
	}
	again, err := readSecret("Again: ", stderr)
	if err != nil {
		return "", err
	}
	if again != pw {
		return "", errors.New("the two passwords don't match; nothing was changed")
	}
	return pw, nil
}

// readSecretTerminal prompts on stderr and reads a line from the terminal on
// stdin with echo off.
func readSecretTerminal(prompt string, stderr io.Writer) (string, error) {
	restore, err := echoOff(os.Stdin)
	if err != nil {
		return "", errors.New("stdin isn't a terminal; pass the password on stdin with --stdin")
	}
	// Ctrl-C mustn't leave the terminal without echo.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			restore()
			fmt.Fprintln(stderr)
			os.Exit(130)
		case <-done:
		}
	}()
	defer func() {
		signal.Stop(sig)
		close(done)
		restore()
		fmt.Fprintln(stderr)
	}()
	fmt.Fprint(stderr, prompt)
	var b []byte
	one := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				break
			}
			b = append(b, one[0])
			if len(b) > 4096 {
				return "", errors.New("the password is too long")
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(b) > 0 {
				break
			}
			return "", err
		}
	}
	return strings.TrimRight(string(b), "\r"), nil
}

// ---- lan-mode ----

func cmdLANMode(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("lan-mode", "on|off", `
Turn LAN mode on or off on the Woodshed machine, for servers without a browser.
On, the UI is served over HTTPS on every interface with a login; it needs a
password first (`+"`woodshed set-password`"+`). Off, it's back on
http://localhost:<port>/ for this machine only. --port also moves the UI. Run it
as an administrator (sudo): it saves the config, and a running service is
restarted to use it.`, stderr)
	cfgPath := configFlag(fs)
	port := fs.Int("port", 0, "UI `port` (default: keep the current one)")
	// The mode may come before or after the flags.
	var mode string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		mode, args = args[0], args[1:]
	}
	if code, done := parse(fs, args, stdout); done {
		return code
	}
	if mode == "" && fs.NArg() == 1 {
		mode = fs.Arg(0)
	} else if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "woodshed lan-mode: unexpected argument %q\n\n%s", fs.Arg(0), fs.help())
		return 2
	}
	if mode != "on" && mode != "off" {
		fmt.Fprintf(stderr, "woodshed lan-mode: say on or off\n\n%s", fs.help())
		return 2
	}
	on := mode == "on"

	cfg, err := readConfig(*cfgPath)
	if err != nil {
		return fail(stderr, "lan-mode", err)
	}
	abs, _ := filepath.Abs(*cfgPath)
	dataDir := cfg.Storage.DataDir
	if err := checkWritable(filepath.Dir(abs)); err != nil {
		return fail(stderr, "lan-mode", adminErr(err))
	}
	if on {
		set, err := api.PasswordSet(dataDir)
		if err != nil {
			return fail(stderr, "lan-mode", adminErr(err))
		}
		if !set {
			return fail(stderr, "lan-mode", errors.New("LAN mode needs a UI password first: run "+asAdmin("set-password")+", then this again"))
		}
	}
	next := cfg.Clone()
	next.UI.LANMode = on
	if *port != 0 {
		next.UI.Port = *port
	}
	if errs := next.Validate(&cfg); len(errs) > 0 {
		if msg, ok := errs["ui.port"]; ok {
			return fail(stderr, "lan-mode", fmt.Errorf("port %d %s (UDP %d, TCP %d)", next.UI.Port, msg,
				next.Listeners.UDP.Port, next.Listeners.TCP.Port))
		}
		return fail(stderr, "lan-mode", fmt.Errorf("%s: %w", abs, errs))
	}
	if next.UI.LANMode == cfg.UI.LANMode && next.UI.Port == cfg.UI.Port {
		fmt.Fprintf(stdout, "LAN mode is already %s on port %d; nothing to change.\n", mode, cfg.UI.Port)
		printUIAddrs(stdout, next, dataDir, nil)
		return 0
	}
	if err := saveConfigKeepingOwner(abs, next); err != nil {
		return fail(stderr, "lan-mode", adminErr(err))
	}
	if on {
		fmt.Fprintf(stdout, "Turned LAN mode on (port %d).\n", next.UI.Port)
	} else {
		fmt.Fprintf(stdout, "Turned LAN mode off (port %d).\n", next.UI.Port)
	}

	before, _ := server.ReadState(dataDir)
	a := applyToRunning("lan-mode", dataDir, stderr)
	var rs *server.RunState
	switch a {
	case restarted:
		fmt.Fprintln(stdout, "Restarted the service to use it.")
		if got, ok := waitForRestart(dataDir, before, 20*time.Second); ok {
			rs = &got
			if got.UI.Error != "" {
				fmt.Fprintf(stdout, "The UI didn't start: %s\n", got.UI.Error)
			}
		}
	case applyFailed:
		return 1
	default:
		applyHint(stdout, a, "the UI changes")
	}
	printUIAddrs(stdout, next, dataDir, rs)
	return 0
}

// printUIAddrs prints where the UI is for cfg: https://<address>:<port>/ for
// each stable address in LAN mode, and the certificate's SHA-256 (from the
// restarted server, else the certificate file).
func printUIAddrs(stdout io.Writer, cfg config.Config, dataDir string, rs *server.RunState) {
	port := strconv.Itoa(cfg.UI.Port)
	if !cfg.UI.LANMode {
		fmt.Fprintf(stdout, "The UI is at http://localhost:%s/ (this machine only).\n", port)
		return
	}
	addrs, _ := api.StableAddrs()
	fmt.Fprintln(stdout, "Open the UI from another machine at:")
	if len(addrs) == 0 {
		fmt.Fprintf(stdout, "  https://<this machine's address>:%s/ (no stable address found)\n", port)
	}
	for _, ip := range addrs {
		fmt.Fprintf(stdout, "  https://%s/\n", net.JoinHostPort(ip.String(), port))
	}
	fp := ""
	if rs != nil {
		fp = rs.UI.CertFingerprint
	}
	if fp == "" {
		fp = certFileFingerprint(cfg, dataDir)
	}
	if fp != "" {
		fmt.Fprintf(stdout, "Certificate SHA-256: %s\n(check it against what the browser shows before you log in)\n", fp)
	} else {
		fmt.Fprintln(stdout, "The certificate is made when the UI starts in LAN mode; then "+asAdmin("status")+" shows its SHA-256.")
	}
}

// certFileFingerprint is the SHA-256 of the configured certificate, or of the
// self-signed one in the data folder ("" if there's none yet).
func certFileFingerprint(cfg config.Config, dataDir string) string {
	path := cfg.UI.CertFile
	if path == "" {
		path = filepath.Join(dataDir, api.CertFile)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	fp, _ := api.CertFingerprintPEM(b)
	return fp
}

// waitForRunState polls the run-state file until a server other than before
// has written it with its UI's outcome, or timeout passes.
func waitForRunState(dataDir string, before server.RunState, timeout time.Duration) (server.RunState, bool) {
	deadline := time.Now().Add(timeout)
	for {
		rs, err := server.ReadState(dataDir)
		if err == nil && (rs.PID != before.PID || rs.StartedAt.After(before.StartedAt)) && (rs.UI.Listening || rs.UI.Error != "") {
			return rs, true
		}
		if time.Now().After(deadline) {
			return server.RunState{}, false
		}
		time.Sleep(200 * time.Millisecond)
	}
}
