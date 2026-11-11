//go:build !windows

// Command consoleclose is Windows-only (see main_windows.go).
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "consoleclose: Windows only")
	os.Exit(2)
}
