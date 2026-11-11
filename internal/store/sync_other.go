//go:build !darwin

package store

import "os"

func syncFile(f *os.File) error { return f.Sync() }
