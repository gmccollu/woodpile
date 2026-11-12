package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var errHeld = errors.New("access is denied")

func fake(fails int, err error) (func(string, string) error, *int) {
	calls := 0
	return func(string, string) error {
		calls++
		if calls <= fails {
			return err
		}
		return nil
	}, &calls
}

func TestRenameRetriesTransient(t *testing.T) {
	var slept []time.Duration
	sleep := func(d time.Duration) { slept = append(slept, d) }
	transient := func(err error) bool { return errors.Is(err, errHeld) }

	// Fails twice, then works: three calls, two waits, no error.
	rename, calls := fake(2, errHeld)
	if err := renameWith(rename, transient, sleep, "a", "b"); err != nil || *calls != 3 || len(slept) != 2 {
		t.Fatalf("err %v, calls %d, slept %v", err, *calls, slept)
	}

	// Always held: 5 tries over about 1 s, then the error says so.
	slept = nil
	rename, calls = fake(100, errHeld)
	err := renameWith(rename, transient, sleep, "a", "b")
	var total time.Duration
	for _, d := range slept {
		total += d
	}
	if !errors.Is(err, errHeld) || *calls != 5 || !strings.Contains(err.Error(), "after 5 tries") || total < 900*time.Millisecond || total > 1500*time.Millisecond {
		t.Fatalf("err %v, calls %d, slept %v", err, *calls, slept)
	}

	// Not transient: one try, no wait, the error unchanged.
	slept = nil
	other := errors.New("no such file")
	rename, calls = fake(100, other)
	if err := renameWith(rename, transient, sleep, "a", "b"); err != other || *calls != 1 || len(slept) != 0 {
		t.Fatalf("err %v, calls %d", err, *calls)
	}
}

func TestWriteFileAndRead(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	if err := WriteFile(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Replace it while a reader holds it open (shared on Windows).
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("two"), 0o600); err != nil {
		t.Fatalf("replace under a reader: %v", err)
	}
	f.Close()
	if b, err := ReadFile(p); err != nil || string(b) != "two" {
		t.Fatalf("%q %v", b, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("temp file left: %v", ents)
	}
	if _, err := ReadFile(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}
