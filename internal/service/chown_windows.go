//go:build windows

package service

import "errors"

// Ownership on Windows is set through ACLs (winacl); these are for the Unix
// installers only.
var errUnixOnly = errors.New("Unix only")

func (h *host) chownTree(dir, user, group string) (int, error) { return 0, errUnixOnly }
func (h *host) chownFile(path, user, group string) error       { return errUnixOnly }
func (h *host) chownDir(path, user, group string) error        { return errUnixOnly }
