package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// LockFileName is the file in the data dir that a running Woodshed holds an
// exclusive lock on, so that two servers (or a server and `woodshed reindex`)
// never use one data dir at once. The lock is the OS's (flock, LockFileEx), so it
// goes away with the process, even after a crash.
const LockFileName = "woodshed.lock"

// errNotPlainFile is returned for a lock file that's a symlink, a hard link or not
// a regular file.
var errNotPlainFile = errors.New("is a link or not a regular file; refusing to use it (remove it if it shouldn't be there)")

// ErrLocked means another process holds the data dir's lock.
var ErrLocked = errors.New("another Woodshed process is using this data folder")

// DirLock is a held data-dir lock.
type DirLock struct {
	f *os.File
}

// LockDataDir takes the data dir's lock without waiting. It returns an error
// wrapping ErrLocked when another process holds it.
func LockDataDir(dataDir string) (*DirLock, error) {
	path := filepath.Join(dataDir, LockFileName)
	// Never through a symlink or a hard link: a root `woodshed reindex` on a folder
	// the service account controls must not truncate some other file.
	f, err := openLockFile(path, true)
	if err != nil {
		return nil, fmt.Errorf("open the lock file: %w", err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		if errors.Is(err, ErrLocked) {
			return nil, fmt.Errorf("%w (%s)", ErrLocked, dataDir)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	// The PID is only a hint for people; the lock is what counts.
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)
	return &DirLock{f: f}, nil
}

// Unlock releases the lock.
func (l *DirLock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := unlockFile(l.f)
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	l.f = nil
	return err
}

// DataDirInUse reports whether a running Woodshed holds dataDir's lock. It
// returns false when the lock file doesn't exist. An error means it couldn't
// tell (for example no permission to open the lock file).
func DataDirInUse(dataDir string) (bool, error) {
	path := filepath.Join(dataDir, LockFileName)
	f, err := openLockFile(path, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		if errors.Is(err, ErrLocked) {
			return true, nil
		}
		return false, err
	}
	_ = unlockFile(f)
	return false, nil
}
