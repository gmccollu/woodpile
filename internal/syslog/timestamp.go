package syslog

import "time"

// futureSlack is how far ahead of the received time a year-less timestamp may
// be before it is read as belonging to the previous year. Device clocks run a
// little fast or sit in a time zone ahead of UTC; anything further ahead is
// far more likely last year's date (a December message received in January).
const futureSlack = 7 * 24 * time.Hour

// stamp is a timestamp as written in a message, before it becomes a time.Time.
type stamp struct {
	year, month, day     int
	hour, min, sec, nsec int
	off                  int  // seconds east of UTC
	yearSet              bool // the message carried a year
	zoneBad              bool // a named zone whose offset is ambiguous or unknown
	offSet               bool // an offset (numeric, Z or a known zone name) was given
}

// time resolves the stamp. Without a zone the wall clock is read as UTC.
// Without a year the year is inferred from received; a zero received, an
// ambiguous zone or an impossible date gives the zero time.
func (st *stamp) time(received time.Time) time.Time {
	if st.zoneBad || st.month < 1 || st.month > 12 || st.day < 1 ||
		st.hour > 23 || st.min > 59 || st.sec > 60 {
		return time.Time{}
	}
	if st.yearSet {
		t, _ := st.at(st.year)
		return t
	}
	if received.IsZero() {
		return time.Time{}
	}
	r := received.UTC()
	limit := r.Add(futureSlack)
	y := r.Year()
	// Latest of next, this and last year that isn't too far in the future.
	// "Next year" only wins for a January date received in late December.
	for _, cy := range [3]int{y + 1, y, y - 1} {
		if t, ok := st.at(cy); ok && !t.After(limit) {
			return t
		}
	}
	return time.Time{}
}

func (st *stamp) at(y int) (time.Time, bool) {
	if st.day > daysIn(st.month, y) {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(st.month), st.day, st.hour, st.min, st.sec, st.nsec, time.UTC)
	if st.off != 0 {
		t = t.Add(-time.Duration(st.off) * time.Second)
	}
	return t, true
}

func daysIn(month, year int) int {
	switch month {
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	case 4, 6, 9, 11:
		return 30
	}
	return 31
}

// zones maps zone abbreviations seen after syslog timestamps to their UTC
// offset in seconds. zoneAmbiguous marks names used for more than one offset
// (CST is both UTC-6 and UTC+8); those leave the timestamp zero.
const zoneAmbiguous = 1 << 30

var zones = map[string]int{
	"UTC": 0, "GMT": 0, "UT": 0, "Z": 0,
	"EST": -5 * 3600, "EDT": -4 * 3600,
	"MST": -7 * 3600, "MDT": -6 * 3600,
	"PST": -8 * 3600, "PDT": -7 * 3600,
	"AKST": -9 * 3600, "AKDT": -8 * 3600, "HST": -10 * 3600,
	"WET": 0, "WEST": 3600, "CET": 3600, "CEST": 2 * 3600,
	"EET": 2 * 3600, "EEST": 3 * 3600, "MSK": 3 * 3600,
	"HKT": 8 * 3600, "SGT": 8 * 3600, "AWST": 8 * 3600,
	"JST": 9 * 3600, "KST": 9 * 3600,
	"ACST": 9*3600 + 1800, "ACDT": 10*3600 + 1800,
	"AEST": 10 * 3600, "AEDT": 11 * 3600,
	"NZST": 12 * 3600, "NZDT": 13 * 3600,
	"CST": zoneAmbiguous, "CDT": zoneAmbiguous, "IST": zoneAmbiguous,
	"BST": zoneAmbiguous, "AST": zoneAmbiguous, "ADT": zoneAmbiguous,
}

// parseISO reads YYYY-MM-DD('T'|' ')hh:mm:ss[.frac][offset] at i. It checks
// shape only; impossible values are caught later by stamp.time.
func parseISO(b []byte, i int) (st stamp, end int, ok bool) {
	y, j, ok := digits(b, i, 4, 4)
	if !ok || j >= len(b) || b[j] != '-' {
		return st, i, false
	}
	mo, j, ok := digits(b, j+1, 2, 2)
	if !ok || j >= len(b) || b[j] != '-' {
		return st, i, false
	}
	d, j, ok := digits(b, j+1, 2, 2)
	if !ok || j >= len(b) || (b[j] != 'T' && b[j] != 't' && b[j] != ' ') {
		return st, i, false
	}
	j, ok = parseClock(b, j+1, &st)
	if !ok {
		return st, i, false
	}
	st.year, st.month, st.day, st.yearSet = y, mo, d, true
	if off, e, ok := parseOffset(b, j); ok {
		st.off, st.offSet, j = off, true, e
	}
	return st, j, true
}

// parseClock reads h[h]:mm:ss[(.|,)frac] at i into st.
func parseClock(b []byte, i int, st *stamp) (int, bool) {
	h, j, ok := digits(b, i, 1, 2)
	if !ok || j >= len(b) || b[j] != ':' {
		return i, false
	}
	mi, j, ok := digits(b, j+1, 2, 2)
	if !ok || j >= len(b) || b[j] != ':' {
		return i, false
	}
	s, j, ok := digits(b, j+1, 2, 2)
	if !ok {
		return i, false
	}
	ns := 0
	if j+1 < len(b) && (b[j] == '.' || b[j] == ',') && isDigit(b[j+1]) {
		j++
		n := 0
		for ; j < len(b) && isDigit(b[j]); j++ {
			if n < 9 {
				ns = ns*10 + int(b[j]-'0')
				n++
			}
		}
		for ; n < 9; n++ {
			ns *= 10
		}
	}
	st.hour, st.min, st.sec, st.nsec = h, mi, s, ns
	return j, true
}

// parseOffset reads Z, z, ±hh, ±hhmm or ±hh:mm at i.
func parseOffset(b []byte, i int) (off, end int, ok bool) {
	if i >= len(b) {
		return 0, i, false
	}
	c := b[i]
	if c == 'Z' || c == 'z' {
		return 0, i + 1, true
	}
	if c != '+' && c != '-' {
		return 0, i, false
	}
	hh, j, ok := digits(b, i+1, 2, 2)
	if !ok {
		return 0, i, false
	}
	mm := 0
	if j+2 < len(b) && b[j] == ':' && isDigit(b[j+1]) && isDigit(b[j+2]) {
		mm, j = int(b[j+1]-'0')*10+int(b[j+2]-'0'), j+3
	} else if v, k, ok := digits(b, j, 2, 2); ok {
		mm, j = v, k
	}
	if hh > 23 || mm > 59 {
		return 0, i, false
	}
	off = hh*3600 + mm*60
	if c == '-' {
		off = -off
	}
	return off, j, true
}

// parseLegacyStamp reads the timestamps found outside an RFC 5424 header:
//
//	Nov 10 20:41:07                  RFC 3164 (day may be space-padded)
//	Nov 10 20:41:07.123              with fractional seconds
//	2026 Nov 10 20:41:07.123         leading year
//	Nov 10 2026 20:41:07             year after the day
//	Nov 10 20:41:07 2026             year after the time
//	2026-11-10T20:41:07.123+01:00    ISO 8601 (also with a space for T)
//
// optionally prefixed by '*' or '.' (clock-sync markers) and followed by a
// zone: an attached or separate numeric offset, or a zone name. The stamp
// must end at whitespace, ':' or the end of b.
func parseLegacyStamp(b []byte, i int) (st stamp, end int, ok bool) {
	j := i
	if j < len(b) && (b[j] == '*' || b[j] == '.') {
		j++
	}
	if st, e, ok := parseISO(b, j); ok {
		if !st.offSet {
			e = zoneSuffix(b, e, &st)
		}
		return st, e, boundary(b, e)
	}
	if y, e, ok := year4(b, j); ok && e < len(b) && b[e] == ' ' {
		st.year, st.yearSet = y, true
		j = skipSpaces(b, e)
	}
	mon := monthAt(b, j)
	if mon == 0 || j+3 >= len(b) || b[j+3] != ' ' {
		return st, i, false
	}
	j = skipSpaces(b, j+3)
	day, j, ok := digits(b, j, 1, 2)
	if !ok || j >= len(b) || b[j] != ' ' {
		return st, i, false
	}
	j = skipSpaces(b, j)
	if !st.yearSet {
		if y, e, ok := year4(b, j); ok && e < len(b) && b[e] == ' ' {
			st.year, st.yearSet = y, true
			j = skipSpaces(b, e)
		}
	}
	j, ok = parseClock(b, j, &st)
	if !ok {
		return st, i, false
	}
	st.month, st.day = mon, day
	j = zoneSuffix(b, j, &st)
	if !st.yearSet {
		k := skipSpaces(b, j)
		if y, e, ok := year4(b, k); ok && k > j && fieldEnd(b, e) {
			st.year, st.yearSet, j = y, true, e
		}
	}
	return st, j, boundary(b, j)
}

// zoneSuffix consumes an optional zone after a clock at i: an attached numeric
// offset, or a separate token that is a numeric offset or a zone name.
// A trailing ':' on the token is left for the caller.
func zoneSuffix(b []byte, i int, st *stamp) int {
	if off, e, ok := parseOffset(b, i); ok && boundary(b, e) {
		st.off, st.offSet = off, true
		return e
	}
	k := skipSpaces(b, i)
	if k == i || k >= len(b) {
		return i
	}
	if off, e, ok := parseOffset(b, k); ok && e > k+1 && boundary(b, e) {
		st.off, st.offSet = off, true
		return e
	}
	e := k
	for e < len(b) && b[e] >= 'A' && b[e] <= 'Z' {
		e++
	}
	if e == k || !boundary(b, e) {
		return i
	}
	off, ok := zones[string(b[k:e])]
	if !ok {
		return i
	}
	if off == zoneAmbiguous {
		st.zoneBad = true
	} else {
		st.off, st.offSet = off, true
	}
	return e
}

func monthAt(b []byte, i int) int {
	if i+3 > len(b) {
		return 0
	}
	// Lower-case the three letters into one key.
	k := uint32(b[i]|0x20)<<16 | uint32(b[i+1]|0x20)<<8 | uint32(b[i+2]|0x20)
	switch k {
	case 'j'<<16 | 'a'<<8 | 'n':
		return 1
	case 'f'<<16 | 'e'<<8 | 'b':
		return 2
	case 'm'<<16 | 'a'<<8 | 'r':
		return 3
	case 'a'<<16 | 'p'<<8 | 'r':
		return 4
	case 'm'<<16 | 'a'<<8 | 'y':
		return 5
	case 'j'<<16 | 'u'<<8 | 'n':
		return 6
	case 'j'<<16 | 'u'<<8 | 'l':
		return 7
	case 'a'<<16 | 'u'<<8 | 'g':
		return 8
	case 's'<<16 | 'e'<<8 | 'p':
		return 9
	case 'o'<<16 | 'c'<<8 | 't':
		return 10
	case 'n'<<16 | 'o'<<8 | 'v':
		return 11
	case 'd'<<16 | 'e'<<8 | 'c':
		return 12
	}
	return 0
}

// year4 reads exactly four digits at i (not followed by another digit).
func year4(b []byte, i int) (int, int, bool) {
	y, j, ok := digits(b, i, 4, 4)
	if !ok || (j < len(b) && isDigit(b[j])) {
		return 0, i, false
	}
	return y, j, true
}

// digits reads between minN and maxN decimal digits at i.
func digits(b []byte, i, minN, maxN int) (int, int, bool) {
	v, j := 0, i
	for j < len(b) && j-i < maxN && isDigit(b[j]) {
		v = v*10 + int(b[j]-'0')
		j++
	}
	if j-i < minN {
		return 0, i, false
	}
	return v, j, true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// fieldEnd reports whether a field ends at i: at a blank, the end of b, or a
// ':' that is itself followed by a blank or the end (so "2001:db8::5" is not
// a year followed by a colon).
func fieldEnd(b []byte, i int) bool {
	if i < len(b) && b[i] == ':' {
		i++
	}
	return i >= len(b) || b[i] <= ' '
}

// boundary reports whether a field may end at i.
func boundary(b []byte, i int) bool {
	return i >= len(b) || b[i] <= ' ' || b[i] == ':'
}

func skipSpaces(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	return i
}
