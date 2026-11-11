// Package winacl is the single source for Woodpile's Windows folder ACLs (security
// review M3): the data folder's SDDL (also in the MSI, packaging/windows/woodpile.wxs),
// the reset that the MSI (packaging/windows/aclfix) and `woodpile install` run over the
// data folder, and the protected ACL for folders `woodpile run` and portable mode create.
// Elsewhere it does nothing: Unix folders get mode 0750 from the code that creates them.
package winacl

import (
	"fmt"
	"strings"
)

// The Windows data folder's ACL, the same SDDL as the MSI's PermissionEx: owner
// Administrators, a protected DACL with full control for SYSTEM and Administrators and
// modify (no permission changes) for the service's virtual account NT SERVICE\Woodpile,
// whose SID is S-1-5-80-<SHA-1 of "WOODPILE">.
const (
	ServiceSID = "S-1-5-80-276366627-2944175572-1807786847-528126453-1705305312"
	DataSDDL   = "O:BAG:SYD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1301bf;;;" + ServiceSID + ")"
)

// InheritedDACL returns, as SDDL, the DACL that a folder (dir=true) or file inside the
// data folder has when it only inherits from DataSDDL: each inheritable ACE of the data
// folder, marked inherited (ID). Folders keep the inheritance flags so their own children
// inherit too; files get only the ACEs that apply to files (OI). ResetDataDir sets it on
// every child, so the result doesn't depend on the OS propagating inheritance.
func InheritedDACL(dir bool) (string, error) {
	i := strings.Index(DataSDDL, "D:")
	if i < 0 {
		return "", fmt.Errorf("winacl: no DACL in %q", DataSDDL)
	}
	rest := DataSDDL[i+2:]
	j := strings.Index(rest, "(")
	if j < 0 {
		return "", fmt.Errorf("winacl: no ACEs in %q", DataSDDL)
	}
	var b strings.Builder
	b.WriteString("D:AI") // auto-inherited, not protected
	for _, ace := range strings.Split(strings.TrimSuffix(rest[j+1:], ")"), ")(") {
		f := strings.Split(ace, ";")
		if len(f) != 6 {
			return "", fmt.Errorf("winacl: unexpected ACE %q", ace)
		}
		flags := f[1]
		oi, ci := strings.Contains(flags, "OI"), strings.Contains(flags, "CI")
		switch {
		case dir && (oi || ci):
			f[1] = flags + "ID"
		case !dir && oi:
			f[1] = "ID"
		default:
			continue // not inherited by this kind of object
		}
		b.WriteString("(" + strings.Join(f, ";") + ")")
	}
	return b.String(), nil
}

// broadTrustees are the groups a data folder must never let in (re-test N2): every
// local user would read the logs, or plant files.
var broadTrustees = map[string]string{
	"WD": "Everyone", "S-1-1-0": "Everyone",
	"BU": "Users", "S-1-5-32-545": "Users",
	"AU": "Authenticated Users", "S-1-5-11": "Authenticated Users",
	"IU": "Interactive", "S-1-5-4": "Interactive",
}

// SharedTrustees returns the broad groups (Everyone, Users, Authenticated Users,
// Interactive) that an SDDL's DACL allows any access, in order, without repeats.
func SharedTrustees(sddl string) []string {
	i := strings.Index(sddl, "D:")
	if i < 0 {
		return nil
	}
	rest := sddl[i+2:]
	if j := strings.Index(rest, "S:"); j >= 0 { // the SACL, if any, follows
		rest = rest[:j]
	}
	var out []string
	for _, part := range strings.Split(rest, "(")[1:] {
		f := strings.Split(strings.TrimSuffix(strings.TrimSpace(part), ")"), ";")
		if len(f) < 6 || (f[0] != "A" && f[0] != "OA") {
			continue
		}
		if name, ok := broadTrustees[strings.TrimSuffix(f[5], ")")]; ok {
			dup := false
			for _, o := range out {
				dup = dup || o == name
			}
			if !dup {
				out = append(out, name)
			}
		}
	}
	return out
}
