//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const isWindows = true

func openShared(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if err == windows.ERROR_FILE_NOT_FOUND || err == windows.ERROR_PATH_NOT_FOUND {
			err = os.ErrNotExist
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// isTransient: another process holds the source or the target for a moment.
func isTransient(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

// FILE_RENAME_INFO_EX flags (not in x/sys/windows).
const (
	fileRenameFlagReplaceIfExists = 0x1
	fileRenameFlagPOSIXSemantics  = 0x2
)

// platformRename renames by handle with POSIX semantics, falling back to
// os.Rename (MoveFileEx) where the file system or Windows version doesn't
// support FileRenameInfoEx.
func platformRename(oldpath, newpath string) error {
	err := renameByHandle(oldpath, newpath)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_SUPPORTED) ||
		errors.Is(err, windows.ERROR_INVALID_FUNCTION) {
		return os.Rename(oldpath, newpath)
	}
	return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
}

// renameByHandle opens oldpath with DELETE access and renames it over newpath
// (FILE_RENAME_INFO_EX: replace if it exists, POSIX semantics).
func renameByHandle(oldpath, newpath string) error {
	target, err := filepath.Abs(newpath)
	if err != nil {
		return err
	}
	src, err := windows.UTF16PtrFromString(oldpath)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(src, windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	name, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	type fileRenameInfoEx struct {
		Flags          uint32
		RootDirectory  windows.Handle
		FileNameLength uint32
		FileName       [1]uint16
	}
	var hdr fileRenameInfoEx
	off := int(unsafe.Offsetof(hdr.FileName))
	size := max(off+len(name)*2, int(unsafe.Sizeof(hdr))) // name includes the terminating NUL
	buf := make([]uint64, (size+7)/8)                     // 8-byte aligned for the handle field
	info := (*fileRenameInfoEx)(unsafe.Pointer(&buf[0]))
	info.Flags = fileRenameFlagReplaceIfExists | fileRenameFlagPOSIXSemantics
	info.FileNameLength = uint32((len(name) - 1) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Add(unsafe.Pointer(&buf[0]), off)), len(name)), name)
	return windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx, (*byte)(unsafe.Pointer(&buf[0])), uint32(size))
}
