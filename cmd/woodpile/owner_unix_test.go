//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Re-test N13: portable mode adopts only its own folder, config and password hash.
func TestCheckOwnedByMe(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "woodpile.toml")
	os.WriteFile(mine, []byte("x"), 0o640)
	if err := checkOwnedByMe(mine); err != nil {
		t.Errorf("my own file: %v", err)
	}
	if err := checkOwnedByMe(filepath.Join(dir, "missing")); err != nil {
		t.Errorf("a missing file: %v", err)
	}
	link := filepath.Join(dir, "ui-password.hash")
	os.Symlink(mine, link)
	if err := checkOwnedByMe(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("a symlink: %v", err)
	}
	if os.Geteuid() != 0 {
		if err := checkOwnedByMe("/etc/hosts"); err == nil || !strings.Contains(err.Error(), "another user") {
			t.Errorf("root's file: %v", err)
		}
	}
}
