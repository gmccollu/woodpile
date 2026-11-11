//go:build !windows

// Command woodpile-aclfix is Windows-only (see main_windows.go).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "woodpile-aclfix runs on Windows only")
	os.Exit(2)
}
