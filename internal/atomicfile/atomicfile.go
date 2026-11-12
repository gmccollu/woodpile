// Package atomicfile holds the file helpers every write-temp-then-rename in
// Woodshed uses, and the matching shared-read open.
//
// On Windows a rename onto a file that another process briefly holds (endpoint
// protection scanning it, a reader without FILE_SHARE_DELETE) fails with
// ERROR_ACCESS_DENIED or ERROR_SHARING_VIOLATION; a moment later it works. Rename
// retries those errors with a short backoff (about 1 s in all). Readers of these
// files should open them with Open or ReadFile, which share delete on Windows, so
// they never cause the failure themselves.
package atomicfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// backoff is the wait before each retry: 5 tries over about 1 s.
var backoff = []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond, 600 * time.Millisecond}

// Rename is os.Rename, retried while the error is transient (Windows: access
// denied, sharing or lock violation). The error after the last try says how many
// were made.
func Rename(oldpath, newpath string) error {
	return renameWith(os.Rename, isTransient, time.Sleep, oldpath, newpath)
}

func renameWith(rename func(string, string) error, transient func(error) bool, sleep func(time.Duration), oldpath, newpath string) error {
	err := rename(oldpath, newpath)
	for i := 0; err != nil && transient(err) && i < len(backoff); i++ {
		sleep(backoff[i])
		err = rename(oldpath, newpath)
		if err != nil && (i == len(backoff)-1 || !transient(err)) {
			return fmt.Errorf("%w (after %d tries)", err, i+2)
		}
	}
	return err
}

// WriteFile writes data to a temp file in path's directory, syncs it and renames
// it over path (with Rename's retries). perm applies on Unix; on Windows the file
// inherits the directory's ACL.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(perm); err != nil && !isWindows {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// Open opens path for reading, letting others rename, replace or delete it while
// it's open (FILE_SHARE_DELETE on Windows; os.Open elsewhere). A missing file is
// os.ErrNotExist.
func Open(path string) (*os.File, error) { return openShared(path) }

// ReadFile is os.ReadFile through Open.
func ReadFile(path string) ([]byte, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
