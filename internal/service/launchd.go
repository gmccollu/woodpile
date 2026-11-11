package service

import (
	"fmt"
	"strings"

	"github.com/gmccollu/woodpile/packaging"
)

// macOS paths, the same as the pkg's (packaging/macos).
const (
	darwinBin       = "/usr/local/bin/woodpile"
	darwinNewsyslog = "/etc/newsyslog.d/com.woodpile.daemon.conf"
	darwinLogDir    = "/Library/Logs/Woodpile"
	darwinPkgConfig = "/Library/Application Support/Woodpile/woodpile.toml"
	darwinPkgData   = "/Library/Application Support/Woodpile"
)

// launchdPlist is the packaged plist (packaging/macos/com.woodpile.daemon.plist)
// with this install's config and data paths (the same as the pkg's by default).
func launchdPlist(cfgPath, dataDir string) (string, error) {
	for what, v := range map[string]string{"the config path": cfgPath, "the data folder": dataDir} {
		if err := checkServicePath(what, v); err != nil {
			return "", err
		}
	}
	p := packaging.LaunchdPlist
	cfgLine := "<string>" + darwinPkgConfig + "</string>"
	wdLine := "<key>WorkingDirectory</key>\n\t<string>" + darwinPkgData + "</string>"
	for _, want := range []string{cfgLine, wdLine, "<string>" + darwinBin + "</string>", "<string>" + DarwinUser + "</string>"} {
		if !strings.Contains(p, want) {
			return "", fmt.Errorf("the packaged plist no longer has %q; update launchdPlist", want)
		}
	}
	p = strings.Replace(p, cfgLine, "<string>"+xmlEscape(cfgPath)+"</string>", 1)
	p = strings.Replace(p, wdLine, "<key>WorkingDirectory</key>\n\t<string>"+xmlEscape(dataDir)+"</string>", 1)
	return strings.Replace(p, "<plist version=\"1.0\">", "<!-- Installed by `woodpile install`; remove it with `woodpile uninstall`. -->\n<plist version=\"1.0\">", 1), nil
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// plistPaths reads the config and data dir back from an installed plist.
func plistPaths(plist string) (cfgPath, dataDir string) {
	unescape := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`).Replace
	if i := strings.Index(plist, "<string>--config</string>"); i >= 0 {
		rest := plist[i+len("<string>--config</string>"):]
		if a := strings.Index(rest, "<string>"); a >= 0 {
			if b := strings.Index(rest[a:], "</string>"); b >= 0 {
				cfgPath = unescape(rest[a+len("<string>") : a+b])
			}
		}
	}
	if i := strings.Index(plist, "<key>WorkingDirectory</key>"); i >= 0 {
		rest := plist[i:]
		if a := strings.Index(rest, "<string>"); a >= 0 {
			if b := strings.Index(rest[a:], "</string>"); b >= 0 {
				dataDir = unescape(rest[a+len("<string>") : a+b])
			}
		}
	}
	return cfgPath, dataDir
}

// ensureMacAccount runs the pkg's own ensure_service_account
// (packaging/macos/scripts/common.sh) to create _woodpile.
func ensureMacAccount(h *host) error { return runPkgFunction(h, "ensure_service_account") }

// runPkgFunction runs one function of the pkg's common.sh. The script goes to sh
// on stdin, so no file in a temp folder another user could swap (review item 6).
func runPkgFunction(h *host, fn string) error {
	out, err := h.runInput(packaging.MacAccountScript+"\nset -e\n"+fn+"\n", "/bin/sh", "-s")
	if out != "" {
		for _, l := range strings.Split(out, "\n") {
			h.say("%s", strings.TrimPrefix(l, "woodpile: "))
		}
	}
	return err
}

func installLaunchd(h *host, o InstallOptions, dataDir string) (Result, error) {
	res := Result{Where: darwinPlist, Exe: darwinBin, Config: o.ConfigPath, DataDir: dataDir, Account: DarwinUser}
	if err := ensureMacAccount(h); err != nil {
		return res, fmt.Errorf("create the %s account: %w", DarwinUser, err)
	}
	notes, err := prepareFolders(h, "darwin", o.ConfigPath, dataDir, DarwinUser, DarwinUser)
	res.Notes = append(res.Notes, notes...)
	if err != nil {
		return res, err
	}
	// The daemon's own log folder, exactly as the pkg sets it (prepare_log_dir):
	// root-owned, with one _woodpile-owned log file and no symlinks, so the
	// service account can't point root at another file.
	if err := runPkgFunction(h, "prepare_log_dir"); err != nil {
		return res, fmt.Errorf("prepare %s: %w", darwinLogDir, err)
	}

	if err := h.copyFile(o.Exe, darwinBin, 0o755); err != nil {
		return res, fmt.Errorf("copy the binary to %s: %w", darwinBin, err)
	}
	h.say("installed %s", darwinBin)
	plist, err := launchdPlist(o.ConfigPath, dataDir)
	if err != nil {
		return res, err
	}
	if err := h.writeFile(darwinPlist, plist, 0o644); err != nil {
		return res, err
	}
	if _, err := h.run("chown", "root:wheel", darwinPlist); err != nil {
		return res, err
	}
	if err := h.writeFile(darwinNewsyslog, packaging.NewsyslogConf, 0o644); err != nil {
		return res, err
	}
	h.say("installed %s (runs as %s; its own log is %s/woodpile.log)", darwinPlist, DarwinUser, darwinLogDir)

	target := "system/" + DarwinName
	_, _ = h.run("launchctl", "enable", target)
	if _, err := h.run("launchctl", "print", target); err == nil {
		_, _ = h.run("launchctl", "bootout", target)
	}
	if _, err := h.run("launchctl", "bootstrap", "system", darwinPlist); err != nil {
		return res, fmt.Errorf("the daemon is installed but didn't load (see %s/woodpile.log): %w", darwinLogDir, err)
	}
	return res, nil
}

func uninstallLaunchd(h *host, o UninstallOptions) (Result, error) {
	res := Result{Where: darwinPlist, Exe: darwinBin, Account: DarwinUser}
	plist, err := h.readFile(darwinPlist)
	if err != nil {
		return res, err
	}
	cfgPath, dataDir := plistPaths(plist)
	res.Config, res.DataDir = cfgPath, dataDir
	target := "system/" + DarwinName
	if _, err := h.run("launchctl", "print", target); err == nil {
		if _, err := h.run("launchctl", "bootout", target); err != nil {
			return res, err
		}
	}
	for _, f := range []string{darwinPlist, darwinNewsyslog, darwinBin} {
		if err := h.remove(f); err != nil {
			return res, err
		}
		h.say("removed %s", f)
	}
	if err := deleteData(h, "darwin", o, &res, cfgPath, dataDir); err != nil {
		return res, err
	}
	if o.DeleteLogs {
		if err := h.removeAll(darwinLogDir); err != nil {
			return res, err
		}
		h.say("deleted %s", darwinLogDir)
		// The account goes too, as with the pkg's uninstall.sh --delete-logs.
		if _, err := h.run("dscl", ".", "-read", "/Users/"+DarwinUser); err == nil {
			if _, err := h.run("dscl", ".", "-delete", "/Users/"+DarwinUser); err != nil {
				return res, err
			}
		}
		if _, err := h.run("dscl", ".", "-read", "/Groups/"+DarwinUser); err == nil {
			if _, err := h.run("dscl", ".", "-delete", "/Groups/"+DarwinUser); err != nil {
				return res, err
			}
		}
		h.say("deleted the %s account", DarwinUser)
	}
	return res, nil
}

func statusLaunchd(h *host) (State, error) {
	if !h.exists(darwinPlist) {
		return StateNotInstalled, nil
	}
	// `launchctl print system/...` works without root, unlike `launchctl list`.
	out, err := h.run("launchctl", "print", "system/"+DarwinName)
	if err != nil {
		return StateStopped, nil // installed but not loaded
	}
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "state = "); ok {
			if v == "running" {
				return StateRunning, nil
			}
			return StateStopped, nil
		}
	}
	return StateUnknown, nil
}

func restartLaunchd(h *host) error {
	_, err := h.run("launchctl", "kickstart", "-k", "system/"+DarwinName)
	return err
}
