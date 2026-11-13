//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gmccollu/woodshed/internal/atomicfile"
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

// shellQuote quotes an argument for a command line shown in a hint, when it needs it.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"&|<>^%()") {
		return s
	}
	return `"` + s + `"`
}

// writeOwnedLikeDir atomically writes dir/name; on Windows the file inherits the
// data folder's ACL (perm is ignored).
func writeOwnedLikeDir(dir, name string, data []byte, _ os.FileMode) error {
	f, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = atomicfile.Rename(tmp, filepath.Join(dir, name))
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
