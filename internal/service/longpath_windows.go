//go:build windows

package service

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// longPath expands 8.3 short names (C:\PROGRA~1) in the longest existing part
// of p (GetLongPathName), so the system-folder checks see real names.
func longPath(p string) string {
	p = winClean(p)
	rest := ""
	for cur := p; ; {
		if l, ok := getLong(cur); ok {
			return filepath.Join(l, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func getLong(p string) (string, bool) {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", false
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetLongPathName(u, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return "", false
	}
	return strings.TrimRight(windows.UTF16ToString(buf[:n]), `\`), true
}
