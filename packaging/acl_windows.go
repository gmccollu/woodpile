//go:build windows

package packaging

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ResetDataACL makes the data folder dir and everything in it safe for the service:
//
//   - dir itself gets WindowsDataSDDL (owner Administrators, the protected DACL);
//   - every file and folder under it gets owner Administrators and only the ACEs it
//     inherits from dir (InheritedDACL), so no explicit grant survives;
//   - reparse points (junctions, symlinks) and extra hard-link names inside it are
//     deleted, not followed: the service never creates them, and following one as
//     SYSTEM would change the ACL of whatever it points at.
//
// It refuses a dir that is itself a reparse point. Every entry is opened by handle
// with FILE_FLAG_OPEN_REPARSE_POINT (the link itself, never its target) and changed
// through that handle. It needs SeBackup/SeRestore/SeTakeOwnership (an elevated
// administrator or SYSTEM, as in the MSI's deferred custom action); run it while the
// service is stopped. logf reports what it removed; it may be nil.
func ResetDataACL(dir string, logf func(format string, args ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	enablePrivileges("SeBackupPrivilege", "SeRestorePrivilege", "SeTakeOwnershipPrivilege")

	root, err := windows.SecurityDescriptorFromString(WindowsDataSDDL)
	if err != nil {
		return err
	}
	owner, _, err := root.Owner()
	if err != nil {
		return err
	}
	rootDACL, _, err := root.DACL()
	if err != nil {
		return err
	}
	childDACL := map[bool]*windows.ACL{}
	for _, isDir := range []bool{true, false} {
		s, err := InheritedDACL(isDir)
		if err != nil {
			return err
		}
		sd, err := windows.SecurityDescriptorFromString(s)
		if err != nil {
			return err
		}
		if childDACL[isDir], _, err = sd.DACL(); err != nil {
			return err
		}
	}

	h, info, err := openNoFollow(dir)
	if err != nil {
		return fmt.Errorf("data folder %s: %w", dir, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		windows.CloseHandle(h)
		return fmt.Errorf("data folder %s is a link or not a folder; remove it and install again", dir)
	}
	err = windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, rootDACL, nil)
	windows.CloseHandle(h)
	if err != nil {
		return fmt.Errorf("data folder %s: %w", dir, err)
	}
	return resetChildren(dir, owner, childDACL, logf)
}

func resetChildren(dir string, owner *windows.SID, dacl map[bool]*windows.ACL, logf func(string, ...any)) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		h, info, err := openNoFollow(p)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		reparse := info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
		switch {
		case reparse || (!isDir && info.NumberOfLinks > 1):
			// Delete this name only: for a link, the link itself; for a hard link, one name.
			why := "link"
			if !reparse {
				why = fmt.Sprintf("hard link (%d names)", info.NumberOfLinks)
			}
			del := byte(1)
			if err := windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &del, uint32(unsafe.Sizeof(del))); err != nil {
				errs = append(errs, fmt.Errorf("removing %s %s: %w", why, p, err))
			} else {
				logf("removed %s %s", why, p)
			}
			windows.CloseHandle(h)
			continue
		}
		err = windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
			owner, nil, dacl[isDir], nil)
		windows.CloseHandle(h)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		if isDir {
			if err := resetChildren(p, owner, dacl, logf); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// openNoFollow opens path itself (a link is opened, not its target) with the rights to
// change its owner and DACL or delete it; backup semantics let SYSTEM past any DACL.
func openNoFollow(path string) (windows.Handle, *windows.ByHandleFileInformation, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, nil, err
	}
	h, err := windows.CreateFile(p,
		windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER|windows.DELETE|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return 0, nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return 0, nil, err
	}
	return h, &info, nil
}

// enablePrivileges turns on the named privileges in this process's token if it holds
// them (best effort: without them, ResetDataACL fails on entries it can't open).
func enablePrivileges(names ...string) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return
	}
	defer tok.Close()
	for _, n := range names {
		var luid windows.LUID
		name, _ := windows.UTF16PtrFromString(n)
		if windows.LookupPrivilegeValue(nil, name, &luid) != nil {
			continue
		}
		tp := windows.Tokenprivileges{PrivilegeCount: 1}
		tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
		_ = windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
	}
}
