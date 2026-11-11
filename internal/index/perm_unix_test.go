//go:build !windows

package index

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestIndexFileModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AddEvent(ctx, Event{Kind: KindStart, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o640 {
			t.Errorf("%s is %v, want 0640", filepath.Base(p), st.Mode().Perm())
		}
	}
	d.Close()
	os.Chmod(path, 0o644) // left more open by an older build: tightened on open
	if d, err = Open(path); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Fatalf("not tightened: %v", st.Mode().Perm())
	}
}

// TestIndexRefusesPlantedFiles: whoever owns the data dir can plant links where the
// index files go. Open refuses each kind, and the target outside the dir is untouched.
func TestIndexRefusesPlantedFiles(t *testing.T) {
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "shadow")
	os.WriteFile(victim, []byte("secret"), 0o600)
	cases := []struct {
		name  string
		plant func(t *testing.T, path string)
	}{
		{"symlink index.db", func(t *testing.T, p string) { os.Symlink(victim, p) }},
		{"symlink -shm", func(t *testing.T, p string) { os.Symlink(victim, p+"-shm") }},
		{"symlink -wal", func(t *testing.T, p string) { os.Symlink(victim, p+"-wal") }},
		{"dangling symlink index.db", func(t *testing.T, p string) { os.Symlink(filepath.Join(victimDir, "preload"), p) }},
		{"hard link index.db", func(t *testing.T, p string) {
			if err := os.Link(victim, p); err != nil {
				t.Skip("hard links not allowed here:", err)
			}
		}},
		{"FIFO -shm", func(t *testing.T, p string) {
			if err := syscall.Mkfifo(p+"-shm", 0o600); err != nil {
				t.Skip(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			c.plant(t, path)
			d, err := Open(path)
			if err == nil {
				d.Close()
				t.Fatal("opened a planted index file")
			}
			if !errors.Is(err, ErrUnsafeFile) {
				t.Fatalf("error %v", err)
			}
			st, _ := os.Stat(victim)
			data, _ := os.ReadFile(victim)
			if st.Mode().Perm() != 0o600 || string(data) != "secret" {
				t.Fatalf("victim changed: %v %q", st.Mode().Perm(), data)
			}
			if _, err := os.Lstat(filepath.Join(victimDir, "preload")); !os.IsNotExist(err) {
				t.Fatal("the dangling link's target was created")
			}
		})
	}
}

func TestIndexRefusesRootInAnotherUsersDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: the temp dir is root's")
	}
	old := geteuid
	geteuid = func() int { return 0 }
	defer func() { geteuid = old }()
	path := filepath.Join(t.TempDir(), FileName) // owned by this (non-root) user
	_, err := Open(path)
	if !errors.Is(err, ErrRunAsOwner) {
		t.Fatalf("root in a user's data dir: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("root created the index before refusing")
	}
}
