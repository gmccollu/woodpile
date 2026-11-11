package logfile

import (
	"strconv"
	"strings"
	"time"
)

// DayLayout is the layout of the day part of a file name.
const DayLayout = "2006-01-02"

// Name is a parsed log file name. A day's files are, in time order,
// "<day>.001.log", "<day>.002.log", ..., then "<day>.log": the active file is always
// "<day>.log", and when it reaches the part size it is renamed to the next numbered
// part. Compressed files add ".gz". Plain `ls` order is therefore time order.
type Name struct {
	Day        string // YYYY-MM-DD (UTC)
	Part       int    // 0 for "<day>.log", else the part number
	Compressed bool
}

// String returns the file's base name.
func (n Name) String() string {
	var b strings.Builder
	b.WriteString(n.Day)
	if n.Part > 0 {
		b.WriteByte('.')
		p := strconv.Itoa(n.Part)
		for i := len(p); i < 3; i++ {
			b.WriteByte('0')
		}
		b.WriteString(p)
	}
	b.WriteString(".log")
	if n.Compressed {
		b.WriteString(".gz")
	}
	return b.String()
}

// ParseName parses a log file's base name. It reports false for anything that isn't a
// Woodpile log file (temp files, user files).
func ParseName(base string) (Name, bool) {
	var n Name
	if s, ok := strings.CutSuffix(base, ".gz"); ok {
		n.Compressed = true
		base = s
	}
	s, ok := strings.CutSuffix(base, ".log")
	if !ok || len(s) < len(DayLayout) {
		return Name{}, false
	}
	n.Day = s[:len(DayLayout)]
	if _, err := time.Parse(DayLayout, n.Day); err != nil {
		return Name{}, false
	}
	rest := s[len(DayLayout):]
	if rest != "" {
		if len(rest) < 4 || rest[0] != '.' {
			return Name{}, false
		}
		for _, c := range rest[1:] {
			if c < '0' || c > '9' {
				return Name{}, false
			}
		}
		p, err := strconv.Atoi(rest[1:])
		if err != nil || p <= 0 {
			return Name{}, false
		}
		n.Part = p
	}
	return n, true
}

// DayOf returns the UTC day of t as YYYY-MM-DD.
func DayOf(t time.Time) string {
	y, m, d := t.UTC().Date()
	if y < 0 || y > 9999 {
		return t.UTC().Format(DayLayout)
	}
	b := []byte{
		byte('0' + y/1000), byte('0' + y/100%10), byte('0' + y/10%10), byte('0' + y%10), '-',
		byte('0' + int(m)/10), byte('0' + int(m)%10), '-', byte('0' + d/10), byte('0' + d%10),
	}
	return string(b)
}

// DayStart returns 00:00 UTC of a YYYY-MM-DD day.
func DayStart(day string) (time.Time, error) { return time.Parse(DayLayout, day) }
