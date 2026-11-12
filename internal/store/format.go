package store

import (
	"net/netip"
	"time"

	"github.com/gmccollu/woodshed/internal/store/logfile"
)

// The line format lives in package logfile; these are shortcuts for callers that only
// import store.

// Escape appends raw with CR, LF and backslash escaped as \r, \n and \\.
func Escape(dst, raw []byte) []byte { return logfile.AppendEscaped(dst, raw) }

// Unescape appends the original bytes of an escaped message; it reverses Escape.
func Unescape(dst, esc []byte) []byte { return logfile.Unescape(dst, esc) }

// FormatLine appends one log line, with its trailing newline.
func FormatLine(dst []byte, received time.Time, sender netip.Addr, raw []byte) []byte {
	return logfile.AppendLine(dst, received, sender, raw)
}

// ParseLine parses one log line.
func ParseLine(line []byte) (logfile.Line, error) { return logfile.ParseLine(line) }
