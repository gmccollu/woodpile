//go:build !windows

package winacl

// ProtectForCurrentUser does nothing outside Windows.
func ProtectForCurrentUser(string) error { return nil }
