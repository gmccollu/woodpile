package service

import (
	"errors"
	"fmt"
	"os"
	"path"
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

// systemFolders are folders a data folder must never be exactly: an install
// would take them over and an uninstall --delete-logs would delete them.
func systemFolders(goos string) []string {
	switch goos {
	case "windows":
		return []string{winEnv("ProgramData", `C:\ProgramData`), winParent(winEnv("USERPROFILE", `C:\Users\x`)), `C:\Users`}
	case "darwin":
		return []string{"/Applications", "/Users", "/private", "/private/tmp", "/usr/local", "/tmp", "/opt", "/Volumes", "/cores"}
	}
	return []string{"/home", "/media", "/mnt", "/opt", "/root", "/srv", "/tmp", "/usr/local"}
}

// systemTrees are folders a data folder must not be inside either (re-test N12),
// with the exceptions that are Woodpile's own (or a service's) places.
func systemTrees(goos string) (trees []string, allowed func(dir string) bool) {
	switch goos {
	case "windows":
		return []string{winEnv("SystemRoot", `C:\Windows`), winEnv("ProgramFiles", `C:\Program Files`),
			winEnv("ProgramFiles(x86)", `C:\Program Files (x86)`)}, func(string) bool { return false }
	case "darwin":
		return []string{"/System", "/Library", "/usr", "/bin", "/sbin", "/etc", "/var", "/private/etc", "/private/var", "/dev", "/cores"},
			func(dir string) bool {
				return within(dir, "/Library/Application Support/Woodpile", true) || varLibService(dir, "/var") || varLibService(dir, "/private/var")
			}
	}
	return []string{"/usr", "/etc", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/boot", "/proc", "/sys", "/dev", "/run", "/var"},
		func(dir string) bool { return varLibService(dir, "/var") }
}

// varLibService is /var/lib/<name> or below (a service's state folder).
func varLibService(dir, varDir string) bool {
	lib := varDir + "/lib"
	return within(dir, lib, false) && dir != lib
}

// within reports whether dir is root or inside it (Unix paths; case-insensitive
// with fold, as on macOS).
func within(dir, root string, fold bool) bool {
	if fold {
		dir, root = strings.ToLower(dir), strings.ToLower(root)
	}
	return dir == root || strings.HasPrefix(dir, strings.TrimSuffix(root, "/")+"/")
}

func winEnv(k, def string) string {
	if v := os.Getenv(k); v != "" && os.PathSeparator == '\\' {
		return v
	}
	return def
}

// winClean normalizes a Windows path for comparison: / to \, the \\?\ and
// \\?\UNC\ prefixes removed, trailing dots and spaces dropped from every
// component (Windows ignores them, so C:\Windows. is C:\Windows), no trailing
// separator. 8.3 short names are expanded by longPath first, where it can.
func winClean(p string) string {
	p = strings.ReplaceAll(p, "/", `\`)
	switch {
	case strings.HasPrefix(p, `\\?\UNC\`):
		p = `\\` + p[len(`\\?\UNC\`):]
	case strings.HasPrefix(p, `\\?\`), strings.HasPrefix(p, `\\.\`):
		p = p[4:]
	}
	parts := strings.Split(p, `\`)
	for i, c := range parts {
		if i == 0 && strings.HasSuffix(c, ":") {
			continue
		}
		parts[i] = strings.TrimRight(c, ". ")
	}
	return strings.TrimRight(strings.Join(parts, `\`), `\`)
}

func winParent(p string) string {
	p = winClean(p)
	if i := strings.LastIndex(p, `\`); i > 0 {
		return p[:i]
	}
	return p
}

func winWithin(dir, root string) bool {
	dir, root = strings.ToLower(winClean(dir)), strings.ToLower(winClean(root))
	return dir == root || strings.HasPrefix(dir, root+`\`)
}

// checkDataDir refuses a data folder that is a system folder or inside one, a
// drive root, a home folder or the parent of home folders (M2, re-test N12), or
// not a clean absolute path.
func checkDataDir(goos, dir string, homes []string) error {
	if goos == "windows" {
		// The raw checks (control characters, ..) before longPath resolves anything.
		if err := checkWindowsDataDir(dir, nil, true); err != nil {
			return err
		}
		return checkWindowsDataDir(longPath(dir), homes, false)
	}
	// Unix paths with Unix rules (package path), whatever OS this runs on.
	if err := checkUnixPath("the data folder", dir); err != nil {
		return err
	}
	fold := goos == "darwin"
	same := func(a, b string) bool {
		a, b = path.Clean(a), path.Clean(b)
		if fold {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	if dir == "/" {
		return fmt.Errorf("the data folder can't be the root folder")
	}
	for _, s := range systemFolders(goos) {
		if same(dir, s) {
			return fmt.Errorf("the data folder can't be the system folder %s", dir)
		}
	}
	trees, allowed := systemTrees(goos)
	for _, t := range trees {
		if within(dir, t, fold) && !allowed(dir) {
			return fmt.Errorf("the data folder can't be in the system folder %s (%s)", t, dir)
		}
	}
	for _, h := range homes {
		if h != "" && (same(dir, h) || same(dir, path.Dir(h))) {
			return fmt.Errorf("the data folder can't be a home folder or the folder holding them (%s)", dir)
		}
	}
	for _, p := range []string{"/home", "/Users"} {
		if same(path.Dir(dir), p) {
			return fmt.Errorf("the data folder can't be a home folder (%s)", dir)
		}
	}
	return nil
}

// checkConfigDir is checkDataDir for the config's folder, which may also be a
// folder of its own directly under /etc (the packages use /etc/woodpile).
func checkConfigDir(goos, dir string, homes []string) error {
	if goos == "linux" && path.Dir(dir) == "/etc" && dir != "/etc" {
		return checkUnixPath("the config folder", dir)
	}
	return checkDataDir(goos, dir, homes)
}

// checkUnixPath is checkServicePath with Unix rules (package path).
func checkUnixPath(what, p string) error {
	if p == "" {
		return fmt.Errorf("%s is empty", what)
	}
	for _, r := range p {
		if unicode.IsControl(r) || r == '"' {
			return fmt.Errorf("%s %q contains a control character or a quote", what, p)
		}
	}
	if !path.IsAbs(p) {
		return fmt.Errorf("%s %q isn't an absolute path", what, p)
	}
	if path.Clean(p) != p {
		return fmt.Errorf("%s %q isn't a clean path (no .., //, or trailing slash)", what, p)
	}
	return nil
}

// checkWindowsDataDir checks a Windows data folder; rawOnly checks only what must
// be checked before longPath resolves the path (control characters, quotes, ..).
func checkWindowsDataDir(dir string, homes []string, rawOnly bool) error {
	for _, r := range dir {
		if r < 0x20 || r == '"' {
			return fmt.Errorf("the data folder %q contains a control character or a quote", dir)
		}
	}
	c := winClean(dir)
	if len(c) < 3 || c[1] != ':' || c[2] != '\\' {
		if !strings.HasPrefix(c, `\\`) {
			return fmt.Errorf("the data folder %q isn't an absolute path", dir)
		}
	}
	if strings.Contains(`\`+strings.ReplaceAll(dir, "/", `\`)+`\`, `\..\`) {
		return fmt.Errorf("the data folder %q contains ..", dir)
	}
	if rawOnly {
		return nil
	}
	if len(c) <= 2 || !strings.Contains(c[3:], `\`) && len(c) <= 3 {
		return fmt.Errorf("the data folder can't be the root of a drive (%s)", dir)
	}
	same := func(a, b string) bool { return strings.EqualFold(winClean(a), winClean(b)) }
	for _, s := range systemFolders("windows") {
		if same(c, s) {
			return fmt.Errorf("the data folder can't be the system folder %s", dir)
		}
	}
	trees, _ := systemTrees("windows")
	for _, t := range trees {
		if winWithin(c, t) {
			return fmt.Errorf("the data folder can't be in the system folder %s (%s)", t, dir)
		}
	}
	for _, h := range homes {
		if h != "" && (same(c, h) || same(c, winParent(h))) {
			return fmt.Errorf("the data folder can't be a home folder or the folder holding them (%s)", dir)
		}
	}
	if same(winParent(c), `C:\Users`) {
		return fmt.Errorf("the data folder can't be a home folder (%s)", dir)
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
