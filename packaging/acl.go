package packaging

import (
	"fmt"
	"strings"
)

// InheritedDACL returns, as SDDL, the DACL that a folder (dir=true) or file inside the
// data folder has when it only inherits from WindowsDataSDDL: each inheritable ACE of the
// data folder, marked inherited (ID). Folders keep the inheritance flags so their own
// children inherit too; files get only the ACEs that apply to files (OI). Used to reset
// every child of the data folder (ResetDataACL) without relying on the OS to propagate.
func InheritedDACL(dir bool) (string, error) {
	i := strings.Index(WindowsDataSDDL, "D:")
	if i < 0 {
		return "", fmt.Errorf("packaging: no DACL in %q", WindowsDataSDDL)
	}
	rest := WindowsDataSDDL[i+2:]
	j := strings.Index(rest, "(")
	if j < 0 {
		return "", fmt.Errorf("packaging: no ACEs in %q", WindowsDataSDDL)
	}
	var b strings.Builder
	b.WriteString("D:AI") // auto-inherited, not protected
	for _, ace := range strings.Split(strings.TrimSuffix(rest[j+1:], ")"), ")(") {
		f := strings.Split(ace, ";")
		if len(f) != 6 {
			return "", fmt.Errorf("packaging: unexpected ACE %q", ace)
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
