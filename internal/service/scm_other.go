//go:build !windows

package service

func windowsServiceAccount(string) (bool, string, error) { return false, "", nil }
