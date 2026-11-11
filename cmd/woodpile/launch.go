package main

import (
	"os/exec"
	"runtime"
)

// launch is how the process was started.
type launch int

const (
	launchTerminal    launch = iota // from a shell in an existing terminal
	launchDoubleClick               // from a file manager (Explorer, Finder, a desktop)
)

// launchProbe is what detectLaunch looks at (faked in tests).
type launchProbe struct {
	goos string
	// consoleProcesses is how many processes share our console (Windows); 1
	// means the console was created just for us, as when Explorer starts us.
	consoleProcesses func() int
	// stdinTerminal reports whether stdin is a terminal (Unix).
	stdinTerminal func() bool
}

func detectLaunch() launch {
	return classifyLaunch(launchProbe{goos: runtime.GOOS, consoleProcesses: consoleProcessCount, stdinTerminal: stdinIsTerminal})
}

// classifyLaunch: on Windows, a console of our own means a double-click; on
// Unix, no terminal on stdin means a file manager started us (Finder opens a
// Terminal window instead, so on macOS a double-click looks like a terminal
// launch, which only adds the one-line note).
func classifyLaunch(p launchProbe) launch {
	if p.goos == "windows" {
		if p.consoleProcesses() == 1 {
			return launchDoubleClick
		}
		return launchTerminal
	}
	if !p.stdinTerminal() {
		return launchDoubleClick
	}
	return launchTerminal
}

// openBrowser opens url in the default browser without waiting for it.
func openBrowser(url string) error {
	name, args := browserCommand(runtime.GOOS, url)
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
