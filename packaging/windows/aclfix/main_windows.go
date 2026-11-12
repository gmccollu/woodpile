//go:build windows

// Command woodshed-aclfix resets the ACL of Woodshed's Windows data folder and everything
// in it (winacl.PrepareDataDir: a folder an ordinary user created is moved aside first, then
// the same reset `woodshed install` runs). The MSI runs it from its Binary table as a deferred
// custom action, as SYSTEM, before the installer creates its folders (so it still sees who
// created an existing one) and before the service starts:
//
//	woodshed-aclfix.exe "C:\ProgramData\Woodshed"
//
// It exits non-zero (failing the install) if the folder is a link or can't be reset.
//
// The MSI also runs it after installing the files, to give them the release commit's time:
//
//	woodshed-aclfix.exe --file-times "C:\Program Files\Woodshed\." <unix seconds>
//
// An MSI cabinet stores file times as local time with no zone, and Windows Installer applies
// them as the target machine's local time, so without this a file can look hours newer (or
// older) than it is. Only regular files directly in the folder are touched; links are skipped.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gmccollu/woodshed/internal/winacl"
)

func main() {
	if len(os.Args) == 4 && os.Args[1] == "--file-times" {
		os.Exit(fileTimes(os.Args[2], os.Args[3]))
	}
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: woodshed-aclfix <data folder>")
		os.Exit(2)
	}
	logf := func(f string, a ...any) { fmt.Printf("woodshed-aclfix: "+f+"\n", a...) }
	if err := winacl.PrepareDataDir(os.Args[1], logf); err != nil {
		fmt.Fprintln(os.Stderr, "woodshed-aclfix:", err)
		os.Exit(1)
	}
	logf("reset %s", os.Args[1])
}

// fileTimes sets the modified and access time of every regular file directly in dir to the
// Unix time secs. It's cosmetic, so problems are reported and it still exits 0.
func fileTimes(dir, secs string) int {
	n, err := strconv.ParseInt(secs, 10, 64)
	if err != nil || n <= 0 {
		fmt.Fprintf(os.Stderr, "woodshed-aclfix: --file-times: bad time %q\n", secs)
		return 0
	}
	t := time.Unix(n, 0)
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "woodshed-aclfix: --file-times:", err)
		return 0
	}
	done := 0
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if err := os.Chtimes(filepath.Join(dir, e.Name()), t, t); err != nil {
			fmt.Fprintln(os.Stderr, "woodshed-aclfix: --file-times:", err)
			continue
		}
		done++
	}
	fmt.Printf("woodshed-aclfix: set %d files in %s to %s\n", done, dir, t.UTC().Format(time.RFC3339))
	return 0
}
