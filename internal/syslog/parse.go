// Package syslog parses syslog messages and frames them off the wire.
//
// Parse is lenient by design: it reads RFC 5424, RFC 3164 and the common
// non-compliant shapes real devices send, fills what it can recognise and
// leaves the rest zero. It never fails; the raw bytes are what gets stored.
//
// StreamReader splits a TCP stream into messages (RFC 6587 octet counting or
// newline framing, detected per message), and TrimDatagram prepares a UDP
// datagram, which is always exactly one message.
package syslog

import (
	"strings"
	"time"

	"github.com/gmccollu/woodpile/internal/model"
)

const (
	maxHostLen = 255 // RFC 5424 HOSTNAME limit
	maxAppLen  = 64  // RFC 5424 allows 48; a little slack for real tags
	maxPRI     = 191 // facility 23, severity 7
)

type span struct{ s, e int }

type result struct {
	fac, sev  int
	ts        time.Time
	host, app span
}

// Parse fills m.Facility, m.Severity, m.Timestamp, m.Hostname and m.App from
// raw. It overwrites all five (fields it cannot read are reset to their zero
// values, and Facility/Severity to model.Unknown when there is no
// valid PRI). It never modifies raw, never panics and keeps no reference to
// raw: Hostname and App are copied (one allocation for both, none when both
// are empty).
//
// Timestamps that carry an offset or zone are converted to that instant in
// UTC. Timestamps without one are read as UTC wall-clock time. A timestamp
// without a year gets one from m.Received (see stamp.time); if m.Received is
// zero, such a timestamp is left zero.
func Parse(raw []byte, m *model.Message) {
	r := result{fac: model.Unknown, sev: model.Unknown}
	b := raw
	i := skipLeading(b, 0, true)
	hasPRI := false
	if j, v, ok := parsePRI(b, i); ok {
		hasPRI = true
		if v <= maxPRI {
			r.fac, r.sev = v>>3, v&7
		}
		i = skipLeading(b, j, false)
	}
	if !parse5424(b, i, hasPRI, &r) {
		parseLegacy(b, i, m.Received, &r)
	}
	m.Facility, m.Severity, m.Timestamp = r.fac, r.sev, r.ts
	m.Hostname, m.App = r.strings(b)
}

// strings copies the host and app spans out of b in a single allocation.
func (r *result) strings(b []byte) (host, app string) {
	h, a := b[r.host.s:r.host.e], b[r.app.s:r.app.e]
	if len(h)+len(a) == 0 {
		return "", ""
	}
	var sb strings.Builder
	sb.Grow(len(h) + len(a))
	sb.Write(h)
	sb.Write(a)
	s := sb.String()
	return s[:len(h)], s[len(h):]
}

// skipLeading skips UTF-8 BOMs and blanks. Before the PRI it also skips line
// breaks and NULs left over from framing.
func skipLeading(b []byte, i int, lineBreaks bool) int {
	for i < len(b) {
		switch c := b[i]; {
		case c == 0xEF && i+2 < len(b) && b[i+1] == 0xBB && b[i+2] == 0xBF:
			i += 3
		case c == ' ' || c == '\t':
			i++
		case lineBreaks && (c == '\r' || c == '\n' || c == 0):
			i++
		default:
			return i
		}
	}
	return i
}

// parsePRI reads "<N>" with 1-3 digits. A syntactically valid PRI above 191
// is still consumed (ok is true) so the rest of the header can be read.
func parsePRI(b []byte, i int) (end, v int, ok bool) {
	if i >= len(b) || b[i] != '<' {
		return i, 0, false
	}
	v, j, ok := digits(b, i+1, 1, 3)
	if !ok || j >= len(b) || b[j] != '>' {
		return i, 0, false
	}
	return j + 1, v, true
}

// parse5424 reads "VERSION SP TIMESTAMP SP HOSTNAME SP APP-NAME" at i. It
// reports false, changing nothing, when b doesn't start with that header.
// A NILVALUE timestamp is accepted only after a PRI, since "1 - ..." alone is
// too weak a signal.
func parse5424(b []byte, i int, hasPRI bool, r *result) bool {
	if i >= len(b) || b[i] < '1' || b[i] > '9' {
		return false
	}
	_, j, _ := digits(b, i, 1, 3)
	if j >= len(b) || b[j] != ' ' {
		return false
	}
	j++
	var ts time.Time
	if isNil(b, j) {
		if !hasPRI {
			return false
		}
		j++
	} else {
		st, e, ok := parseISO(b, j)
		if !ok || (e < len(b) && b[e] != ' ') {
			return false
		}
		ts, j = st.time(time.Time{}), e
	}
	r.ts = ts
	j = field5424(b, j, maxHostLen, &r.host)
	field5424(b, j, maxAppLen, &r.app)
	return true
}

// isNil reports whether the field at i is the RFC 5424 NILVALUE "-".
func isNil(b []byte, i int) bool {
	return i < len(b) && b[i] == '-' && (i+1 == len(b) || b[i+1] == ' ')
}

// field5424 reads " FIELD" at i into sp (left empty for "-" or a value that
// isn't printable US-ASCII or is longer than maxLen) and returns its end.
func field5424(b []byte, i, maxLen int, sp *span) int {
	if i >= len(b) || b[i] != ' ' {
		return i
	}
	i++
	e := i
	for e < len(b) && b[e] != ' ' {
		e++
	}
	if e > i && e-i <= maxLen && !isNil(b, i) && printable(b[i:e]) {
		*sp = span{i, e}
	}
	return e
}

// parseLegacy reads everything that isn't an RFC 5424 header:
//
//	[SEQ: ] [HOST: ] TIMESTAMP[:] [HOST [:]] [TAG] MSG
//
// where TAG is "name:", "name[pid]:" or a "%FAC-N-MNEMONIC:" tag. Without a
// timestamp only strong tags are believed (see noStamp).
func parseLegacy(b []byte, i int, received time.Time, r *result) {
	i = skipSeq(b, i)
	if s, e := tokenAt(b, i); e-s >= 2 && b[e-1] == ':' && legacyHost(b[s:e-1]) {
		if st, end, ok := parseLegacyStamp(b, skipSpaces(b, e)); ok {
			r.host = span{s, e - 1}
			r.ts = st.time(received)
			afterStamp(b, end, true, r)
			return
		}
	}
	if st, end, ok := parseLegacyStamp(b, i); ok {
		r.ts = st.time(received)
		afterStamp(b, end, false, r)
		return
	}
	noStamp(b, i, r)
}

// skipSeq skips a leading sequence number written as "123: ".
func skipSeq(b []byte, i int) int {
	_, j, ok := digits(b, i, 1, 10)
	if ok && j+1 < len(b) && b[j] == ':' && b[j+1] == ' ' {
		return skipSpaces(b, j+1)
	}
	return i
}

// afterStamp reads the hostname and tag after a timestamp ending at i.
func afterStamp(b []byte, i int, haveHost bool, r *result) {
	colon := i < len(b) && b[i] == ':'
	if colon {
		i++
	}
	i = skipSpaces(b, i)
	s, e := tokenAt(b, i)
	if colon || haveHost {
		// A ':' right after the timestamp ends the header: no hostname follows.
		if a, _, ok := parseTag(b, s, e); ok {
			r.app = a
		}
		return
	}
	if e-s == 1 && b[s] == ':' {
		s, e = tokenAt(b, skipSpaces(b, e))
		if a, _, ok := parseTag(b, s, e); ok {
			r.app = a
		}
		return
	}
	if a, strong, ok := parseTag(b, s, e); ok {
		// "host: %TAG:" or "host: name[pid]:" (the host carries a colon).
		if !strong && legacyHost(b[s:e-1]) {
			s2, e2 := tokenAt(b, skipSpaces(b, e))
			if a2, strong2, ok2 := parseTag(b, s2, e2); ok2 && strong2 {
				r.host, r.app = span{s, e - 1}, a2
				return
			}
		}
		r.app = a // no hostname, just a tag
		return
	}
	switch {
	case e-s == 1 && b[s] == '-': // a NILVALUE-style empty hostname
	case legacyHost(b[s:e]):
		r.host = span{s, e}
	default:
		return
	}
	s, e = tokenAt(b, skipSpaces(b, e))
	if e-s == 1 && b[s] == ':' { // "host : tag"
		s, e = tokenAt(b, skipSpaces(b, e))
	}
	if a, _, ok := parseTag(b, s, e); ok {
		r.app = a
	}
}

// noStamp handles messages without a recognisable timestamp. Here "word:" is
// as likely to be prose ("Disk full: /var") as a tag, so only strong tags
// count: "name[pid]" or a %-tag, alone or after a hostname.
func noStamp(b []byte, i int, r *result) {
	s, e := tokenAt(b, i)
	if a, strong, ok := parseTag(b, s, e); ok && strong {
		r.app = a
		return
	}
	hs, he := s, e
	if he-hs >= 2 && b[he-1] == ':' {
		he--
	}
	if !legacyHost(b[hs:he]) {
		return
	}
	s2, e2 := tokenAt(b, skipSpaces(b, e))
	if a, strong, ok := parseTag(b, s2, e2); ok && strong {
		r.host, r.app = span{hs, he}, a
	}
}

// parseTag recognises a tag token b[s:e] and returns the app-name span.
// Strong tags are "name[pid]" (with an optional ':') and %-tags ending in
// ':' or shaped like %FAC-N-MNEMONIC; "name:" is a weak tag.
func parseTag(b []byte, s, e int) (app span, strong, ok bool) {
	if e-s < 2 {
		return span{}, false, false
	}
	t := b[s:e]
	if t[0] == '%' {
		n := len(t)
		colon := t[n-1] == ':'
		if colon {
			n--
		}
		if n >= 2 && appName(t[:n]) && (colon || pctShape(t[1:n])) {
			return span{s, s + n}, true, true
		}
		return span{}, false, false
	}
	for k := 1; k < len(t); k++ {
		if t[k] != '[' {
			continue
		}
		q := k + 1
		for q < len(t) && t[q] != ']' {
			q++
		}
		if q == len(t) || !(q == len(t)-1 || (q == len(t)-2 && t[q+1] == ':')) || !appName(t[:k]) {
			return span{}, false, false
		}
		return span{s, s + k}, true, true
	}
	if t[len(t)-1] == ':' && appName(t[:len(t)-1]) {
		return span{s, e - 1}, false, true
	}
	return span{}, false, false
}

// pctShape reports whether t (without the '%') looks like FAC[-SUB]-N-MNEMONIC:
// at least three '-'-separated alphanumeric parts with a single-digit
// severity in a middle part.
func pctShape(t []byte) bool {
	parts, start, sevMid := 0, 0, false
	for i := 0; i <= len(t); i++ {
		if i < len(t) && t[i] != '-' {
			if !isAlnum(t[i]) && t[i] != '_' {
				return false
			}
			continue
		}
		if i == start {
			return false
		}
		if parts > 0 && i < len(t) && i-start == 1 && t[start] >= '0' && t[start] <= '7' {
			sevMid = true
		}
		parts++
		start = i + 1
	}
	return parts >= 3 && sevMid
}

// tokenAt returns the run of bytes above ' ' starting at i.
func tokenAt(b []byte, i int) (int, int) {
	e := i
	for e < len(b) && b[e] > ' ' {
		e++
	}
	return i, e
}

// legacyHost reports whether t can be a hostname outside an RFC 5424 header:
// letters, digits, '.', '-', '_' and ':' (IPv6), with a letter or digit.
func legacyHost(t []byte) bool {
	if len(t) == 0 || len(t) > maxHostLen || t[0] == '-' || t[0] == '.' {
		return false
	}
	alnum := false
	for _, c := range t {
		switch {
		case isAlnum(c):
			alnum = true
		case c == '.' || c == '-' || c == '_' || c == ':':
		default:
			return false
		}
	}
	return alnum
}

// appName reports whether t can be an app name: printable ASCII without
// brackets or ':', at most maxAppLen bytes, with at least one letter.
func appName(t []byte) bool {
	if len(t) == 0 || len(t) > maxAppLen {
		return false
	}
	letter := false
	for _, c := range t {
		if c <= ' ' || c > '~' || c == '[' || c == ']' || c == ':' {
			return false
		}
		if (c|0x20) >= 'a' && (c|0x20) <= 'z' {
			letter = true
		}
	}
	return letter
}

func printable(t []byte) bool {
	for _, c := range t {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool {
	return isDigit(c) || ((c|0x20) >= 'a' && (c|0x20) <= 'z')
}
