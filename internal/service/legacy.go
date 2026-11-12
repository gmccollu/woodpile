package service

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// Woodshed was called Woodpile until its 1.0 release candidates. The packages move
// those installs over: the MSI by its major upgrade (the same UpgradeCode), the deb
// by Conflicts/Replaces, the rpm by Obsoletes, the pkg by its preinstall script.
// `woodshed install` refuses while an old Woodpile service is still defined, with
// the command that removes it, and `woodshed status` points at it. Old data is never
// migrated or deleted: status and the installers only say where it is.

// The old Woodpile names and paths (pre-1.0 release candidates).
const (
	legacyWindowsName = "Woodpile"
	legacyWindowsKey  = `Software\Woodpile` // the old MSI's registry key
	legacyLinuxUnit   = "/etc/systemd/system/woodpile.service"
	legacyDebList     = "/var/lib/dpkg/info/woodpile.list" // dpkg's file list: the deb is installed
	legacyBin         = "/usr/local/bin/woodpile"          // `woodpile install` on Linux and macOS
	legacyDarwinLabel = "com.woodpile.daemon"
	legacyDarwinPlist = "/Library/LaunchDaemons/" + legacyDarwinLabel + ".plist"
	legacyUninstallSh = "/usr/local/share/woodpile/uninstall.sh" // the old pkg's uninstaller
)

var (
	legacyLinuxPackageUnits = []string{"/usr/lib/systemd/system/woodpile.service", "/lib/systemd/system/woodpile.service"}
	legacyLinuxData         = []string{"/var/lib/woodpile", "/etc/woodpile"}
	legacyDarwinData        = []string{"/Library/Application Support/Woodpile"}
)

// Legacy is what an old Woodpile install left on this machine.
type Legacy struct {
	// Service is the old service's unit, plist, SCM entry or MSI key; "" if none.
	Service string
	// Remove is the command (or the steps) that removes it.
	Remove string
	// Data lists the old data and config folders that exist. Woodshed doesn't use
	// them; they can be deleted.
	Data []string
}

// ServiceNote says that the old service is still there and how to remove it ("" if
// it isn't).
func (l Legacy) ServiceNote() string {
	if l.Service == "" {
		return ""
	}
	return fmt.Sprintf("the old Woodpile service is still installed (%s); remove it with: %s", l.Service, l.Remove)
}

// DataNote says where the old data is and that it can be deleted ("" if there is
// none).
func (l Legacy) DataNote() string {
	if len(l.Data) == 0 {
		return ""
	}
	return fmt.Sprintf("the old Woodpile data in %s isn't used by Woodshed; you can delete it", strings.Join(l.Data, " and "))
}

// DetectLegacy looks for an old Woodpile service and its data on this machine. It
// only reads (stat, the registry), so it needs no administrator rights.
func DetectLegacy() (Legacy, error) { return detectLegacy(runtime.GOOS, osProber{}, os.Getenv) }

func detectLegacy(goos string, p prober, getenv func(string) string) (Legacy, error) {
	var l Legacy
	switch goos {
	case "windows":
		svc, msi, err := p.windowsService(legacyWindowsName, legacyWindowsKey)
		if err != nil {
			return Legacy{}, fmt.Errorf("look for the old Woodpile service: %w", err)
		}
		switch {
		case msi:
			l.Service = `HKLM\` + legacyWindowsKey
			if svc {
				l.Service = `service "` + legacyWindowsName + `"`
			}
			l.Remove = "Settings > Apps > Woodpile > Uninstall (installing the Woodshed MSI also replaces it)"
		case svc:
			l.Service = `service "` + legacyWindowsName + `"`
			exe := windowsDir(getenv, "ProgramFiles", `C:\Program Files`) + `\Woodpile\woodpile.exe`
			if p.exists(exe) {
				l.Remove = `"` + exe + `" uninstall (from an administrator prompt)`
			} else {
				l.Remove = "sc.exe stop Woodpile, then sc.exe delete Woodpile (from an administrator prompt)"
			}
		}
		l.Data = existing(p, windowsDir(getenv, "ProgramData", `C:\ProgramData`)+`\Woodpile`)
	case "darwin":
		if p.exists(legacyDarwinPlist) {
			l.Service = legacyDarwinPlist
			switch {
			case p.exists(legacyUninstallSh):
				l.Remove = "sudo " + legacyUninstallSh
			case p.exists(legacyBin):
				l.Remove = "sudo " + legacyBin + " uninstall"
			default:
				l.Remove = "sudo launchctl bootout system/" + legacyDarwinLabel + "; sudo rm " + legacyDarwinPlist
			}
		}
		l.Data = existing(p, legacyDarwinData...)
	default:
		for _, u := range legacyLinuxPackageUnits {
			if p.exists(u) {
				l.Service = u
				l.Remove = "sudo dnf remove woodpile"
				if p.exists(legacyDebList) {
					l.Remove = "sudo apt remove woodpile"
				}
				break
			}
		}
		if l.Service == "" && p.exists(legacyLinuxUnit) {
			l.Service = legacyLinuxUnit
			if p.exists(legacyBin) {
				l.Remove = "sudo " + legacyBin + " uninstall"
			} else {
				l.Remove = "sudo systemctl disable --now woodpile.service && sudo rm " + legacyLinuxUnit + " && sudo systemctl daemon-reload"
			}
		}
		l.Data = existing(p, legacyLinuxData...)
	}
	return l, nil
}

// checkLegacy refuses an install while the old Woodpile service is defined: both
// would want port 514.
func checkLegacy(l Legacy) error {
	if l.Service == "" {
		return nil
	}
	return fmt.Errorf("Woodpile, Woodshed's old name, is still installed as a service (%s), and the two would compete for port 514; "+
		"remove it first with: %s (its logs and config are kept)", l.Service, l.Remove)
}

// windowsDir is an environment folder (%ProgramData%, %ProgramFiles%) without a
// trailing separator, or def when it isn't set.
func windowsDir(getenv func(string) string, env, def string) string {
	if v := strings.TrimRight(getenv(env), `\/`); v != "" {
		return v
	}
	return def
}

func existing(p prober, paths ...string) []string {
	var out []string
	for _, d := range paths {
		if p.exists(d) {
			out = append(out, d)
		}
	}
	return out
}
