package service

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gmccollu/woodpile/packaging"
)

// Linux paths for `woodpile install` (the deb/rpm use /usr/bin and
// /usr/lib/systemd/system).
const (
	linuxBin       = "/usr/local/bin/woodpile"
	linuxSysctl    = "/etc/sysctl.d/60-woodpile.conf"
	linuxPkgExec   = "/usr/bin/woodpile run --service --config /etc/woodpile/woodpile.toml"
	linuxPkgRW     = "ReadWritePaths=/var/lib/woodpile /etc/woodpile"
	linuxPkgWD     = "WorkingDirectory=/var/lib/woodpile"
	installedByTag = "# Installed by `woodpile install`; remove it with `woodpile uninstall`."
)

// systemdUnit is the packaged unit (packaging/linux/woodpile.service) with this
// install's binary, config and data paths. Everything else (the woodpile user,
// CAP_NET_BIND_SERVICE, the sandboxing) is the package's, unchanged.
func systemdUnit(bin, cfgPath, dataDir string) (string, error) {
	u := packaging.SystemdUnit
	for _, want := range []string{linuxPkgExec, linuxPkgRW, linuxPkgWD} {
		if !strings.Contains(u, want) {
			return "", fmt.Errorf("the packaged unit no longer has %q; update systemdUnit", want)
		}
	}
	rw := []string{quoteIfSpace(dataDir)}
	if d := filepath.Dir(cfgPath); d != dataDir {
		rw = append(rw, quoteIfSpace(d))
	}
	u = strings.Replace(u, linuxPkgExec, quoteIfSpace(bin)+" run --service --config "+quoteIfSpace(cfgPath), 1)
	u = strings.Replace(u, linuxPkgRW, "ReadWritePaths="+strings.Join(rw, " "), 1)
	u = strings.Replace(u, linuxPkgWD, "WorkingDirectory="+quoteIfSpace(dataDir), 1)
	return installedByTag + "\n" + u, nil
}

// nologinShell is the first of the shells the package's postinstall tries.
func nologinShell(h *host) string {
	for _, s := range []string{"/usr/sbin/nologin", "/sbin/nologin"} {
		if h.exists(s) {
			return s
		}
	}
	return "/bin/false"
}

func installSystemd(h *host, o InstallOptions, dataDir string) (Result, error) {
	res := Result{Where: linuxManualUnit, Exe: linuxBin, Config: o.ConfigPath, DataDir: dataDir, Account: LinuxUser}
	if !h.exists("/run/systemd/system") {
		return res, fmt.Errorf("systemd isn't running here, so there's no service to install; run `woodpile run --config %s` instead", o.ConfigPath)
	}
	// The system user and group, exactly as packaging/linux/scripts/postinstall.sh.
	if _, err := h.run("getent", "group", LinuxUser); err != nil {
		if _, err := h.run("groupadd", "--system", LinuxUser); err != nil {
			return res, err
		}
		h.say("created group %s", LinuxUser)
	}
	if _, err := h.run("getent", "passwd", LinuxUser); err != nil {
		if _, err := h.run("useradd", "--system", "--gid", LinuxUser, "--home-dir", "/var/lib/woodpile", "--no-create-home",
			"--shell", nologinShell(h), "--comment", "Woodpile syslog server", LinuxUser); err != nil {
			return res, err
		}
		h.say("created user %s", LinuxUser)
	}

	// Data and config folders, owned by the service user (0750).
	cfgDir := filepath.Dir(o.ConfigPath)
	for _, d := range []string{dataDir, cfgDir} {
		if err := h.mkdirAll(d, 0o750); err != nil {
			return res, err
		}
	}
	owner := LinuxUser + ":" + LinuxUser
	if _, err := h.run("chown", "-R", owner, dataDir); err != nil {
		return res, err
	}
	if cfgDir != dataDir {
		if _, err := h.run("chown", owner, cfgDir); err != nil {
			return res, err
		}
		if h.exists(o.ConfigPath) {
			if _, err := h.run("chown", owner, o.ConfigPath); err != nil {
				return res, err
			}
		}
	}
	if err := h.chmod(dataDir, 0o750); err != nil {
		return res, err
	}

	if err := h.copyFile(o.Exe, linuxBin, 0o755); err != nil {
		return res, fmt.Errorf("copy the binary to %s: %w", linuxBin, err)
	}
	h.say("installed %s", linuxBin)

	unit, err := systemdUnit(linuxBin, o.ConfigPath, dataDir)
	if err != nil {
		return res, err
	}
	if err := h.writeFile(linuxManualUnit, unit, 0o644); err != nil {
		return res, err
	}
	h.say("installed %s (runs as %s with CAP_NET_BIND_SERVICE only)", linuxManualUnit, LinuxUser)

	// The receive-buffer drop-in, as the package installs it.
	if err := h.writeFile(linuxSysctl, packaging.SysctlDropIn, 0o644); err != nil {
		return res, err
	}
	if _, err := h.run("sysctl", "-q", "-p", linuxSysctl); err != nil {
		res.Notes = append(res.Notes, fmt.Sprintf("couldn't apply %s now (%v); it applies at the next boot", linuxSysctl, err))
	}

	if _, err := h.run("systemctl", "daemon-reload"); err != nil {
		return res, err
	}
	if _, err := h.run("systemctl", "enable", LinuxName+".service"); err != nil {
		return res, err
	}
	if _, err := h.run("systemctl", "start", LinuxName+".service"); err != nil {
		return res, fmt.Errorf("the service is installed but didn't start (see journalctl -u woodpile): %w", err)
	}
	return res, nil
}

// unitPaths reads the binary, config and data dir back from an installed unit.
func unitPaths(unit string) (bin, cfgPath, dataDir string) {
	for _, line := range strings.Split(unit, "\n") {
		switch {
		case strings.HasPrefix(line, "ExecStart="):
			f := splitUnitWords(strings.TrimPrefix(line, "ExecStart="))
			if len(f) > 0 {
				bin = f[0]
			}
			for i := 0; i+1 < len(f); i++ {
				if f[i] == "--config" {
					cfgPath = f[i+1]
				}
			}
		case strings.HasPrefix(line, "WorkingDirectory="):
			if f := splitUnitWords(strings.TrimPrefix(line, "WorkingDirectory=")); len(f) > 0 {
				dataDir = f[0]
			}
		}
	}
	return bin, cfgPath, dataDir
}

// splitUnitWords splits a unit value on spaces, honouring double quotes.
func splitUnitWords(s string) []string {
	var out []string
	var cur strings.Builder
	inQ, have := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQ, have = !inQ, true
		case (r == ' ' || r == '\t') && !inQ:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

func uninstallSystemd(h *host, o UninstallOptions) (Result, error) {
	res := Result{Where: linuxManualUnit, Account: LinuxUser}
	unit, err := h.readFile(linuxManualUnit)
	if err != nil {
		return res, err
	}
	bin, cfgPath, dataDir := unitPaths(unit)
	res.Exe, res.Config, res.DataDir = bin, cfgPath, dataDir
	if h.exists("/run/systemd/system") {
		_, _ = h.run("systemctl", "stop", LinuxName+".service")
		_, _ = h.run("systemctl", "disable", LinuxName+".service")
	}
	if err := h.remove(linuxManualUnit); err != nil {
		return res, err
	}
	if h.exists("/run/systemd/system") {
		_, _ = h.run("systemctl", "daemon-reload")
		_, _ = h.run("systemctl", "reset-failed", LinuxName+".service")
	}
	h.say("removed %s", linuxManualUnit)
	if err := h.remove(linuxSysctl); err != nil {
		return res, err
	}
	_, _ = h.run("sysctl", "-q", "--system")
	if bin == linuxBin {
		if err := h.remove(linuxBin); err != nil {
			return res, err
		}
		h.say("removed %s", linuxBin)
	}
	return res, deleteData(h, o, &res, cfgPath, dataDir)
}

// deleteData deletes the data folder and the config with DeleteLogs, or notes
// where they were kept.
func deleteData(h *host, o UninstallOptions, res *Result, cfgPath, dataDir string) error {
	if !o.DeleteLogs {
		if dataDir != "" {
			res.Notes = append(res.Notes, "kept the logs, index and config in "+dataDir)
		}
		return nil
	}
	if dataDir == "" || dataDir == "/" || !filepath.IsAbs(dataDir) {
		return fmt.Errorf("not deleting the data folder %q: it doesn't look like Woodpile's", dataDir)
	}
	if err := h.removeAll(dataDir); err != nil {
		return err
	}
	h.say("deleted %s", dataDir)
	if cfgPath != "" && filepath.Dir(cfgPath) != dataDir {
		if err := h.remove(cfgPath); err != nil {
			return err
		}
		// The config folder goes too if nothing else is in it.
		_ = h.remove(filepath.Dir(cfgPath))
		h.say("deleted %s", cfgPath)
	}
	return nil
}

func statusSystemd(h *host) (State, error) {
	if !h.exists(linuxManualUnit) && !h.exists(linuxPackageUnits[0]) && !h.exists(linuxPackageUnits[1]) {
		return StateNotInstalled, nil
	}
	out, err := h.run("systemctl", "is-active", LinuxName+".service")
	switch strings.TrimSpace(out) {
	case "active", "reloading", "deactivating":
		return StateRunning, nil
	case "inactive", "failed":
		return StateStopped, nil
	case "activating":
		return State("starting"), nil
	}
	if err != nil {
		return StateUnknown, err
	}
	return StateUnknown, nil
}

func restartSystemd(h *host) error {
	_, err := h.run("systemctl", "restart", LinuxName+".service")
	return err
}
