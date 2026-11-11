//go:build !windows

package winacl

// ProtectForCurrentUser does nothing outside Windows.
func ProtectForCurrentUser(string) error { return nil }

// SharedAccess is Windows-only; Unix folders are checked by mode.
func SharedAccess(string) ([]string, error) { return nil, nil }

// OwnedByCurrentUser is Windows-only.
func OwnedByCurrentUser(string) (bool, error) { return true, nil }

// OwnedByAdmins is Windows-only.
func OwnedByAdmins(string) (bool, string, error) { return false, "", nil }
