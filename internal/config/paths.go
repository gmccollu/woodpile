package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultConfigPath returns where the service reads its config on this OS
// (docs/service-contract.md):
//
//   - Linux: /etc/woodpile/woodpile.toml
//   - macOS: /Library/Application Support/Woodpile/woodpile.toml
//   - Windows: C:\ProgramData\Woodpile\woodpile.toml (%ProgramData% if set)
func DefaultConfigPath() string {
	c, _ := pathsFor(runtime.GOOS, os.Getenv)
	return c
}

// DefaultDataDir returns the default data directory on this OS:
// /var/lib/woodpile on Linux, /Library/Application Support/Woodpile on macOS and
// C:\ProgramData\Woodpile on Windows.
func DefaultDataDir() string {
	_, d := pathsFor(runtime.GOOS, os.Getenv)
	return d
}

func pathsFor(goos string, getenv func(string) string) (configPath, dataDir string) {
	switch goos {
	case "windows":
		base := getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		base = strings.TrimRight(base, `\/`)
		dir := base + `\Woodpile`
		return dir + `\` + FileName, dir
	case "darwin":
		dir := "/Library/Application Support/Woodpile"
		return dir + "/" + FileName, dir
	default: // linux and other Unix
		return "/etc/woodpile/" + FileName, "/var/lib/woodpile"
	}
}

// isAbs reports whether p is an absolute path on this OS (Windows paths need a
// drive or UNC prefix).
func isAbs(p string) bool { return filepath.IsAbs(p) }
