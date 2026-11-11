//go:build !windows

package server

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The lock file must never be followed to another file: a root reindex on a
// folder the service account controls would truncate it (security review M1e).
func TestLockRefusesLinks(t *testing.T) {
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("precious\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, plant := range map[string]func(lock string) error{
		"symlink":  func(lock string) error { return os.Symlink(victim, lock) },
		"hardlink": func(lock string) error { return os.Link(victim, lock) },
		"fifo":     func(lock string) error { return syscall.Mkfifo(lock, 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			lock := filepath.Join(dir, LockFileName)
			if err := plant(lock); err != nil {
				t.Skip(err)
			}
			if l, err := LockDataDir(dir); err == nil {
				l.Unlock()
				t.Fatal("LockDataDir accepted it")
			}
			if _, err := DataDirInUse(dir); err == nil {
				t.Error("DataDirInUse accepted it")
			}
			if b, _ := os.ReadFile(victim); string(b) != "precious\n" {
				t.Fatalf("the target was changed: %q", b)
			}
		})
	}
}
