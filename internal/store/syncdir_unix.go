//go:build !windows

package store

import "os"

// syncDir makes renames and new files in dir durable.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		syncFile(d)
		d.Close()
	}
}
