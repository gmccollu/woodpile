//go:build windows

package winacl

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProtectForCurrentUser gives dir a protected ACL (nothing inherited from the
// parent, which in ProgramData or a shared folder may let every user in): full
// control for SYSTEM, Administrators and the current user. For a data folder
// that Woodshed just created outside the installer (portable mode, `woodshed run`).
func ProtectForCurrentUser(dir string) error {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sddl := fmt.Sprintf("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;%s)", tu.User.Sid.String())
	return SetSDDL(dir, sddl, false)
}

// SetSDDL applies an SDDL's DACL (and owner, with owner set) to path as a
// protected DACL; inheritable entries propagate to what's inside.
func SetSDDL(path, sddl string, owner bool) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	var o *windows.SID
	if owner {
		if o, _, err = sd.Owner(); err != nil {
			return err
		}
		info |= windows.OWNER_SECURITY_INFORMATION
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, o, nil, dacl, nil)
}

// OwnedByAdmins reports whether path's owner is SYSTEM, Administrators or
// TrustedInstaller, and names the owner.
func OwnedByAdmins(path string) (bool, string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false, "", err
	}
	o, _, err := sd.Owner()
	if err != nil {
		return false, "", err
	}
	name := o.String()
	if a, d, _, err := o.LookupAccount(""); err == nil {
		name = d + `\` + a
	}
	return trustedOwner(o), name, nil
}

// ResetDataDir makes the data folder dir and everything in it safe for the service:
//
//   - dir itself gets DataSDDL (owner Administrators, the protected DACL);
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
func ResetDataDir(dir string, logf func(format string, args ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	enablePrivileges("SeBackupPrivilege", "SeRestorePrivilege", "SeTakeOwnershipPrivilege")

	root, err := windows.SecurityDescriptorFromString(DataSDDL)
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
// them (best effort: without them, ResetDataDir fails on entries it can't open).
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

// PrepareDataDir is what the MSI runs (packaging/windows/aclfix), before the installer
// creates the folder: EnsureDataDir, then ResetDataDir.
func PrepareDataDir(dir string, logf func(format string, args ...any)) error {
	if err := EnsureDataDir(dir, logf); err != nil {
		return err
	}
	return ResetDataDir(dir, logf)
}

// ensureAttempts bounds EnsureDataDir's create / set-aside loop.
const ensureAttempts = 5

// testHookAfterSetAside, when set (tests only), runs after each set-aside.
var testHookAfterSetAside func()

// EnsureDataDir leaves dir as a folder owned by SYSTEM, Administrators or TrustedInstaller
// (security review N6, W2). A missing dir is created in one step with its final security
// descriptor (DataSDDL: owner Administrators, the protected DACL), so nobody can open it
// between its creation and its ACL. If dir exists and is trusted, it's kept (the caller
// resets it); if it exists and isn't, it's moved aside (SetAsideUntrusted) and creation is
// tried again: whoever keeps re-creating the path is outlasted ensureAttempts times, then
// it fails. The new folder's owner is checked again on a handle to it.
func EnsureDataDir(dir string, logf func(format string, args ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	enablePrivileges("SeBackupPrivilege", "SeRestorePrivilege", "SeTakeOwnershipPrivilege")
	if parent := filepath.Dir(dir); parent != dir {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	for i := 0; i < ensureAttempts; i++ {
		err := createProtectedDir(dir)
		if err == nil {
			return checkTrustedDir(dir)
		}
		if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		moved, err := SetAsideUntrusted(dir, logf)
		if err != nil {
			return err
		}
		if moved == "" {
			return checkTrustedDir(dir) // it exists and is trusted
		}
		if testHookAfterSetAside != nil {
			testHookAfterSetAside()
		}
	}
	return fmt.Errorf("%s was re-created by someone else %d times while Woodshed replaced it; "+
		"find what keeps creating it (another user's program), stop it and install again", dir, ensureAttempts)
}

// createProtectedDir creates dir with DataSDDL as its security descriptor, atomically;
// ERROR_ALREADY_EXISTS if anything is at that path already.
func createProtectedDir(dir string) error {
	sd, err := windows.SecurityDescriptorFromString(DataSDDL)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	return windows.CreateDirectory(p, &sa)
}

// checkTrustedDir opens dir itself (not a link's target) and checks it's a folder owned by
// SYSTEM, Administrators or TrustedInstaller.
func checkTrustedDir(dir string) error {
	h, info, err := openNoFollow(dir)
	if err != nil {
		return fmt.Errorf("data folder %s: %w", dir, err)
	}
	defer windows.CloseHandle(h)
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("data folder %s is a link or not a folder; remove it and install again", dir)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("data folder %s: %w", dir, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("data folder %s: %w", dir, err)
	}
	if !trustedOwner(owner) {
		return fmt.Errorf("data folder %s is owned by %s, not SYSTEM or Administrators, right after it was created; install again", dir, owner.String())
	}
	return nil
}

// SetAsideUntrusted moves dir out of the way if it exists and isn't owned by SYSTEM,
// Administrators or TrustedInstaller (security review N6: a folder an ordinary user
// created before the install, whose open handles would keep their rights through an ACL
// reset). It's renamed, not deleted, to "<dir>.untrusted-<UTC time>-<random>", so
// whatever is in it is kept, and the caller makes a fresh one. The random part (W3) means
// nobody can block the rename by creating the name first; a collision just tries another. It returns the new name, or "" when dir
// doesn't exist or is trusted. A dir that is a link is refused; a rename that's blocked
// (the folder is in use) is an error saying so.
func SetAsideUntrusted(dir string, logf func(format string, args ...any)) (string, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	enablePrivileges("SeBackupPrivilege", "SeRestorePrivilege", "SeTakeOwnershipPrivilege")
	h, info, err := openNoFollow(dir)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("data folder %s: %w", dir, err)
	}
	defer windows.CloseHandle(h)
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return "", fmt.Errorf("data folder %s is a link or not a folder; remove it and install again", dir)
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return "", fmt.Errorf("data folder %s: %w", dir, err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return "", fmt.Errorf("data folder %s: %w", dir, err)
	}
	if trustedOwner(owner) {
		return "", nil
	}
	who := owner.String()
	if a, d, _, err := owner.LookupAccount(""); err == nil {
		who = d + `\` + a
	}
	for i := 0; i < 20; i++ {
		var rnd [4]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return "", err
		}
		target := dir + ".untrusted-" + time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(rnd[:])
		err = renameByHandle(h, target)
		if err == nil {
			logf("%s was owned by %s, not SYSTEM or Administrators: moved it to %s (its contents are kept there) and made a new one", dir, who, target)
			return target, nil
		}
		if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) && !errors.Is(err, windows.ERROR_FILE_EXISTS) {
			break
		}
	}
	return "", fmt.Errorf("%s is owned by %s, not SYSTEM or Administrators, and couldn't be moved aside (%v); "+
		"close whatever is using it (or delete it, or take ownership as an administrator) and install again", dir, who, err)
}

// trustedOwner reports whether o is SYSTEM, Administrators or TrustedInstaller.
func trustedOwner(o *windows.SID) bool {
	return o.IsWellKnown(windows.WinLocalSystemSid) || o.IsWellKnown(windows.WinBuiltinAdministratorsSid) ||
		o.String() == "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464" // TrustedInstaller
}

// renameByHandle renames the file or folder open as h to the absolute path target,
// without replacing anything there (FILE_RENAME_INFO through SetFileInformationByHandle;
// the handle was opened with DELETE and backup semantics, so a hostile DACL can't stop it).
func renameByHandle(h windows.Handle, target string) error {
	name, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	type fileRenameInfo struct {
		ReplaceIfExists uint32
		RootDirectory   windows.Handle
		FileNameLength  uint32
		FileName        [1]uint16
	}
	var hdr fileRenameInfo
	off := int(unsafe.Offsetof(hdr.FileName))
	buf := make([]byte, off+len(name)*2) // name includes the terminating NUL
	info := (*fileRenameInfo)(unsafe.Pointer(&buf[0]))
	info.FileNameLength = uint32((len(name) - 1) * 2)
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buf[off])), len(name)), name)
	return windows.SetFileInformationByHandle(h, windows.FileRenameInfo, &buf[0], uint32(len(buf)))
}

// SharedAccess returns the broad groups (Everyone, Users, Authenticated Users,
// Interactive) that path's DACL allows any access (re-test N2).
func SharedAccess(path string) ([]string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	return SharedTrustees(sd.String()), nil
}

// OwnedByCurrentUser reports whether path's owner is this process's user, or
// Administrators when this process is elevated (an elevated administrator's new
// files are owned by the Administrators group).
func OwnedByCurrentUser(path string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	o, _, err := sd.Owner()
	if err != nil {
		return false, err
	}
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false, err
	}
	if windows.EqualSid(o, tu.User.Sid) {
		return true, nil
	}
	return windows.GetCurrentProcessToken().IsElevated() && o.IsWellKnown(windows.WinBuiltinAdministratorsSid), nil
}
