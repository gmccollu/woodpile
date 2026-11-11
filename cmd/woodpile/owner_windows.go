//go:build windows

package main

// ownership is a no-op on Windows: the rewritten config inherits the data
// folder's ACL.
type ownership struct{}

func fileOwnership(string) (ownership, error) { return ownership{}, nil }

func (ownership) restore(string) error { return nil }
