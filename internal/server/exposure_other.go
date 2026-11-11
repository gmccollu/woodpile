//go:build !windows

package server

// dataDirExposure is Windows-only: Unix data folders are checked by owner and
// mode where they're adopted (portable mode) or created.
func dataDirExposure(string) ([]string, error) { return nil, nil }
