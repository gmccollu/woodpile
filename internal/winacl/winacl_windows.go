//go:build windows

package winacl

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// ProtectForCurrentUser gives dir a protected ACL (nothing inherited from the
// parent, which in ProgramData or a shared folder may let every user in): full
// control for SYSTEM, Administrators and the current user. For a data folder
// that Woodpile just created outside the installer (portable mode, `woodpile run`).
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

// ResetToInherited removes path's explicit entries and makes it inherit its
// parent's, with owner Administrators.
func ResetToInherited(path string) error {
	empty, err := windows.ACLFromEntries(nil, nil)
	if err != nil {
		return err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		admins, nil, empty, nil)
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
	ok := o.IsWellKnown(windows.WinLocalSystemSid) || o.IsWellKnown(windows.WinBuiltinAdministratorsSid) ||
		o.String() == "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464" // TrustedInstaller
	return ok, name, nil
}
