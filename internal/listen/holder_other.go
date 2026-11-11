//go:build !darwin && !linux && !windows

package listen

func portHolder(string, int) string { return "" }
