//go:build windows

package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestReplaceUnderNonSharingReader: a reader without FILE_SHARE_DELETE blocks
// the replace; Rename retries for about 1 s, then fails and leaves the file and
// no temp behind.
func TestReplaceUnderNonSharingReader(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	if err := WriteFile(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	name, _ := windows.UTF16PtrFromString(p)
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = WriteFile(p, []byte("two"), 0o600)
	windows.CloseHandle(h)
	if err == nil || !strings.Contains(err.Error(), "after 5 tries") {
		t.Fatalf("err %v, want the retry-then-error path", err)
	}
	if b, _ := ReadFile(p); string(b) != "one" {
		t.Errorf("file now %q", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("temp file left: %v", ents)
	}
	// Once the reader is gone the replace works.
	if err := WriteFile(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRenameByHandleReplaces: the POSIX-semantics rename itself (no fallback).
func TestRenameByHandleReplaces(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.WriteFile(a, []byte("new"), 0o600)
	os.WriteFile(b, []byte("old"), 0o600)
	r, err := Open(b)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := renameByHandle(a, b); err != nil {
		t.Fatalf("renameByHandle: %v", err)
	}
	if got, _ := ReadFile(b); string(got) != "new" {
		t.Errorf("b = %q", got)
	}
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Errorf("a still there: %v", err)
	}
}
