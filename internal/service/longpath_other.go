//go:build !windows

package service

// longPath only matters on Windows (8.3 short names); elsewhere it's the identity
// apart from the same normalization the checks apply.
func longPath(p string) string { return p }
