//go:build !windows

package index

import (
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
	// A database left 0644 by an older build is tightened on open.
	os.Chmod(path, 0o644)
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Fatalf("not tightened: %v", st.Mode().Perm())
	}
}

func TestIndexOwnedLikeDataDirWhenRoot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	// Pretend to be root. The data dir is ours, so the chown to its owner is allowed
	// for an ordinary user too; this exercises the root path end to end.
	old := geteuid
	geteuid = func() int { return 0 }
	defer func() { geteuid = old }()
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.AddEvent(ctx, Event{Kind: KindStart, Text: "x"})
	d.Close()
	dst, _ := os.Stat(dir)
	want := dst.Sys().(*syscall.Stat_t)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		st, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue // checkpointed away on close
		}
		got := st.Sys().(*syscall.Stat_t)
		if got.Uid != want.Uid || got.Gid != want.Gid {
			t.Errorf("%s owned %d:%d, data dir %d:%d", filepath.Base(p), got.Uid, got.Gid, want.Uid, want.Gid)
		}
	}
}
