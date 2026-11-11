//go:build !windows

package main

func consoleProcessCount() int { return 0 }

func windowsFirewallNotes() string { return "" }
