package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gmccollu/woodshed/internal/config"
)

// Tests for the installer findings of the 1.0 security review (M1, M2). The
// service account can write its own config, so a hostile config must not steer a
// root install or uninstall.

func linuxHost(t *testing.T) (*host, *fakeRunner) {
	r := &fakeRunner{}
	h := fakeHost(t, r, "/run/systemd/system")
	return h, r
}

func writeHostileConfig(t *testing.T, h *host, path, dataDir string) {
	t.Helper()
	c := config.Default()
	c.Storage.DataDir = dataDir
	if err := config.Save(h.path(path), c); err != nil {
		t.Fatal(err)
	}
}

// M1a: a data_dir with a newline must never reach the unit as a directive.
func TestM1aNewlineInPathsNeverReachesTheUnit(t *testing.T) {
	unixOnly(t)
	if _, err := systemdUnit(linuxBin, "/etc/woodshed/woodshed.toml", "/var/lib/woodshed\nExecStartPre=+/tmp/x"); err == nil {
		t.Fatal("systemdUnit accepted a newline")
	}
	for _, bad := range []string{"/var/lib/wp\"x", "relative/dir", "/var/lib/../etc", "/var/lib/wp\\x", "/a\x00b"} {
		if _, err := systemdUnit(linuxBin, "/etc/woodshed/woodshed.toml", bad); err == nil {
			t.Errorf("systemdUnit accepted %q", bad)
		}
	}
	if u, err := systemdUnit(linuxBin, "/etc/woodshed/woodshed.toml", "/srv/100%logs"); err != nil || !strings.Contains(u, "WorkingDirectory=/srv/100%%logs\n") {
		t.Errorf("%% isn't escaped: %v\n%s", err, u)
	}
	if _, err := launchdPlist(darwinPkgConfig, "/x\n<key>"); err == nil {
		t.Error("launchdPlist accepted a newline")
	}

	// And the config's data_dir isn't used at all: the install refuses the mismatch.
	h, r := linuxHost(t)
	writeHostileConfig(t, h, "/etc/woodshed/woodshed.toml", "/var/lib/woodshed\nExecStartPre=+/tmp/x")
	_, err := installSystemd(h, InstallOptions{ConfigPath: "/etc/woodshed/woodshed.toml", Exe: fakeExe(t)}, "/var/lib/woodshed")
	if err == nil || !strings.Contains(err.Error(), "names data_dir") {
		t.Fatalf("install with a hostile config: %v", err)
	}
	if h.exists(linuxManualUnit) || len(r.chowned) > 0 {
		t.Error("it went ahead anyway")
	}
}

// M1b, M2: system folders are never taken over, whether named by the config or
// on the command line.
func TestM1bM2SystemFoldersAreRefused(t *testing.T) {
	unixOnly(t)
	for _, dir := range []string{"/", "/etc", "/usr", "/var", "/var/lib", "/home", "/root"} {
		if err := checkDataDir("linux", dir, nil); err == nil {
			t.Errorf("linux accepted %s", dir)
		}
	}
	for _, dir := range []string{"/Library", "/System", "/Users", "/private/etc"} {
		if err := checkDataDir("darwin", dir, nil); err == nil {
			t.Errorf("darwin accepted %s", dir)
		}
	}
	for _, dir := range []string{"/home/alice", "/Users/bob"} {
		if err := checkDataDir("linux", dir, nil); err == nil {
			t.Errorf("accepted the home folder %s", dir)
		}
	}
	if err := checkDataDir("linux", "/srv/x", []string{"/srv/x"}); err == nil {
		t.Error("accepted the caller's home folder")
	}
	for _, ok := range []string{"/var/lib/woodshed", "/srv/syslog/woodshed", "/home/alice/woodshed-data"} {
		if err := checkDataDir("linux", ok, nil); err != nil {
			t.Errorf("refused %s: %v", ok, err)
		}
	}

	// config data_dir="/etc": refused, nothing chowned.
	h, r := linuxHost(t)
	writeHostileConfig(t, h, "/etc/woodshed/woodshed.toml", "/etc")
	if _, err := installSystemd(h, InstallOptions{ConfigPath: "/etc/woodshed/woodshed.toml", Exe: fakeExe(t)}, "/var/lib/woodshed"); err == nil {
		t.Fatal("installed with data_dir=/etc in the config")
	}
	if len(r.chowned) > 0 {
		t.Errorf("chowned %v", r.chowned)
	}
	// --data-dir /etc on the command line: refused too.
	if _, err := installSystemd(h, InstallOptions{ConfigPath: "/etc/woodshed/woodshed.toml", Exe: fakeExe(t)}, "/etc"); err == nil {
		t.Fatal("installed with --data-dir /etc")
	}
	// M2: --config /etc/woodshed.toml would give /etc to the service account.
	h2, _ := linuxHost(t)
	if _, err := installSystemd(h2, InstallOptions{ConfigPath: "/etc/woodshed.toml", Exe: fakeExe(t)}, "/var/lib/woodshed"); err == nil ||
		!strings.Contains(err.Error(), "config's folder") {
		t.Fatalf("--config /etc/woodshed.toml: %v", err)
	}
}

// A folder full of someone else's files isn't adopted (or deleted).
func TestNonEmptyForeignFolderRefused(t *testing.T) {
	unixOnly(t)
	h, _ := linuxHost(t)
	os.MkdirAll(h.path("/srv/stuff"), 0o755)
	os.WriteFile(h.path("/srv/stuff/notes.txt"), []byte("mine"), 0o644)
	if _, err := installSystemd(h, InstallOptions{ConfigPath: "/etc/woodshed/woodshed.toml", Exe: fakeExe(t)}, "/srv/stuff"); err == nil ||
		!strings.Contains(err.Error(), "isn't a Woodshed data folder") {
		t.Fatalf("install into a foreign folder: %v", err)
	}
	var res Result
	if err := deleteData(h, "linux", UninstallOptions{DeleteLogs: true}, &res, "", "/srv/stuff"); err == nil {
		t.Fatal("deleteData deleted a foreign folder")
	}
	if !h.exists("/srv/stuff/notes.txt") {
		t.Fatal("the file is gone")
	}
	for _, sys := range []string{"/etc", "/", "/home"} {
		if err := deleteData(h, "linux", UninstallOptions{DeleteLogs: true}, &res, "", sys); err == nil {
			t.Errorf("deleteData accepted %s", sys)
		}
	}
}

// M1c, M1f: the chown walk doesn't follow symlinks (the config, or links inside
// the data folder) and skips hard links.
func TestM1cM1fChownSkipsLinks(t *testing.T) {
	unixOnly(t)
	h, r := linuxHost(t)
	outside := h.path("/etc/shadow")
	os.MkdirAll(filepath.Dir(outside), 0o755)
	os.WriteFile(outside, []byte("secret"), 0o600)
	data := "/var/lib/woodshed"
	os.MkdirAll(h.path(data+"/logs"), 0o750)
	os.WriteFile(h.path(data+"/logs/a.log"), []byte("x"), 0o640)
	os.Symlink(outside, h.path(data+"/evil-symlink"))
	os.Link(outside, h.path(data+"/evil-hardlink"))
	skipped, err := h.chownTree(data, "woodshed", "woodshed")
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 2 {
		t.Errorf("skipped %d, want 2 (the symlink and the hard link)", skipped)
	}
	for _, p := range r.chowned {
		if strings.Contains(p, "evil") || strings.HasPrefix(p, "/etc") {
			t.Errorf("chowned %s", p)
		}
	}
	if len(r.chowned) != 3 { // the folder, logs/, logs/a.log
		t.Errorf("chowned %v", r.chowned)
	}
	// A symlinked config is refused before anything is chowned.
	h2, r2 := linuxHost(t)
	os.MkdirAll(h2.path("/etc/woodshed"), 0o750)
	os.Symlink(outside, h2.path("/etc/woodshed/woodshed.toml"))
	if _, err := installSystemd(h2, InstallOptions{ConfigPath: "/etc/woodshed/woodshed.toml", Exe: fakeExe(t)}, data); err == nil ||
		!strings.Contains(err.Error(), "is a link") {
		t.Fatalf("symlinked config: %v", err)
	}
	if len(r2.chowned) > 0 {
		t.Errorf("chowned %v", r2.chowned)
	}
	// A symlinked data folder too.
	h3, _ := linuxHost(t)
	os.MkdirAll(h3.path("/var/lib"), 0o755)
	os.Symlink(h3.path("/etc"), h3.path(data))
	if _, err := installSystemd(h3, InstallOptions{ConfigPath: "/etc/woodshed/woodshed.toml", Exe: fakeExe(t)}, data); err == nil {
		t.Fatal("symlinked data folder accepted")
	}
}

// M1d: Windows paths too (the checks are shared; this runs everywhere).
func TestM1dWindowsFoldersRefused(t *testing.T) {
	for _, dir := range []string{`C:\`, `C:\Windows`, `C:\Program Files`, `C:\ProgramData`, `C:\Users`, `C:\Users\alice`} {
		if err := checkDataDir("windows", dir, nil); err == nil && filepath.IsAbs(dir) {
			t.Errorf("windows accepted %s", dir)
		}
	}
}

// Re-test N12: system folders are refused as prefixes too, with Woodshed's own
// places allowed; Windows paths are normalized first.
func TestN12SystemPrefixes(t *testing.T) {
	for _, c := range []struct {
		goos, dir string
		ok        bool
	}{
		{"linux", "/usr/share/woodshed", false},
		{"linux", "/etc/woodshed-data", false},
		{"linux", "/lib64/x", false},
		{"linux", "/boot/efi/x", false},
		{"linux", "/var/log/woodshed", false},
		{"linux", "/var/lib", false},
		{"linux", "/var/lib/woodshed", true},
		{"linux", "/var/lib/syslog-archive", true},
		{"linux", "/var/lib/woodshed/sub", true},
		{"linux", "/srv/woodshed", true},
		{"linux", "/opt/woodshed/data", true},
		{"darwin", "/Library/Application Support/Woodshed", true},
		{"darwin", "/library/application support/woodshed", true},
		{"darwin", "/Library/Application Support/Other", false},
		{"darwin", "/Library/Logs/Woodshed", false},
		{"darwin", "/System/Volumes/Data/x", false},
		{"darwin", "/private/var/db/x", false},
		{"windows", `C:\ProgramData\Woodshed`, true},
		{"windows", `D:\syslog\woodshed`, true},
		{"windows", `C:\Windows\Temp\wp`, false},
		{"windows", `C:\Windows.\Temp`, false},
		{"windows", `C:\Program Files\Woodshed\data`, false},
		{"windows", `\\?\C:\Windows\System32\x`, false},
		{"windows", `C:/Windows/x`, false},
		{"windows", `C:\ProgramData`, false},
		{"windows", `C:\ProgramData.`, false},
		{"windows", `C:\Users\alice`, false},
		{"windows", `C:\`, false},
		{"windows", `C:\a\..\Windows`, false},
	} {
		err := checkDataDir(c.goos, c.dir, nil)
		if (err == nil) != c.ok {
			t.Errorf("%s %q: %v, want ok=%v", c.goos, c.dir, err, c.ok)
		}
	}
	if got := winClean(`\\?\C:\Windows. \System32.\`); got != `C:\Windows\System32` {
		t.Errorf("winClean = %q", got)
	}
}
