//go:build windows

package main

import (
	"fmt"
	"os"

	"github.com/gmccollu/woodshed/internal/config"
	"github.com/gmccollu/woodshed/internal/winacl"
)

// saveConfigKeepingOwner is config.Save on Windows: the rewritten config inherits
// the data folder's ACL.
func saveConfigKeepingOwner(path string, c config.Config) error { return config.Save(path, c) }

// checkOwnedByMe refuses an existing path that is a link, or owned by someone
// other than this user, SYSTEM or Administrators, before portable mode adopts it
// (re-test N13).
func checkOwnedByMe(path string) error {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("%s is a link; portable mode won't use it", path)
	}
	if mine, err := winacl.OwnedByCurrentUser(path); err != nil || mine {
		return err
	}
	if admins, owner, err := winacl.OwnedByAdmins(path); err != nil || admins {
		return err
	} else {
		return fmt.Errorf("%s belongs to %s; portable mode only uses its own folders. "+
			"Use your own copy, or pass --dir with a folder of yours", path, owner)
	}
}

// runAsOwnerFirst is Unix only: on Windows reindex runs as an administrator.
func runAsOwnerFirst(string) error { return nil }

func runAsOwnerHint(string) string {
	return "Run it from an administrator prompt, with the service stopped."
}

// tightenPortableDir is Unix only: on Windows the server checks the folder's ACL.
func tightenPortableDir(string) (string, error) { return "", nil }
