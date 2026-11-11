package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// The installers run as root or an administrator and change ownership,
// permissions and (with --delete-logs) delete the data folder. The service
// account can write its own config, so nothing here may trust the config's
// paths (security review M1, M2): the data folder is the OS default or the one
// the administrator passed to `woodpile install --data-dir`, recorded in a place
// only administrators can write (the unit, the plist, the service's registry
// key), and every path is checked before use.

// checkServicePath checks a path an installer writes into a service definition
// or acts on: absolute, clean, and free of control characters (a newline in a
// unit file adds a directive) and quotes.
func checkServicePath(what, p string) error {
	if p == "" {
		return fmt.Errorf("%s is empty", what)
	}
	for _, r := range p {
		if unicode.IsControl(r) || r == '"' {
			return fmt.Errorf("%s %q contains a control character or a quote", what, p)
		}
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%s %q isn't an absolute path", what, p)
	}
	if filepath.Clean(p) != p {
		return fmt.Errorf("%s %q isn't a clean path (no .., //, or trailing slash)", what, p)
	}
	return nil
}

// systemFolders are folders a data folder must never be: an install would take
// them over and an uninstall --delete-logs would delete them.
func systemFolders(goos string) []string {
	switch goos {
	case "windows":
		env := func(k, def string) string {
			if v := os.Getenv(k); v != "" {
				return v
			}
			return def
		}
		return []string{
			env("SystemRoot", `C:\Windows`), env("ProgramFiles", `C:\Program Files`), env("ProgramFiles(x86)", `C:\Program Files (x86)`),
			env("ProgramData", `C:\ProgramData`), filepath.Dir(env("USERPROFILE", `C:\Users\x`)), `C:\Users`,
		}
	case "darwin":
		return []string{"/Applications", "/Library", "/Library/Application Support", "/System", "/Users", "/private", "/private/etc",
			"/private/var", "/private/tmp", "/usr", "/usr/local", "/bin", "/sbin", "/etc", "/var", "/tmp", "/opt", "/Volumes", "/cores"}
	}
	return []string{"/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib32", "/lib64", "/media", "/mnt", "/opt", "/proc",
		"/root", "/run", "/sbin", "/srv", "/sys", "/tmp", "/usr", "/usr/local", "/var", "/var/lib", "/var/log", "/var/tmp"}
}

// checkDataDir refuses a data folder that is a system folder, a drive root, a
// home folder or the parent of home folders (M2), or not a clean absolute path.
func checkDataDir(goos, dir string, homes []string) error {
	if err := checkServicePath("the data folder", dir); err != nil {
		return err
	}
	same := func(a, b string) bool {
		a, b = filepath.Clean(a), filepath.Clean(b)
		if goos == "windows" || goos == "darwin" {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	if filepath.Dir(dir) == dir || (goos == "windows" && len(strings.TrimRight(dir, `\/`)) <= 2) {
		return fmt.Errorf("the data folder can't be the root of a drive (%s)", dir)
	}
	for _, s := range systemFolders(goos) {
		if same(dir, s) {
			return fmt.Errorf("the data folder can't be the system folder %s", dir)
		}
	}
	for _, h := range homes {
		if h != "" && (same(dir, h) || same(dir, filepath.Dir(h))) {
			return fmt.Errorf("the data folder can't be a home folder or the folder holding them (%s)", dir)
		}
	}
	parent := filepath.Dir(dir)
	for _, p := range []string{"/home", "/Users", `C:\Users`} {
		if same(parent, p) {
			return fmt.Errorf("the data folder can't be a home folder (%s)", dir)
		}
	}
	return nil
}

// woodpileMarkers are entries only a Woodpile data folder has.
var woodpileMarkers = []string{"index.db", "logs", "woodpile.lock", "woodpile.toml"}

// checkDataDirContents refuses an existing data folder that is a link, or is
// neither empty nor a Woodpile data folder (has none of woodpileMarkers).
func (h *host) checkDataDirContents(dir string) error {
	fi, err := os.Lstat(h.path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || !fi.IsDir() {
		return fmt.Errorf("the data folder %s is a link or not a folder", dir)
	}
	entries, err := os.ReadDir(h.path(dir))
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	for _, m := range woodpileMarkers {
		if li, err := os.Lstat(h.path(filepath.Join(dir, m))); err == nil && li.Mode()&os.ModeSymlink == 0 {
			return nil
		}
	}
	return fmt.Errorf("%s isn't empty and isn't a Woodpile data folder (no index.db or logs); pick an empty folder", dir)
}

// homeDirs is this machine's home folders for checkDataDir: the caller's and,
// under sudo, the invoking user's.
func homeDirs() []string {
	var hs []string
	if h, err := os.UserHomeDir(); err == nil {
		hs = append(hs, h)
	}
	if u := os.Getenv("SUDO_USER"); u != "" {
		for _, base := range []string{"/home", "/Users"} {
			hs = append(hs, filepath.Join(base, u))
		}
	}
	return hs
}

// checkConfigPath refuses a config path that is a symlink or not a regular file
// (an install chowns it to the service account).
func (h *host) checkConfigPath(p string) error {
	if err := checkServicePath("the config path", p); err != nil {
		return err
	}
	fi, err := os.Lstat(h.path(p))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return fmt.Errorf("the config %s is a link or not a regular file", p)
	}
	return nil
}
