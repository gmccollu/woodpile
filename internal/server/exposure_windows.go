//go:build windows

package server

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/gmccollu/woodpile/internal/winacl"
)

// trustedSIDs may own or be granted access to a data folder besides this
// process's user: SYSTEM, Administrators, TrustedInstaller, the service's
// virtual account, and OWNER RIGHTS / CREATOR OWNER placeholders.
var trustedSIDs = []string{
	"S-1-5-18",     // SYSTEM
	"S-1-5-32-544", // Administrators
	"S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464", // TrustedInstaller
	winacl.ServiceSID, // NT SERVICE\Woodpile
	"S-1-3-0",         // CREATOR OWNER (inherit-only placeholder)
	"S-1-3-4",         // OWNER RIGHTS
}

// dataDirExposure lists what makes the data folder shared (re-test N2, W1): broad
// groups (Everyone, Users, Authenticated Users, Interactive), an owner that isn't
// this user or a trusted account, and any other account an allow entry names.
// An ordinary user could otherwise pre-create C:\ProgramData\Woodpile with a
// protected ACL naming themselves and be accepted.
func dataDirExposure(dir string) ([]string, error) {
	out, err := winacl.SharedAccess(dir)
	if err != nil {
		return nil, err
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	trusted := func(sid *windows.SID) bool {
		if windows.EqualSid(sid, tu.User.Sid) {
			return true
		}
		s := sid.String()
		for _, t := range trustedSIDs {
			if s == t {
				return true
			}
		}
		return false
	}
	name := func(sid *windows.SID) string {
		if a, d, _, err := sid.LookupAccount(""); err == nil {
			if d != "" {
				return d + `\` + a
			}
			return a
		}
		return sid.String()
	}
	broad := map[string]bool{"S-1-1-0": true, "S-1-5-32-545": true, "S-1-5-11": true, "S-1-5-4": true}
	if owner, _, err := sd.Owner(); err == nil && owner != nil && !trusted(owner) {
		out = append(out, "its owner "+name(owner))
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return out, nil // no DACL: SharedAccess already reported a NULL DACL as Everyone if it is one
	}
	if dacl == nil {
		return append(out, "everyone (no access control list)"), nil
	}
	seen := map[string]bool{}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, fmt.Errorf("read the data folder's ACL: %w", err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		s := sid.String()
		if trusted(sid) || broad[s] || seen[s] {
			continue // broad groups are already in out
		}
		seen[s] = true
		out = append(out, name(sid))
	}
	return out, nil
}
