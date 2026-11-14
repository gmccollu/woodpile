package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gmccollu/woodshed/internal/config"
	"github.com/gmccollu/woodshed/internal/server"
	"github.com/gmccollu/woodshed/internal/updatecheck"
)

// ---- update-check ----

// cmdUpdateCheck sets updates.check from the machine itself (SPEC §10.2), for
// headless servers and for the installers' WOODSHED_UPDATE_CHECK. Like lan-mode,
// it saves the config and restarts a running service to use it; --no-restart is
// for installers, which start or restart the service themselves.
func cmdUpdateCheck(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("update-check", "on|off|ask", `
Turn the daily update check on or off, or set it back to asking in the UI, on
the Woodshed machine. On, Woodshed fetches one public file once a day,
`+updatecheck.URL+`,
sends nothing about this server, and shows a banner when a newer version is
out; it never downloads or installs anything. Off, it makes no outbound
connections. Run it as an administrator (sudo): it saves the config, creating
it if it doesn't exist yet, and a running service is restarted to use it.`, stderr)
	cfgPath := configFlag(fs)
	noRestart := fs.Bool("no-restart", false, "don't restart a running service (for installers, which start it themselves)")
	var value string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		value, args = args[0], args[1:]
	}
	if code, done := parse(fs, args, stdout); done {
		return code
	}
	if value == "" && fs.NArg() == 1 {
		value = fs.Arg(0)
	} else if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "woodshed update-check: unexpected argument %q\n\n%s", fs.Arg(0), fs.help())
		return 2
	}
	// Installers pass what the admin typed: accept any case and spaces around it.
	v := strings.ToLower(strings.TrimSpace(value))
	if !config.ValidUpdateCheck(v) {
		if value == "" {
			fmt.Fprintf(stderr, "woodshed update-check: say on, off or ask\n\n%s", fs.help())
		} else {
			fmt.Fprintf(stderr, "woodshed update-check: %q isn't on, off or ask; nothing changed\n\n%s", value, fs.help())
		}
		return 2
	}

	abs, err := filepath.Abs(*cfgPath)
	if err != nil {
		return fail(stderr, "update-check", err)
	}
	cfg, exists, err := readOrNewConfig(abs)
	if err != nil {
		return fail(stderr, "update-check", err)
	}
	if err := checkWritable(filepath.Dir(abs)); err != nil {
		return fail(stderr, "update-check", adminErr(err))
	}
	if exists && cfg.Updates.Check == v {
		fmt.Fprintf(stdout, "The update check is already %s; nothing to change.\n", describeCheck(v))
		devNote(stdout)
		return 0
	}
	next := cfg.Clone()
	next.Updates.Check = v
	if exists {
		if errs := next.Validate(&cfg); len(errs) > 0 {
			return fail(stderr, "update-check", fmt.Errorf("%s: %w", abs, errs))
		}
		if err := saveConfigKeepingOwner(abs, next); err != nil {
			return fail(stderr, "update-check", adminErr(err))
		}
	} else {
		// A new config, owned like its folder (an installer runs this as root or
		// SYSTEM before the service first starts and would otherwise create it).
		b, err := config.Marshal(next)
		if err != nil {
			return fail(stderr, "update-check", err)
		}
		if err := writeOwnedLikeDir(filepath.Dir(abs), filepath.Base(abs), b, 0o640); err != nil {
			return fail(stderr, "update-check", adminErr(err))
		}
	}
	switch v {
	case config.UpdateCheckOn:
		fmt.Fprintf(stdout, "Turned the update check on: once a day Woodshed fetches %s (nothing about this server is sent).\n", updatecheck.URL)
	case config.UpdateCheckOff:
		fmt.Fprintln(stdout, "Turned the update check off: Woodshed makes no outbound connections.")
	default:
		fmt.Fprintln(stdout, "Set the update check back to asking: the UI asks once whether to turn it on.")
	}
	devNote(stdout)

	dataDir := next.Storage.DataDir
	if *noRestart {
		if inUse, _ := server.DataDirInUse(dataDir); inUse {
			fmt.Fprintln(stdout, "Woodshed is running; it uses the change when it next starts.")
		}
		return 0
	}
	before, _ := server.ReadState(dataDir)
	switch a := applyToRunning("update-check", dataDir, stderr); a {
	case restarted:
		fmt.Fprintln(stdout, "Restarted the service to use it.")
		waitForRestart(dataDir, before, 20*time.Second)
	case applyFailed:
		return 1
	default:
		applyHint(stdout, a, "it uses the change")
	}
	return 0
}

// readOrNewConfig reads the config at abs, or returns the defaults a server
// would create there when there's no file yet (the folder must exist).
func readOrNewConfig(abs string) (config.Config, bool, error) {
	if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
		if fi, err := os.Stat(filepath.Dir(abs)); err != nil || !fi.IsDir() {
			return config.Config{}, false, fmt.Errorf("no config file at %s, and no folder to create it in", abs)
		}
		return server.NewConfigFor(abs), false, nil
	}
	c, err := readConfig(abs)
	return c, err == nil, err
}

// devNote says so when this binary is a build that never checks.
func devNote(stdout io.Writer) {
	if _, ok := updatecheck.ReleaseBuild(version); !ok || releasesURL == "" {
		fmt.Fprintf(stdout, "(This build, %s, isn't a release: it never checks. The setting applies to release builds.)\n", version)
	}
}

func describeCheck(v string) string {
	switch v {
	case config.UpdateCheckOn:
		return "on"
	case config.UpdateCheckOff:
		return "off"
	}
	return "set to ask in the UI"
}

// updateRows is `woodshed status`'s line for the update check: the setting,
// and the last check from the data folder's state file when it can be read.
func updateRows(w *tabw, cfg config.Config, serverVersion string) {
	if _, ok := updatecheck.ReleaseBuild(serverVersion); !ok || releasesURL == "" {
		w.row("Updates", "no update check in this build ("+serverVersion+")")
		return
	}
	switch cfg.Updates.Check {
	case config.UpdateCheckOff:
		w.row("Updates", "check off ("+asAdmin("update-check on")+" turns it on)")
		return
	case config.UpdateCheckAsk:
		w.row("Updates", "check not set: the UI asks; or run "+asAdmin("update-check on|off"))
		return
	}
	last, result, msg, latest, ok := updatecheck.ReadState(cfg.Storage.DataDir)
	switch {
	case !ok || last == nil:
		w.row("Updates", "check on; no check yet")
	case result == "ok" && latest != nil:
		w.row("Updates", fmt.Sprintf("check on; last check %s: OK, %s is the latest", last.Local().Format("2006-01-02 15:04"), latest.Version))
	default:
		w.row("Updates", fmt.Sprintf("check on; last check %s failed: %s", last.Local().Format("2006-01-02 15:04"), msg))
	}
}
