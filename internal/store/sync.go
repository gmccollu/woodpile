package store

import "os"

// syncPath fsyncs a file by path (one whose handle was closed while dirty).
func syncPath(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	err = syncFile(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
