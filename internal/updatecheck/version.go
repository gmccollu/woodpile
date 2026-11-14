package updatecheck

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"
)

// Version is a SemVer version without build metadata ("1.2.0", "1.2.0-rc10").
type Version struct {
	Major, Minor, Patch int
	Pre                 []string // prerelease identifiers; nil for a release
}

// maxVersionLen bounds what ParseVersion looks at (a tag in a hostile file).
const maxVersionLen = 64

var (
	coreRE  = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)
	identRE = regexp.MustCompile(`^[0-9A-Za-z-]+$`)
	// releasePreRE is the prerelease part of a real release tag: rc1, rc.1, beta2,
	// alpha. A git describe suffix ("5-gabc1234-dirty") or a snapshot's
	// ("snapshot.abc1234") doesn't match.
	releasePreRE = regexp.MustCompile(`^[a-z]+(\.?[0-9]+)?$`)
)

// ParseVersion parses s, with or without a leading "v". Build metadata ("+…")
// isn't accepted.
func ParseVersion(s string) (Version, bool) {
	if len(s) > maxVersionLen {
		return Version{}, false
	}
	s = strings.TrimPrefix(s, "v")
	core, pre, hasPre := strings.Cut(s, "-")
	m := coreRE.FindStringSubmatch(core)
	if m == nil {
		return Version{}, false
	}
	var v Version
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	v.Patch, _ = strconv.Atoi(m[3])
	if hasPre {
		for _, id := range strings.Split(pre, ".") {
			if !identRE.MatchString(id) {
				return Version{}, false
			}
			if isNumeric(id) && len(id) > 1 && id[0] == '0' {
				return Version{}, false // SemVer: no leading zeros in numeric identifiers
			}
			v.Pre = append(v.Pre, id)
		}
	}
	return v, true
}

// String is the version without a "v".
func (v Version) String() string {
	s := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// IsPrerelease reports whether v has a prerelease part.
func (v Version) IsPrerelease() bool { return len(v.Pre) > 0 }

// Compare orders versions as SemVer does, except that an identifier made of
// letters then digits compares its letters, then its number: rc10 is newer than
// rc9 (SPEC §10.2).
func (v Version) Compare(o Version) int {
	if c := cmp.Compare(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmp.Compare(v.Patch, o.Patch); c != 0 {
		return c
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1 // a release is newer than its prereleases
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		if c := compareIdent(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(v.Pre), len(o.Pre))
}

func compareIdent(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		return compareDigits(a, b)
	case an:
		return -1 // numeric identifiers sort before alphanumeric ones
	case bn:
		return 1
	}
	ap, ad := splitLettersDigits(a)
	bp, bd := splitLettersDigits(b)
	if ad != "" || bd != "" {
		if ap == bp {
			if ad == "" || bd == "" {
				return cmp.Compare(len(ad), len(bd)) // "rc" before "rc1"
			}
			return compareDigits(ad, bd)
		}
		return strings.Compare(ap, bp)
	}
	return strings.Compare(a, b)
}

// splitLettersDigits splits "rc10" into "rc" and "10". An identifier that isn't
// letters followed by digits comes back whole, with no digits.
func splitLettersDigits(s string) (string, string) {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	if i == 0 || i == len(s) {
		return s, ""
	}
	for _, c := range s[:i] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return s, ""
		}
	}
	return s[:i], s[i:]
}

// compareDigits compares two digit strings as numbers of any length.
func compareDigits(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if c := cmp.Compare(len(a), len(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ReleaseBuild parses the running build's version and reports whether it is a
// release (stable or prerelease) that may check: a SemVer version that isn't
// 0.0.0, with no build metadata and a prerelease part, if any, like a real tag's
// (rc1, beta.2). Development builds (0.0.0-dev, a git describe version, a CI
// snapshot) never check or ask (SPEC §10.2).
func ReleaseBuild(version string) (Version, bool) {
	v, ok := ParseVersion(version)
	if !ok || (v.Major == 0 && v.Minor == 0 && v.Patch == 0) {
		return Version{}, false
	}
	if len(v.Pre) > 0 {
		pre := strings.Join(v.Pre, ".")
		if !releasePreRE.MatchString(pre) {
			return Version{}, false
		}
		switch strings.TrimRight(strings.ReplaceAll(pre, ".", ""), "0123456789") {
		case "dev", "snapshot", "dirty", "ci":
			return Version{}, false
		}
	}
	return v, true
}
