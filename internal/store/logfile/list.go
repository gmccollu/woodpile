package logfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LogsDir is the directory under the data dir holding one directory per device.
const LogsDir = "logs"

// TempSuffix ends the name of every temporary file the store writes (a gzip in
// progress). Leftovers are deleted at startup.
const TempSuffix = ".tmp"

// Entry is a log file found on disk.
type Entry struct {
	DeviceID string
	Name     Name
	Rel      string // path relative to the data dir, '/'-separated
	Path     string // absolute (or data-dir-joined) OS path
	Size     int64
}

// RelPath returns the data-dir-relative path of a device's file.
func RelPath(deviceID string, n Name) string { return LogsDir + "/" + deviceID + "/" + n.String() }

// List walks <dataDir>/logs and returns every log file, ordered by device then name,
// plus the paths of leftover temp files. Other files are ignored.
func List(dataDir string) (files []Entry, temps []string, err error) {
	root := filepath.Join(dataDir, LogsDir)
	devs, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, dv := range devs {
		if !dv.IsDir() {
			continue
		}
		dir := filepath.Join(root, dv.Name())
		ents, err := os.ReadDir(dir)
		if err != nil {
			return nil, nil, err
		}
		for _, e := range ents {
			if !e.Type().IsRegular() {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if strings.HasSuffix(e.Name(), TempSuffix) {
				temps = append(temps, p)
				continue
			}
			n, ok := ParseName(e.Name())
			if !ok {
				continue
			}
			size, err := entrySize(p, e)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			files = append(files, Entry{DeviceID: dv.Name(), Name: n, Rel: RelPath(dv.Name(), n), Path: p, Size: size})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].DeviceID != files[j].DeviceID {
			return files[i].DeviceID < files[j].DeviceID
		}
		return files[i].Rel < files[j].Rel
	})
	return files, temps, nil
}
