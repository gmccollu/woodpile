//go:build !windows

// Command woodshed-aclfix is Windows-only (see main_windows.go).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "woodshed-aclfix runs on Windows only")
	os.Exit(2)
}
