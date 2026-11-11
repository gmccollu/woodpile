//go:build !linux && !darwin && !windows

package store

func lowerThreadPriority() error { return nil }
