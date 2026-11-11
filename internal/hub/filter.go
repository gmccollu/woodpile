package hub

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gmccollu/woodpile/internal/model"
)

// Filter selects the messages a subscriber gets. Zero fields match everything.
type Filter struct {
	Devices   []string // device IDs; empty means every device not hidden (Options.HiddenOf)
	Group     string   // only devices in this group
	Ungrouped bool     // only devices with no group (an alternative to Group)
	// MinSeverity is the numeric severity ceiling (0 emerg … 7 debug): messages with
	// severity <= *MinSeverity pass. Messages with an unknown severity always pass.
	// Nil means no severity filter.
	MinSeverity *int
	// Severities, when not zero, is the exact set of severities that pass
	// (docs/api.md, "Severity filters"). It wins over MinSeverity.
	Severities SeveritySet
	Q          string // case-insensitive substring of the raw message
}

// SeveritySet is a set of syslog severities 0-7 plus "unknown" (no parseable
// PRI). The zero value means "no severity filter" (all pass), as an empty
// severities= does; SeverityNone (severities=none) lets nothing pass.
type SeveritySet uint16

// SeverityUnknownBit is the set bit for messages with no parseable PRI.
const SeverityUnknownBit SeveritySet = 1 << 8

// SeverityNone is the set that matches nothing (severities=none). Has is false
// for every severity.
const SeverityNone SeveritySet = 1 << 9

// AllSeverities holds 0-7 and unknown.
const AllSeverities SeveritySet = 0x1ff

// Has reports whether sev (0-7, or model.Unknown) is in the set.
func (s SeveritySet) Has(sev int) bool {
	if sev < 0 || sev > 7 {
		return s&SeverityUnknownBit != 0
	}
	return s&(1<<uint(sev)) != 0
}

// With returns s with sev (0-7, or model.Unknown) added.
func (s SeveritySet) With(sev int) SeveritySet {
	if sev < 0 || sev > 7 {
		return s | SeverityUnknownBit
	}
	return s | 1<<uint(sev)
}

// ParseSeverities reads a severities= value: a comma-separated list of 0-7 and
// "unknown", or "none" on its own (SeverityNone). Blank items are ignored, so ""
// is the zero set (meaning all).
func ParseSeverities(v string) (SeveritySet, error) {
	var s SeveritySet
	none := false
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		switch {
		case item == "":
		case strings.EqualFold(item, "none"):
			none = true
		case strings.EqualFold(item, "unknown"):
			s |= SeverityUnknownBit
		case len(item) == 1 && item[0] >= '0' && item[0] <= '7':
			s |= 1 << uint(item[0]-'0')
		default:
			return 0, fmt.Errorf("severities: %q is not a severity (0 to 7, unknown, or none on its own)", item)
		}
	}
	if none {
		if s != 0 {
			return 0, fmt.Errorf("severities: none can't be combined with other values")
		}
		return SeverityNone, nil
	}
	return s, nil
}

// ParseFilter reads a Filter from /api/live query parameters: devices (comma
// separated), group, ungrouped=true, severities (0-7 and unknown, comma
// separated), min_severity (0-7) and q. Both severity forms are validated; when
// severities is given (and not empty) it wins over min_severity.
func ParseFilter(v url.Values) (Filter, error) {
	var f Filter
	for _, d := range strings.Split(v.Get("devices"), ",") {
		if d = strings.TrimSpace(d); d != "" {
			f.Devices = append(f.Devices, d)
		}
	}
	f.Group = v.Get("group")
	switch u := v.Get("ungrouped"); u {
	case "", "false", "0":
	case "true", "1":
		f.Ungrouped = true
	default:
		return Filter{}, fmt.Errorf("ungrouped must be true or false, not %q", u)
	}
	if f.Group != "" && f.Ungrouped {
		return Filter{}, errors.New("group and ungrouped=true are alternatives; send one")
	}
	if s := v.Get("min_severity"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 7 {
			return Filter{}, fmt.Errorf("min_severity must be an integer from 0 to 7, not %q", s)
		}
		f.MinSeverity = &n
	}
	set, err := ParseSeverities(v.Get("severities"))
	if err != nil {
		return Filter{}, err
	}
	if set != 0 {
		f.Severities, f.MinSeverity = set, nil
	}
	f.Q = v.Get("q")
	return f, nil
}

// compiled is a Filter prepared for matching.
type compiled struct {
	devices   map[string]bool
	group     string
	ungrouped bool
	maxSev    int         // -1: no severity ceiling
	sevs      SeveritySet // 0: no severity set
	q         []byte
	qASCII    bool
}

func compile(f Filter) *compiled {
	c := &compiled{group: f.Group, ungrouped: f.Ungrouped, maxSev: -1}
	if len(f.Devices) > 0 {
		c.devices = make(map[string]bool, len(f.Devices))
		for _, d := range f.Devices {
			c.devices[d] = true
		}
	}
	if f.Severities != 0 && f.Severities&AllSeverities != AllSeverities {
		c.sevs = f.Severities
	} else if f.Severities == 0 && f.MinSeverity != nil {
		c.maxSev = *f.MinSeverity
	}
	if f.Q != "" {
		c.q = bytes.ToLower([]byte(f.Q))
		c.qASCII = isASCII(c.q)
	}
	return c
}

func (c *compiled) match(e *Entry, g *groupLookup) bool {
	if c.devices != nil && !c.devices[e.DeviceID] {
		return false
	}
	if c.devices == nil && g.hidden() {
		return false // hidden devices only when named
	}
	if c.maxSev >= 0 && e.Severity != model.Unknown && e.Severity > c.maxSev {
		return false
	}
	if c.sevs != 0 && !c.sevs.Has(e.Severity) {
		return false
	}
	if c.group != "" && g.get() != c.group {
		return false
	}
	if c.ungrouped && g.get() != "" {
		return false
	}
	if c.q != nil {
		if c.qASCII {
			return containsFoldASCII(e.Raw, c.q)
		}
		return bytes.Contains(bytes.ToLower(e.Raw), c.q)
	}
	return true
}

// Match reports whether the filter selects e, with group giving a device's group
// (no device is hidden; see Options.HiddenOf).
func (f Filter) Match(e Entry, group func(deviceID string) string) bool {
	if group == nil {
		group = func(string) string { return "" }
	}
	return compile(f).match(&e, &groupLookup{f: group, id: e.DeviceID})
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// containsFoldASCII reports whether s contains the lower-case ASCII needle q,
// ignoring ASCII case in s. It doesn't allocate.
func containsFoldASCII(s, q []byte) bool {
	n := len(q)
	if n == 0 {
		return true
	}
	first := q[0]
	for i := 0; i+n <= len(s); i++ {
		if lowerASCII(s[i]) != first {
			continue
		}
		j := 1
		for j < n && lowerASCII(s[i+j]) == q[j] {
			j++
		}
		if j == n {
			return true
		}
	}
	return false
}
