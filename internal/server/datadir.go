package server

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/gmccollu/woodpile/internal/api"
	"github.com/gmccollu/woodpile/internal/winacl"
)

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// makeProtectedDir creates dir (mode 0750) and, on Windows, gives it a protected
// ACL for this account, SYSTEM and Administrators before anything is written in it.
func makeProtectedDir(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return winacl.ProtectForCurrentUser(dir)
}

// sharedAccess is winacl.SharedAccess (faked in tests).
var sharedAccess = winacl.SharedAccess

// ownedByCurrentUser is winacl.OwnedByCurrentUser (faked in tests).
var ownedByCurrentUser = winacl.OwnedByCurrentUser

// protectDir is winacl.ProtectForCurrentUser (faked in tests).
var protectDir = winacl.ProtectForCurrentUser

// checkSharedDataDir checks, on every start, that the data folder doesn't let
// Everyone, Users or Authenticated Users in (re-test N2; Windows only, where a
// folder in ProgramData inherits such entries). Outside the service it fixes a
// folder this user created or owns. Under the service it refuses to start,
// unless opts.AllowSharedDataDir; then, as when it can't fix it, it returns a
// banner (also recorded as an error event).
func checkSharedDataDir(dir string, createdHere bool, opts Options, log *slog.Logger) (*api.Banner, error) {
	groups, err := sharedAccess(dir)
	if err != nil {
		log.Warn("couldn't read the data folder's permissions", "dir", dir, "err", err)
		return nil, nil
	}
	if len(groups) == 0 {
		return nil, nil
	}
	who := strings.Join(groups, ", ")
	if !opts.Service {
		owned, _ := ownedByCurrentUser(dir)
		if createdHere || owned {
			if err := protectDir(dir); err == nil {
				if again, err := sharedAccess(dir); err == nil && len(again) == 0 {
					log.Warn("the data folder let others in; it now allows only this account, SYSTEM and Administrators",
						"dir", dir, "was", who)
					return nil, nil
				}
			}
		}
	}
	text := fmt.Sprintf("The data folder %s lets %s open it, so other users of this machine can read the logs "+
		"or plant files. Restrict it to SYSTEM, Administrators and the service account (reinstall or repair "+
		"Woodpile, or run `woodpile install`).", dir, who)
	if opts.Service && !opts.AllowSharedDataDir {
		log.Error("refusing to start: " + text + " To start anyway, add --allow-shared-data-dir to the service's command line.")
		return nil, fmt.Errorf("the data folder %s lets %s in; refusing to start (fix its permissions, or pass --allow-shared-data-dir)", dir, who)
	}
	log.Error(text, "dir", dir)
	return &api.Banner{Kind: "shared_data_dir", Level: api.LevelError, Text: text}, nil
}
