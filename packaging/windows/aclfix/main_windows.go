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
package main

import (
	"fmt"
	"os"

	"github.com/gmccollu/woodshed/internal/winacl"
)

func main() {
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
