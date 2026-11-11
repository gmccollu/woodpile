//go:build windows

// Command woodpile-aclfix resets the ACL of Woodpile's Windows data folder and everything
// in it (winacl.ResetDataDir, the same reset `woodpile install` runs). The MSI runs it from its Binary table as a deferred
// custom action, as SYSTEM, after the files are installed and before the service starts:
//
//	woodpile-aclfix.exe "C:\ProgramData\Woodpile"
//
// It exits non-zero (failing the install) if the folder is a link or can't be reset.
package main

import (
	"fmt"
	"os"

	"github.com/gmccollu/woodpile/internal/winacl"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: woodpile-aclfix <data folder>")
		os.Exit(2)
	}
	logf := func(f string, a ...any) { fmt.Printf("woodpile-aclfix: "+f+"\n", a...) }
	if err := winacl.ResetDataDir(os.Args[1], logf); err != nil {
		fmt.Fprintln(os.Stderr, "woodpile-aclfix:", err)
		os.Exit(1)
	}
	logf("reset %s", os.Args[1])
}
